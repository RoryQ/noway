package spanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/spanner"
	databaseAdmin "cloud.google.com/go/spanner/admin/database/apiv1"
	"cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	"cloud.google.com/go/spanner/spansql"
	"github.com/google/uuid"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/resolver"
	"github.com/roryq/noway/pkg/version"
)

var (
	// ErrLockRowMissing indicates that the lock row was deleted from the schema history table during heartbeat.
	ErrLockRowMissing = errors.New("lock row missing during heartbeat")

	// ErrLockOwnershipLost indicates that lock ownership was taken by another process during heartbeat.
	ErrLockOwnershipLost = errors.New("lock ownership lost during heartbeat")
)

// SpannerDatabase implements database.Database for Google Cloud Spanner.
type SpannerDatabase struct {
	client      *spanner.Client
	adminClient *databaseAdmin.DatabaseAdminClient
	projectID   string
	instanceID  string
	databaseID  string
	config      *config.Configuration
	mu          sync.Mutex

	// heartbeatInterval is the interval between lock heartbeat refreshes.
	// If <= 0, defaults to 3 minutes.
	heartbeatInterval time.Duration

	// lockTTL is the duration after which a lock row is considered expired.
	// If <= 0, defaults to 10 minutes.
	lockTTL time.Duration

	// onHeartbeat is an optional hook called on each heartbeat tick (used for testing).
	onHeartbeat func(err error)

	// heartbeatAttemptInterceptor is an optional test hook called on each heartbeat attempt.
	heartbeatAttemptInterceptor func() error

	// testHookHistoryTableExistsFalseOnce is an optional test hook that makes HistoryTableExists return false once.
	testHookHistoryTableExistsFalseOnce bool

	activeLockMu  sync.Mutex
	activeLockErr error

	supportsCurrentTS bool
	currentTSWarned   bool
}

func (db *SpannerDatabase) checkSupportsCurrentTS(ctx context.Context) bool {
	db.mu.Lock()
	if db.supportsCurrentTS {
		db.mu.Unlock()
		return true
	}
	db.mu.Unlock()

	if db.client == nil {
		return false
	}

	iter := db.client.Single().Query(ctx, spanner.NewStatement("SELECT CURRENT_TIMESTAMP()"))
	defer iter.Stop()
	_, err := iter.Next()
	if err == nil {
		db.mu.Lock()
		db.supportsCurrentTS = true
		db.mu.Unlock()
		return true
	}

	db.mu.Lock()
	if !db.currentTSWarned {
		db.currentTSWarned = true
		fmt.Fprintf(os.Stderr, "Warning: Spanner CURRENT_TIMESTAMP() probe failed: %v. Falling back to client clock.\n", err)
	}
	db.mu.Unlock()
	return false
}

var _ database.Database = (*SpannerDatabase)(nil)

// New creates and initializes a new SpannerDatabase instance.
func New(ctx context.Context, cfg *config.Configuration) (*SpannerDatabase, error) {
	projectID := cfg.GCPProjectID
	instanceID := cfg.GCPSpannerInstanceID
	databaseID := cfg.GCPSpannerDatabaseID

	if projectID == "" {
		return nil, fmt.Errorf("Cloud Spanner project ID is required. Set via config, JDBC URL, or GOOGLE_CLOUD_PROJECT env var")
	}
	if instanceID == "" {
		return nil, fmt.Errorf("Cloud Spanner instance ID is required. Set via config, JDBC URL, or SPANNER_INSTANCE_ID env var")
	}
	if databaseID == "" {
		return nil, fmt.Errorf("Cloud Spanner database ID is required. Set via config, JDBC URL, or SPANNER_DATABASE_ID env var")
	}

	var opts []option.ClientOption

	switch {
	case cfg.GCPUseEmulator:
		host := cfg.GCPSpannerEndpoint
		if host == "" {
			host = os.Getenv("SPANNER_EMULATOR_HOST")
		}
		if host == "" {
			host = "localhost:9010"
		}
		if !cfg.GCPEmulatorAllowRemote && !database.IsLoopbackHost(host) {
			return nil, fmt.Errorf("refusing Spanner emulator connection to non-loopback host %q without emulatorAllowRemote=true", host)
		}
		slog.Warn("Spanner emulator mode: no TLS and no authentication", "host", host)
		opts = append(opts, option.WithEndpoint(host), option.WithoutAuthentication(),
			option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())))
	default:
		if cfg.GCPSpannerEndpoint != "" {
			opts = append(opts, option.WithEndpoint(cfg.GCPSpannerEndpoint))
		}
		credOpts, err := database.CredentialOptions(cfg)
		if err != nil {
			return nil, err
		}
		opts = append(opts, credOpts...)
	}

	dbPath := fmt.Sprintf("projects/%s/instances/%s/databases/%s", projectID, instanceID, databaseID)

	client, err := spanner.NewClient(ctx, dbPath, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Spanner client for database %s: %w", dbPath, err)
	}

	adminClient, err := databaseAdmin.NewDatabaseAdminClient(ctx, opts...)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to create Spanner DatabaseAdminClient: %w", err)
	}

	return &SpannerDatabase{
		client:      client,
		adminClient: adminClient,
		projectID:   projectID,
		instanceID:  instanceID,
		databaseID:  databaseID,
		config:      cfg,
	}, nil
}

// Close closes the Spanner client and admin client.
func (db *SpannerDatabase) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	var firstErr error
	if db.client != nil {
		db.client.Close()
	}
	if db.adminClient != nil {
		if err := db.adminClient.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// databasePath returns the fully qualified database path.
func (db *SpannerDatabase) databasePath() string {
	return fmt.Sprintf("projects/%s/instances/%s/databases/%s", db.projectID, db.instanceID, db.databaseID)
}

// instancePath returns the fully qualified instance path.
func (db *SpannerDatabase) instancePath() string {
	return fmt.Sprintf("projects/%s/instances/%s", db.projectID, db.instanceID)
}

// Quote quotes an identifier with backticks.
func (db *SpannerDatabase) Quote(identifiers ...string) string {
	quoted := make([]string, 0, len(identifiers))
	for _, id := range identifiers {
		if id == "" || id == "default" {
			continue
		}
		escaped := strings.ReplaceAll(id, "`", "\\`")
		quoted = append(quoted, "`"+escaped+"`")
	}
	if len(quoted) == 0 {
		return ""
	}
	return strings.Join(quoted, ".")
}

// GetCurrentUser returns the database user session.
func (db *SpannerDatabase) GetCurrentUser(ctx context.Context) (string, error) {
	if db.config != nil && db.config.InstalledBy != "" {
		return db.config.InstalledBy, nil
	}
	if user := os.Getenv("USER"); user != "" {
		return user, nil
	}
	return "spanner", nil
}

// EnsureSchema creates the database if it does not exist (when CreateSchemas is true).
func (db *SpannerDatabase) EnsureSchema(ctx context.Context, schema string) error {
	exists, err := db.SchemaExists(ctx, schema)
	if err == nil && exists {
		return nil
	}

	req := &databasepb.CreateDatabaseRequest{
		Parent:          db.instancePath(),
		CreateStatement: fmt.Sprintf("CREATE DATABASE `%s`", db.databaseID),
	}

	op, err := db.adminClient.CreateDatabase(ctx, req)
	if err != nil {
		if status.Code(err) == codes.AlreadyExists || strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create Spanner database %s: %w", db.databaseID, err)
	}

	_, err = op.Wait(ctx)
	if err != nil {
		if status.Code(err) == codes.AlreadyExists || strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return fmt.Errorf("failed waiting for database creation: %w", err)
	}
	return nil
}

// SchemaExists checks if the Spanner database exists.
func (db *SpannerDatabase) SchemaExists(ctx context.Context, schema string) (bool, error) {
	_, err := db.adminClient.GetDatabase(ctx, &databasepb.GetDatabaseRequest{
		Name: db.databasePath(),
	})
	if err == nil {
		return true, nil
	}
	if status.Code(err) == codes.NotFound || strings.Contains(strings.ToLower(err.Error()), "not found") {
		return false, nil
	}
	return false, err
}

type spannerDDLConstraint struct {
	table      string
	constraint string
}

type spannerDDLObjects struct {
	changeStreams  []string
	tables         []string
	views          []string
	viewDefs       map[string]string
	models         []string
	sequences      []string
	indexes        []string
	searchIndexes  []string
	vectorIndexes  []string
	propertyGraphs []string
	constraints    []spannerDDLConstraint
	tableParents   map[string]string
}

func viewReferences(viewDef, targetView string) bool {
	if viewDef == "" || targetView == "" {
		return false
	}
	cleanTarget := cleanIdentifier(targetView)
	quotedTarget := "`" + cleanTarget + "`"
	escapedClean := regexp.QuoteMeta(cleanTarget)
	escapedQuoted := regexp.QuoteMeta(quotedTarget)

	pattern := `(?i)(?:^|[^a-zA-Z0-9_` + "`" + `])(?:` + escapedQuoted + `|` + escapedClean + `)(?:$|[^a-zA-Z0-9_` + "`" + `])`
	re, err := regexp.Compile(pattern)
	if err != nil {
		lowerDef := strings.ToLower(viewDef)
		return strings.Contains(lowerDef, strings.ToLower(quotedTarget)) || strings.Contains(lowerDef, strings.ToLower(cleanTarget))
	}
	return re.MatchString(viewDef)
}

func sortViewsDependenciesFirst(views []string, viewDefs map[string]string) []string {
	if len(views) <= 1 {
		return views
	}

	dependencies := make(map[string][]string)
	incomingDependents := make(map[string]int)

	for _, v := range views {
		def := ""
		if viewDefs != nil {
			def = viewDefs[v]
		}
		for _, other := range views {
			if v != other && viewReferences(def, other) {
				dependencies[v] = append(dependencies[v], other)
				incomingDependents[other]++
			}
		}
	}

	var queue []string
	for _, v := range views {
		if incomingDependents[v] == 0 {
			queue = append(queue, v)
		}
	}
	sort.Strings(queue)

	var result []string
	visited := make(map[string]bool)

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		if visited[curr] {
			continue
		}
		visited[curr] = true
		result = append(result, curr)

		for _, dep := range dependencies[curr] {
			incomingDependents[dep]--
			if incomingDependents[dep] <= 0 && !visited[dep] {
				queue = append(queue, dep)
			}
		}
		sort.Strings(queue)
	}

	for _, v := range views {
		if !visited[v] {
			result = append(result, v)
		}
	}

	return result
}

var (
	createModelRegex         = regexp.MustCompile(`(?i)^\s*CREATE(?:\s+OR\s+REPLACE)?\s+MODEL\s+(?:IF\s+NOT\s+EXISTS\s+)?([` + "`" + `]?[a-zA-Z0-9_]+[` + "`" + `]?)`)
	createChangeStreamRegex  = regexp.MustCompile(`(?i)^\s*CREATE(?:\s+OR\s+REPLACE)?\s+CHANGE\s+STREAM\s+([` + "`" + `]?[a-zA-Z0-9_]+[` + "`" + `]?)`)
	createSequenceRegex      = regexp.MustCompile(`(?i)^\s*CREATE(?:\s+OR\s+REPLACE)?\s+SEQUENCE\s+(?:IF\s+NOT\s+EXISTS\s+)?([` + "`" + `]?[a-zA-Z0-9_]+[` + "`" + `]?)`)
	createSearchIndexRegex   = regexp.MustCompile(`(?i)^\s*CREATE(?:\s+OR\s+REPLACE)?\s+SEARCH\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?([` + "`" + `]?[a-zA-Z0-9_]+[` + "`" + `]?)`)
	createVectorIndexRegex   = regexp.MustCompile(`(?i)^\s*CREATE(?:\s+OR\s+REPLACE)?\s+VECTOR\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?([` + "`" + `]?[a-zA-Z0-9_]+[` + "`" + `]?)`)
	createPropertyGraphRegex = regexp.MustCompile(`(?i)^\s*CREATE(?:\s+OR\s+REPLACE)?\s+PROPERTY\s+GRAPH\s+(?:IF\s+NOT\s+EXISTS\s+)?([` + "`" + `]?[a-zA-Z0-9_]+[` + "`" + `]?)`)
)

func cleanIdentifier(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "`")
	id = strings.TrimSuffix(id, "`")
	return id
}

func isInformationSchemaMissingError(err error) bool {
	if err == nil {
		return false
	}
	c := status.Code(err)
	if c == codes.PermissionDenied || c == codes.Unauthenticated {
		return false
	}
	if c == codes.NotFound || c == codes.Unimplemented {
		return true
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "not found: information_schema") ||
		strings.Contains(errStr, "unrecognized name: information_schema") ||
		strings.Contains(errStr, "table not found") ||
		strings.Contains(errStr, "unexpected trailing query contents")
}

func getTableDepth(table string, parents map[string]string, visited map[string]bool) int {
	if visited[table] {
		return 0
	}
	parent, hasParent := parents[table]
	if !hasParent || parent == "" {
		return 0
	}
	visited[table] = true
	return 1 + getTableDepth(parent, parents, visited)
}

func sortTablesChildrenFirst(tables []string, parents map[string]string) []string {
	type tableWithDepth struct {
		name  string
		depth int
	}

	withDepth := make([]tableWithDepth, len(tables))
	for i, t := range tables {
		visited := make(map[string]bool)
		d := getTableDepth(t, parents, visited)
		withDepth[i] = tableWithDepth{name: t, depth: d}
	}

	sort.SliceStable(withDepth, func(i, j int) bool {
		if withDepth[i].depth != withDepth[j].depth {
			return withDepth[i].depth > withDepth[j].depth // Higher depth (interleaved children) first
		}
		return withDepth[i].name < withDepth[j].name
	})

	result := make([]string, len(tables))
	for i, td := range withDepth {
		result[i] = td.name
	}
	return result
}

func (db *SpannerDatabase) inspectDDL(ctx context.Context) (*spannerDDLObjects, error) {
	if db.adminClient == nil {
		return nil, fmt.Errorf("adminClient is nil")
	}
	resp, err := db.adminClient.GetDatabaseDdl(ctx, &databasepb.GetDatabaseDdlRequest{
		Database: db.databasePath(),
	})
	if err != nil {
		return nil, err
	}
	return parseDDLStatements(resp.Statements), nil
}

func parseDDLStatements(statements []string) *spannerDDLObjects {
	objs := &spannerDDLObjects{
		tableParents: make(map[string]string),
		viewDefs:     make(map[string]string),
	}
	seenTables := make(map[string]bool)
	seenIndexes := make(map[string]bool)
	seenSearchIndexes := make(map[string]bool)
	seenVectorIndexes := make(map[string]bool)
	seenPropertyGraphs := make(map[string]bool)
	seenViews := make(map[string]bool)
	seenConstraints := make(map[string]bool)
	seenChangeStreams := make(map[string]bool)
	seenSequences := make(map[string]bool)
	seenModels := make(map[string]bool)

	for _, stmtStr := range statements {
		stmt, err := spansql.ParseDDLStmt(stmtStr)
		if err == nil {
			switch s := stmt.(type) {
			case *spansql.CreateChangeStream:
				csName := string(s.Name)
				if !seenChangeStreams[csName] {
					seenChangeStreams[csName] = true
					objs.changeStreams = append(objs.changeStreams, csName)
				}
			case *spansql.CreateSequence:
				seqName := string(s.Name)
				if !seenSequences[seqName] {
					seenSequences[seqName] = true
					objs.sequences = append(objs.sequences, seqName)
				}
			case *spansql.CreateTable:
				tName := string(s.Name)
				if !seenTables[tName] {
					seenTables[tName] = true
					objs.tables = append(objs.tables, tName)
				}
				if s.Interleave != nil && s.Interleave.Parent != "" {
					objs.tableParents[tName] = string(s.Interleave.Parent)
				}
				for _, c := range s.Constraints {
					if _, isFK := c.Constraint.(spansql.ForeignKey); isFK && c.Name != "" {
						cKey := tName + "." + string(c.Name)
						if !seenConstraints[cKey] {
							seenConstraints[cKey] = true
							objs.constraints = append(objs.constraints, spannerDDLConstraint{
								table:      tName,
								constraint: string(c.Name),
							})
						}
					}
				}
			case *spansql.CreateIndex:
				idxName := string(s.Name)
				if !seenIndexes[idxName] {
					seenIndexes[idxName] = true
					objs.indexes = append(objs.indexes, idxName)
				}
			case *spansql.CreateView:
				vName := string(s.Name)
				if !seenViews[vName] {
					seenViews[vName] = true
					objs.views = append(objs.views, vName)
					objs.viewDefs[vName] = stmtStr
				}
			case *spansql.AlterTable:
				tName := string(s.Name)
				if ac, ok := s.Alteration.(spansql.AddConstraint); ok {
					if _, isFK := ac.Constraint.Constraint.(spansql.ForeignKey); isFK && ac.Constraint.Name != "" {
						cKey := tName + "." + string(ac.Constraint.Name)
						if !seenConstraints[cKey] {
							seenConstraints[cKey] = true
							objs.constraints = append(objs.constraints, spannerDDLConstraint{
								table:      tName,
								constraint: string(ac.Constraint.Name),
							})
						}
					}
				}
			}
		}

		if m := createModelRegex.FindStringSubmatch(stmtStr); len(m) > 1 {
			mName := cleanIdentifier(m[1])
			if !seenModels[mName] {
				seenModels[mName] = true
				objs.models = append(objs.models, mName)
			}
		}
		if m := createChangeStreamRegex.FindStringSubmatch(stmtStr); len(m) > 1 {
			csName := cleanIdentifier(m[1])
			if !seenChangeStreams[csName] {
				seenChangeStreams[csName] = true
				objs.changeStreams = append(objs.changeStreams, csName)
			}
		}
		if m := createSequenceRegex.FindStringSubmatch(stmtStr); len(m) > 1 {
			seqName := cleanIdentifier(m[1])
			if !seenSequences[seqName] {
				seenSequences[seqName] = true
				objs.sequences = append(objs.sequences, seqName)
			}
		}
		if m := createSearchIndexRegex.FindStringSubmatch(stmtStr); len(m) > 1 {
			idxName := cleanIdentifier(m[1])
			if !seenSearchIndexes[idxName] {
				seenSearchIndexes[idxName] = true
				objs.searchIndexes = append(objs.searchIndexes, idxName)
			}
		}
		if m := createVectorIndexRegex.FindStringSubmatch(stmtStr); len(m) > 1 {
			idxName := cleanIdentifier(m[1])
			if !seenVectorIndexes[idxName] {
				seenVectorIndexes[idxName] = true
				objs.vectorIndexes = append(objs.vectorIndexes, idxName)
			}
		}
		if m := createPropertyGraphRegex.FindStringSubmatch(stmtStr); len(m) > 1 {
			pgName := cleanIdentifier(m[1])
			if !seenPropertyGraphs[pgName] {
				seenPropertyGraphs[pgName] = true
				objs.propertyGraphs = append(objs.propertyGraphs, pgName)
			}
		}
	}
	return objs
}

func (db *SpannerDatabase) dropStatementsFromDDLObjects(objs *spannerDDLObjects, excludeMap map[string]bool) ([]string, map[string]bool) {
	var ddlStatements []string
	seenStmts := make(map[string]bool)
	addStmt := func(s string) {
		if s != "" && !seenStmts[s] {
			seenStmts[s] = true
			ddlStatements = append(ddlStatements, s)
		}
	}
	preservedTables := make(map[string]bool)

	for _, csName := range objs.changeStreams {
		addStmt(fmt.Sprintf("DROP CHANGE STREAM %s", db.Quote(csName)))
	}
	for _, fk := range objs.constraints {
		addStmt(fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", db.Quote(fk.table), db.Quote(fk.constraint)))
	}
	for _, pgName := range objs.propertyGraphs {
		addStmt(fmt.Sprintf("DROP PROPERTY GRAPH %s", db.Quote(pgName)))
	}
	searchIdxSet := make(map[string]bool)
	for _, idxName := range objs.searchIndexes {
		searchIdxSet[idxName] = true
		addStmt(fmt.Sprintf("DROP SEARCH INDEX %s", db.Quote(idxName)))
	}
	vectorIdxSet := make(map[string]bool)
	for _, idxName := range objs.vectorIndexes {
		vectorIdxSet[idxName] = true
		addStmt(fmt.Sprintf("DROP VECTOR INDEX %s", db.Quote(idxName)))
	}
	for _, idxName := range objs.indexes {
		if !searchIdxSet[idxName] && !vectorIdxSet[idxName] {
			addStmt(fmt.Sprintf("DROP INDEX %s", db.Quote(idxName)))
		}
	}
	orderedViews := sortViewsDependenciesFirst(objs.views, objs.viewDefs)
	for _, vName := range orderedViews {
		addStmt(fmt.Sprintf("DROP VIEW %s", db.Quote(vName)))
	}
	for _, mName := range objs.models {
		addStmt(fmt.Sprintf("DROP MODEL %s", db.Quote(mName)))
	}
	orderedTables := sortTablesChildrenFirst(objs.tables, objs.tableParents)
	for _, tName := range orderedTables {
		if excludeMap != nil && excludeMap[tName] {
			preservedTables[tName] = true
			continue
		}
		addStmt(fmt.Sprintf("DROP TABLE %s", db.Quote(tName)))
	}
	for _, seqName := range objs.sequences {
		addStmt(fmt.Sprintf("DROP SEQUENCE %s", db.Quote(seqName)))
	}
	return ddlStatements, preservedTables
}

// SchemaEmpty checks if the database has no user tables (excluding schema history table).
func (db *SpannerDatabase) SchemaEmpty(ctx context.Context, schema string) (bool, error) {
	exists, err := db.SchemaExists(ctx, schema)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}

	historyTable := "flyway_schema_history"
	if db.config != nil && db.config.Table != "" {
		historyTable = db.config.Table
	}

	sql := `SELECT COUNT(1) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = '' AND TABLE_TYPE = 'BASE TABLE' AND TABLE_NAME != @table_name`
	stmt := spanner.Statement{
		SQL: sql,
		Params: map[string]interface{}{
			"table_name": historyTable,
		},
	}
	iter := db.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if err != nil {
		if isInformationSchemaMissingError(err) {
			if objs, ddlErr := db.inspectDDL(ctx); ddlErr == nil {
				for _, tName := range objs.tables {
					if tName != historyTable {
						return false, nil
					}
				}
				return true, nil
			}
		}
		return false, fmt.Errorf("failed to query tables: %w", err)
	}
	var count int64
	if err := row.Column(0, &count); err != nil {
		return false, err
	}
	return count == 0, nil
}

// DropSchema drops all tables and objects in the database.
func (db *SpannerDatabase) DropSchema(ctx context.Context, schema string) error {
	return db.CleanSchema(ctx, schema)
}

// CleanSchema drops all user tables, views, and indexes in the database.
// If excludeTables are provided, those tables are preserved and their migration records cleared.
func (db *SpannerDatabase) CleanSchema(ctx context.Context, schema string, excludeTables ...string) error {
	excludeMap := make(map[string]bool)
	for _, ext := range excludeTables {
		if ext != "" {
			excludeMap[ext] = true
		}
	}

	var ddlStatements []string
	seenStmts := make(map[string]bool)
	addStmt := func(s string) {
		if s != "" && !seenStmts[s] {
			seenStmts[s] = true
			ddlStatements = append(ddlStatements, s)
		}
	}

	var infoSchemaErr error

	// 1. Change streams (must be dropped before tables)
	csSQL := `SELECT CHANGE_STREAM_NAME FROM INFORMATION_SCHEMA.CHANGE_STREAMS WHERE CHANGE_STREAM_SCHEMA = ''`
	iter := db.client.Single().Query(ctx, spanner.NewStatement(csSQL))
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			infoSchemaErr = err
			break
		}
		var csName string
		if err := row.Column(0, &csName); err != nil {
			infoSchemaErr = err
			break
		}
		addStmt(fmt.Sprintf("DROP CHANGE STREAM %s", db.Quote(csName)))
	}
	iter.Stop()

	// 2. Foreign key constraints
	if infoSchemaErr == nil {
		fkSQL := `SELECT CONSTRAINT_NAME, TABLE_NAME 
		          FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS 
		          WHERE CONSTRAINT_TYPE = 'FOREIGN KEY' AND TABLE_SCHEMA = ''`
		iter = db.client.Single().Query(ctx, spanner.NewStatement(fkSQL))
		for {
			row, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				infoSchemaErr = err
				break
			}
			var cName, tName string
			if err := row.Columns(&cName, &tName); err != nil {
				infoSchemaErr = err
				break
			}
			addStmt(fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", db.Quote(tName), db.Quote(cName)))
		}
		iter.Stop()
	}

	// 3. Property Graphs (must be dropped before views and base tables)
	if infoSchemaErr == nil {
		pgSQL := `SELECT PROPERTY_GRAPH_NAME FROM INFORMATION_SCHEMA.PROPERTY_GRAPHS WHERE PROPERTY_GRAPH_SCHEMA = ''`
		iter = db.client.Single().Query(ctx, spanner.NewStatement(pgSQL))
		for {
			row, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				infoSchemaErr = err
				break
			}
			var pgName string
			if err := row.Column(0, &pgName); err != nil {
				infoSchemaErr = err
				break
			}
			addStmt(fmt.Sprintf("DROP PROPERTY GRAPH %s", db.Quote(pgName)))
		}
		iter.Stop()
	}

	// 4. Secondary indexes (including search and vector indexes)
	if infoSchemaErr == nil {
		idxSQL := `SELECT INDEX_NAME, TABLE_NAME, INDEX_TYPE 
		           FROM INFORMATION_SCHEMA.INDEXES 
		           WHERE INDEX_TYPE != 'PRIMARY_KEY' AND TABLE_SCHEMA = '' AND NOT IS_MANAGED`
		iter = db.client.Single().Query(ctx, spanner.NewStatement(idxSQL))
		for {
			row, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				infoSchemaErr = err
				break
			}
			var idxName, tName string
			var idxType spanner.NullString
			if err := row.Columns(&idxName, &tName, &idxType); err != nil {
				infoSchemaErr = err
				break
			}
			t := ""
			if idxType.Valid {
				t = strings.ToUpper(idxType.StringVal)
			}
			switch t {
			case "SEARCH":
				addStmt(fmt.Sprintf("DROP SEARCH INDEX %s", db.Quote(idxName)))
			case "VECTOR":
				addStmt(fmt.Sprintf("DROP VECTOR INDEX %s", db.Quote(idxName)))
			default:
				addStmt(fmt.Sprintf("DROP INDEX %s", db.Quote(idxName)))
			}
		}
		iter.Stop()
	}

	// 5. Views (dropped in dependency order: leaf views first)
	if infoSchemaErr == nil {
		viewsSQL := `SELECT TABLE_NAME, VIEW_DEFINITION FROM INFORMATION_SCHEMA.VIEWS WHERE TABLE_SCHEMA = ''`
		iter = db.client.Single().Query(ctx, spanner.NewStatement(viewsSQL))
		var viewsToDrop []string
		viewDefs := make(map[string]string)
		for {
			row, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				infoSchemaErr = err
				break
			}
			var vName, vDef string
			if err := row.Columns(&vName, &vDef); err != nil {
				infoSchemaErr = err
				break
			}
			viewsToDrop = append(viewsToDrop, vName)
			viewDefs[vName] = vDef
		}
		iter.Stop()

		if infoSchemaErr == nil {
			orderedViews := sortViewsDependenciesFirst(viewsToDrop, viewDefs)
			for _, vName := range orderedViews {
				addStmt(fmt.Sprintf("DROP VIEW %s", db.Quote(vName)))
			}
		}
	}

	// 6. Machine learning models
	if infoSchemaErr == nil {
		modelSQL := `SELECT MODEL_NAME FROM INFORMATION_SCHEMA.MODELS WHERE MODEL_SCHEMA = ''`
		iter = db.client.Single().Query(ctx, spanner.NewStatement(modelSQL))
		for {
			row, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				infoSchemaErr = err
				break
			}
			var mName string
			if err := row.Column(0, &mName); err != nil {
				infoSchemaErr = err
				break
			}
			addStmt(fmt.Sprintf("DROP MODEL %s", db.Quote(mName)))
		}
		iter.Stop()
	}

	preservedTables := make(map[string]bool)
	// 6. Base tables
	if infoSchemaErr == nil {
		tableParents := make(map[string]string)
		var tablesToDrop []string
		tblSQL := `SELECT TABLE_NAME, PARENT_TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_TYPE = 'BASE TABLE' AND TABLE_SCHEMA = ''`
		iter = db.client.Single().Query(ctx, spanner.NewStatement(tblSQL))
		for {
			row, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				infoSchemaErr = err
				break
			}
			var tName string
			var parentTable spanner.NullString
			if err := row.Columns(&tName, &parentTable); err != nil {
				infoSchemaErr = err
				break
			}
			if excludeMap[tName] {
				preservedTables[tName] = true
				continue
			}
			if parentTable.Valid && parentTable.StringVal != "" {
				tableParents[tName] = parentTable.StringVal
			}
			tablesToDrop = append(tablesToDrop, tName)
		}
		iter.Stop()

		if infoSchemaErr == nil {
			// Sort interleaved children before parents so DROP TABLE does not fail
			tablesToDrop = sortTablesChildrenFirst(tablesToDrop, tableParents)
			for _, tName := range tablesToDrop {
				addStmt(fmt.Sprintf("DROP TABLE %s", db.Quote(tName)))
			}
		}
	}

	// 7. Sequences (must be dropped after tables that reference them in default values)
	if infoSchemaErr == nil {
		seqSQL := `SELECT NAME FROM INFORMATION_SCHEMA.SEQUENCES WHERE SCHEMA = ''`
		iter = db.client.Single().Query(ctx, spanner.NewStatement(seqSQL))
		for {
			row, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				infoSchemaErr = err
				break
			}
			var seqName string
			if err := row.Column(0, &seqName); err != nil {
				infoSchemaErr = err
				break
			}
			addStmt(fmt.Sprintf("DROP SEQUENCE %s", db.Quote(seqName)))
		}
		iter.Stop()
	}

	// Fallback to GetDatabaseDdl if INFORMATION_SCHEMA is unavailable (e.g., spannertest emulator)
	if infoSchemaErr != nil {
		if isInformationSchemaMissingError(infoSchemaErr) {
			// Reset any partial statements and preserved tables collected prior to error to prevent duplicate drops
			ddlStatements = nil
			seenStmts = make(map[string]bool)
			preservedTables = make(map[string]bool)

			if objs, ddlErr := db.inspectDDL(ctx); ddlErr == nil {
				ddlStatements, preservedTables = db.dropStatementsFromDDLObjects(objs, excludeMap)
			} else {
				return fmt.Errorf("failed to inspect Spanner DDL for clean: %w", ddlErr)
			}
		} else {
			return fmt.Errorf("failed to query Spanner schema for clean: %w", infoSchemaErr)
		}
	}

	if len(ddlStatements) > 0 {
		op, err := db.adminClient.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{
			Database:   db.databasePath(),
			Statements: ddlStatements,
		})
		if err != nil {
			return fmt.Errorf("failed to submit schema clean DDL: %w", err)
		}
		if err := op.Wait(ctx); err != nil {
			return fmt.Errorf("failed executing schema clean DDL: %w", err)
		}
	}

	for pt := range preservedTables {
		delSQL := fmt.Sprintf("DELETE FROM %s WHERE `installed_rank` >= 0 OR `description` != 'flyway-lock'", db.Quote(pt))
		if err := db.ExecuteStatement(ctx, delSQL); err != nil {
			return fmt.Errorf("failed to clean preserved table %s: %w", pt, err)
		}
	}

	return nil
}

// HistoryTableDDL returns the DDL statement used to create the schema history table in Spanner.
func (db *SpannerDatabase) HistoryTableDDL(schema, table string) string {
	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	return fmt.Sprintf(`CREATE TABLE %s (
		`+"`installed_rank`"+` INT64 NOT NULL,
		`+"`version`"+` STRING(50),
		`+"`description`"+` STRING(200) NOT NULL,
		`+"`type`"+` STRING(20) NOT NULL,
		`+"`script`"+` STRING(1000) NOT NULL,
		`+"`checksum`"+` INT64,
		`+"`installed_by`"+` STRING(100) NOT NULL,
		`+"`installed_on`"+` TIMESTAMP NOT NULL OPTIONS (allow_commit_timestamp=true),
		`+"`execution_time`"+` INT64 NOT NULL,
		`+"`success`"+` BOOL NOT NULL
	) PRIMARY KEY (`+"`installed_rank`"+`);`, db.Quote(tableName))
}

// EnsureHistoryTable creates the flyway_schema_history table if it doesn't exist.
func (db *SpannerDatabase) EnsureHistoryTable(ctx context.Context, schema, table string) error {
	exists, err := db.HistoryTableExists(ctx, schema, table)
	if err == nil && exists {
		return nil
	}

	ddl := db.HistoryTableDDL(schema, table)
	ddl = strings.TrimSuffix(ddl, ";")

	if execErr := db.ExecuteStatement(ctx, ddl); execErr != nil {
		if exists, checkErr := db.HistoryTableExists(ctx, schema, table); checkErr == nil && exists {
			return nil
		}
		return execErr
	}
	return nil
}

// HistoryTableExists checks if the history table exists in Spanner.
func (db *SpannerDatabase) HistoryTableExists(ctx context.Context, schema, table string) (bool, error) {
	if db.testHookHistoryTableExistsFalseOnce {
		db.testHookHistoryTableExistsFalseOnce = false
		return false, nil
	}

	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	sql := "SELECT COUNT(1) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_NAME = @tableName AND TABLE_SCHEMA = ''"
	stmt := spanner.Statement{
		SQL: sql,
		Params: map[string]interface{}{
			"tableName": tableName,
		},
	}

	iter := db.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if err != nil {
		if isInformationSchemaMissingError(err) {
			if objs, ddlErr := db.inspectDDL(ctx); ddlErr == nil {
				for _, tName := range objs.tables {
					if tName == tableName {
						return true, nil
					}
				}
				return false, nil
			}
		}
		return false, err
	}
	var count int64
	if err := row.Column(0, &count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// FetchHistory retrieves all history records ordered by installed_rank ASC.
func (db *SpannerDatabase) FetchHistory(ctx context.Context, schema, table string) ([]resolver.AppliedMigration, error) {
	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	sql := fmt.Sprintf(`SELECT 
		`+"`installed_rank`"+`,
		`+"`version`"+`,
		`+"`description`"+`,
		`+"`type`"+`,
		`+"`script`"+`,
		`+"`checksum`"+`,
		`+"`installed_by`"+`,
		`+"`installed_on`"+`,
		`+"`execution_time`"+`,
		`+"`success`"+`
	FROM %s
	ORDER BY `+"`installed_rank`"+` ASC`, db.Quote(tableName))

	iter := db.client.Single().Query(ctx, spanner.NewStatement(sql))
	defer iter.Stop()

	var records []resolver.AppliedMigration
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read schema history row: %w", err)
		}

		var (
			rank        int64
			verNull     spanner.NullString
			description string
			recType     string
			script      string
			csNull      spanner.NullInt64
			installedBy string
			installedOn time.Time
			execTime    int64
			success     bool
		)

		if err := row.Columns(&rank, &verNull, &description, &recType, &script, &csNull, &installedBy, &installedOn, &execTime, &success); err != nil {
			return nil, fmt.Errorf("failed to scan history row: %w", err)
		}

		// Skip internal lock rows (installed_rank = -1)
		if rank < 0 {
			continue
		}

		var ver *version.Version
		if verNull.Valid && verNull.StringVal != "" {
			v, err := version.Parse(verNull.StringVal)
			if err == nil {
				ver = &v
			}
		}

		var cs *int64
		if csNull.Valid {
			c := csNull.Int64
			cs = &c
		}

		records = append(records, resolver.AppliedMigration{
			InstalledRank: int(rank),
			Version:       ver,
			Description:   description,
			Type:          recType,
			Script:        script,
			Checksum:      cs,
			InstalledBy:   installedBy,
			InstalledOn:   installedOn,
			ExecutionTime: execTime,
			Success:       success,
		})
	}

	return records, nil
}

func (db *SpannerDatabase) setActiveLockErr(err error) {
	db.activeLockMu.Lock()
	defer db.activeLockMu.Unlock()
	db.activeLockErr = err
}

func (db *SpannerDatabase) checkActiveLockErr() error {
	db.activeLockMu.Lock()
	defer db.activeLockMu.Unlock()
	if db.activeLockErr != nil {
		return fmt.Errorf("spanner lock heartbeat error: %w", db.activeLockErr)
	}
	return nil
}

func extractLockOwner(verNull spanner.NullString, instBy string) string {
	if verNull.Valid && verNull.StringVal != "" {
		return verNull.StringVal
	}
	return instBy
}

// AbbreviateDescription truncates description to at most 200 runes (first 197 + "...").
func AbbreviateDescription(desc string) string {
	return database.AbbreviateDescription(desc)
}

// AbbreviateScript truncates script to at most 1000 runes ("..." + runes 3 to 1000).
func AbbreviateScript(script string) string {
	return database.AbbreviateScript(script)
}

// InsertHistory inserts a new applied migration record into the history table.
func (db *SpannerDatabase) InsertHistory(ctx context.Context, schema, table string, rec database.HistoryRecord) error {
	if err := db.checkActiveLockErr(); err != nil {
		return err
	}

	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	var verVal spanner.NullString
	if rec.Version != nil && *rec.Version != "" {
		verVal = spanner.NullString{StringVal: *rec.Version, Valid: true}
	}

	var csVal spanner.NullInt64
	if rec.Checksum != nil {
		csVal = spanner.NullInt64{Int64: *rec.Checksum, Valid: true}
	}

	desc := AbbreviateDescription(rec.Description)
	script := AbbreviateScript(rec.Script)

	m := spanner.Insert(tableName,
		[]string{"installed_rank", "version", "description", "type", "script", "checksum", "installed_by", "installed_on", "execution_time", "success"},
		[]interface{}{
			int64(rec.InstalledRank),
			verVal,
			desc,
			rec.Type,
			script,
			csVal,
			rec.InstalledBy,
			spanner.CommitTimestamp,
			rec.ExecutionTime,
			rec.Success,
		},
	)

	_, err := db.client.Apply(ctx, []*spanner.Mutation{m})
	if err != nil {
		return fmt.Errorf("failed to insert history record: %w", err)
	}
	return nil
}

// UpdateHistory updates an existing migration record.
func (db *SpannerDatabase) UpdateHistory(ctx context.Context, schema, table string, rec database.HistoryRecord) error {
	if err := db.checkActiveLockErr(); err != nil {
		return err
	}

	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	var csVal spanner.NullInt64
	if rec.Checksum != nil {
		csVal = spanner.NullInt64{Int64: *rec.Checksum, Valid: true}
	}

	desc := AbbreviateDescription(rec.Description)
	script := AbbreviateScript(rec.Script)

	m := spanner.Update(tableName,
		[]string{"installed_rank", "description", "type", "script", "checksum"},
		[]interface{}{
			int64(rec.InstalledRank),
			desc,
			rec.Type,
			script,
			csVal,
		},
	)

	_, err := db.client.Apply(ctx, []*spanner.Mutation{m})
	if err != nil {
		return fmt.Errorf("failed to update history record: %w", err)
	}
	return nil
}

// DeleteHistory removes a history record.
func (db *SpannerDatabase) DeleteHistory(ctx context.Context, schema, table string, installedRank int) error {
	if err := db.checkActiveLockErr(); err != nil {
		return err
	}

	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	m := spanner.Delete(tableName, spanner.Key{int64(installedRank)}.AsPrefix())
	_, err := db.client.Apply(ctx, []*spanner.Mutation{m})
	if err != nil {
		return fmt.Errorf("failed to delete history record: %w", err)
	}
	return nil
}

// Lock acquires a Flyway-compatible lock row in the history table.
func (db *SpannerDatabase) Lock(ctx context.Context, schema, table string) (database.UnlockFunc, error) {
	tableName := table
	if tableName == "" {
		tableName = "flyway_schema_history"
	}

	lockID := uuid.New().String()

	ttl := db.lockTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	retryCount := 50
	if db.config != nil && db.config.LockRetryCount > 0 {
		retryCount = db.config.LockRetryCount
	}

	supportsCurTS := db.checkSupportsCurrentTS(ctx)

	var lastErr error
	for attempt := 0; attempt < retryCount; attempt++ {
		acquired := false
		_, err := db.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
			var querySQL string
			if supportsCurTS {
				querySQL = fmt.Sprintf("SELECT installed_rank, version, installed_by, installed_on, CURRENT_TIMESTAMP() AS current_ts FROM %s WHERE installed_rank IN (-100, -1) AND description = 'flyway-lock'", db.Quote(tableName))
			} else {
				querySQL = fmt.Sprintf("SELECT installed_rank, version, installed_by, installed_on FROM %s WHERE installed_rank IN (-100, -1) AND description = 'flyway-lock'", db.Quote(tableName))
			}
			stmt := spanner.Statement{SQL: querySQL}
			iter := txn.Query(ctx, stmt)
			defer iter.Stop()

			var expiredKeys []spanner.Key
			for {
				row, err := iter.Next()
				if err == iterator.Done {
					break
				}
				if err != nil {
					return err
				}
				var rank int64
				var verNull spanner.NullString
				var instBy string
				var instOn time.Time
				var curTS time.Time
				if supportsCurTS {
					if scanErr := row.Columns(&rank, &verNull, &instBy, &instOn, &curTS); scanErr != nil {
						return scanErr
					}
				} else {
					if scanErr := row.Columns(&rank, &verNull, &instBy, &instOn); scanErr != nil {
						return scanErr
					}
				}
				now := curTS
				if now.IsZero() {
					now = time.Now()
				}
				if now.Sub(instOn) < ttl {
					return fmt.Errorf("lock already held by %s", extractLockOwner(verNull, instBy))
				}
				expiredKeys = append(expiredKeys, spanner.Key{rank})
			}

			// Delete any expired lock rows (at -100 or transitional -1)
			for _, k := range expiredKeys {
				if delErr := txn.BufferWrite([]*spanner.Mutation{spanner.Delete(tableName, k)}); delErr != nil {
					return delErr
				}
			}

			// Insert Flyway-compatible lock row at installed_rank = -100 using spanner.Insert
			m := spanner.Insert(tableName,
				[]string{"installed_rank", "version", "description", "type", "script", "checksum", "installed_by", "installed_on", "execution_time", "success"},
				[]interface{}{
					int64(-100),
					spanner.NullString{StringVal: lockID, Valid: true},
					"flyway-lock",
					"",
					"",
					spanner.NullInt64{},
					lockID,
					spanner.CommitTimestamp,
					int64(0),
					true,
				},
			)
			if bufferErr := txn.BufferWrite([]*spanner.Mutation{m}); bufferErr != nil {
				return bufferErr
			}
			acquired = true
			return nil
		})

		if err == nil && acquired {
			db.setActiveLockErr(nil)

			interval := db.heartbeatInterval
			if interval <= 0 {
				interval = 3 * time.Minute
			}

			var lockErrMu sync.Mutex
			var heartbeatErr error
			setHeartbeatErr := func(err error) {
				lockErrMu.Lock()
				if heartbeatErr == nil {
					heartbeatErr = err
				}
				lockErrMu.Unlock()
				db.setActiveLockErr(err)
			}
			getHeartbeatErr := func() error {
				lockErrMu.Lock()
				defer lockErrMu.Unlock()
				return heartbeatErr
			}

			// Start background lock heartbeat to update installed_on every interval
			heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				ticker := time.NewTicker(interval)
				defer ticker.Stop()

				lastHeartbeatSuccess := time.Now()
				safetyMargin := 2 * time.Minute
				if ttl <= 2*time.Minute {
					safetyMargin = ttl / 4
				}
				deadline := ttl - safetyMargin

				attemptHeartbeat := func() error {
					if db.heartbeatAttemptInterceptor != nil {
						if interceptErr := db.heartbeatAttemptInterceptor(); interceptErr != nil {
							return interceptErr
						}
					}
					tickTimeout := 30 * time.Second
					if interval < tickTimeout {
						tickTimeout = interval / 2
						if tickTimeout < 500*time.Millisecond {
							tickTimeout = 500 * time.Millisecond
						}
					}
					tickCtx, cancelTick := context.WithTimeout(heartbeatCtx, tickTimeout)
					defer cancelTick()

					_, hErr := db.client.ReadWriteTransaction(tickCtx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
						stmt := spanner.Statement{
							SQL: fmt.Sprintf("SELECT version, installed_by FROM %s WHERE installed_rank = -100 AND description = 'flyway-lock'", db.Quote(tableName)),
						}
						iter := txn.Query(ctx, stmt)
						defer iter.Stop()

						row, err := iter.Next()
						if err == iterator.Done {
							return ErrLockRowMissing
						}
						if err != nil {
							return err
						}

						var verNull spanner.NullString
						var instBy string
						if err := row.Columns(&verNull, &instBy); err != nil {
							return err
						}
						owner := extractLockOwner(verNull, instBy)
						if owner != lockID {
							return fmt.Errorf("%w: owned by %q, expected %q", ErrLockOwnershipLost, owner, lockID)
						}

						m := spanner.Update(tableName,
							[]string{"installed_rank", "installed_on"},
							[]interface{}{int64(-100), spanner.CommitTimestamp},
						)
						return txn.BufferWrite([]*spanner.Mutation{m})
					})
					return hErr
				}

				for {
					select {
					case <-heartbeatCtx.Done():
						return
					case <-ticker.C:
						hErr := attemptHeartbeat()
						if heartbeatCtx.Err() != nil {
							return
						}
						if hErr == nil {
							lastHeartbeatSuccess = time.Now()
							if db.onHeartbeat != nil {
								db.onHeartbeat(nil)
							}
							continue
						}

						// Check if the lock was lost or missing (non-transient fatal error)
						if errors.Is(hErr, ErrLockOwnershipLost) || errors.Is(hErr, ErrLockRowMissing) {
							setHeartbeatErr(hErr)
							if db.onHeartbeat != nil {
								db.onHeartbeat(hErr)
							}
							return
						}

						// Transient error (e.g. Unavailable, network blip): retry until TTL minus safety margin
						retryBackoff := 1 * time.Second
						if interval < 5*time.Second {
							retryBackoff = 20 * time.Millisecond
						}
						success := false
						for time.Since(lastHeartbeatSuccess) < deadline {
							select {
							case <-heartbeatCtx.Done():
								return
							case <-time.After(retryBackoff):
							}

							hErr = attemptHeartbeat()
							if heartbeatCtx.Err() != nil {
								return
							}
							if hErr == nil {
								lastHeartbeatSuccess = time.Now()
								if db.onHeartbeat != nil {
									db.onHeartbeat(nil)
								}
								success = true
								break
							}
							if errors.Is(hErr, ErrLockOwnershipLost) || errors.Is(hErr, ErrLockRowMissing) {
								setHeartbeatErr(hErr)
								if db.onHeartbeat != nil {
									db.onHeartbeat(hErr)
								}
								return
							}
							retryBackoff = min(retryBackoff*2, 10*time.Second)
						}

						if !success {
							finalErr := fmt.Errorf("lock heartbeat failed to refresh within TTL safety margin (last refresh %v ago, TTL %v): %w", time.Since(lastHeartbeatSuccess), ttl, hErr)
							setHeartbeatErr(finalErr)
							if db.onHeartbeat != nil {
								db.onHeartbeat(finalErr)
							}
							return
						}
					}
				}
			}()

			var unlockOnce sync.Once
			var unlockErr error
			unlock := func(unlockCtx context.Context) error {
				unlockOnce.Do(func() {
					cancelHeartbeat()
					wg.Wait()

					hbErr := getHeartbeatErr()
					db.setActiveLockErr(nil)

					_, delErr := db.client.ReadWriteTransaction(unlockCtx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
						stmt := spanner.Statement{
							SQL: fmt.Sprintf("SELECT version, installed_by FROM %s WHERE installed_rank = -100 AND description = 'flyway-lock'", db.Quote(tableName)),
						}
						iter := txn.Query(ctx, stmt)
						defer iter.Stop()

						row, err := iter.Next()
						if err == iterator.Done {
							return fmt.Errorf("lock row missing on unlock")
						}
						if err != nil {
							return err
						}

						var verNull spanner.NullString
						var instBy string
						if err := row.Columns(&verNull, &instBy); err != nil {
							return err
						}
						owner := extractLockOwner(verNull, instBy)
						if owner != lockID {
							return fmt.Errorf("cannot unlock: lock owned by %q, expected %q", owner, lockID)
						}

						m := spanner.Delete(tableName, spanner.Key{int64(-100)})
						return txn.BufferWrite([]*spanner.Mutation{m})
					})

					if hbErr != nil {
						unlockErr = fmt.Errorf("lock heartbeat failed: %w", hbErr)
					} else if delErr != nil {
						if status.Code(delErr) != codes.NotFound {
							unlockErr = fmt.Errorf("failed to release Spanner lock: %w", delErr)
						}
					}
				})
				return unlockErr
			}
			return unlock, nil
		}

		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}

	return nil, fmt.Errorf("failed to acquire Spanner lock after %d attempts: %w", retryCount, lastErr)
}

// ExecuteStatement executes a SQL statement, routing DDL to UpdateDatabaseDdl and DML/Queries to the client.
func (db *SpannerDatabase) ExecuteStatement(ctx context.Context, sql string) error {
	if err := db.checkActiveLockErr(); err != nil {
		return err
	}

	trimmed := strings.TrimSpace(sql)
	if trimmed == "" || trimmed == ";" {
		return nil
	}

	// Trim trailing semicolons for Spanner DDL/DML
	trimmed = strings.TrimRight(trimmed, ";")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return nil
	}

	if isDDL(trimmed) {
		op, err := db.adminClient.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{
			Database:   db.databasePath(),
			Statements: []string{trimmed},
		})
		if err != nil {
			return fmt.Errorf("failed to submit Spanner DDL statement: %w", err)
		}
		if err := op.Wait(ctx); err != nil {
			return fmt.Errorf("Spanner DDL execution failed: %w", err)
		}
		return nil
	}

	// Query or DML
	upper := strings.ToUpper(stripLeadingComments(trimmed))
	if strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "WITH") || strings.HasPrefix(upper, "SHOW") {
		iter := db.client.Single().Query(ctx, spanner.NewStatement(trimmed))
		defer iter.Stop()
		for {
			_, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return fmt.Errorf("query execution failed: %w", err)
			}
		}
		return nil
	}

	// DML in ReadWriteTransaction
	_, err := db.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		_, execErr := txn.Update(ctx, spanner.NewStatement(trimmed))
		return execErr
	})
	if err != nil {
		return fmt.Errorf("Spanner DML execution failed: %w", err)
	}
	return nil
}

func isDDL(sql string) bool {
	clean := stripLeadingComments(sql)
	fields := strings.Fields(clean)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToUpper(fields[0]) {
	case "CREATE", "ALTER", "DROP", "GRANT", "REVOKE", "RENAME", "ANALYZE":
		return true
	}
	return false
}

func stripLeadingComments(sql string) string {
	s := strings.TrimSpace(sql)
	for {
		if strings.HasPrefix(s, "--") {
			if idx := strings.Index(s, "\n"); idx != -1 {
				s = strings.TrimSpace(s[idx+1:])
			} else {
				return ""
			}
		} else if strings.HasPrefix(s, "#") {
			if idx := strings.Index(s, "\n"); idx != -1 {
				s = strings.TrimSpace(s[idx+1:])
			} else {
				return ""
			}
		} else if strings.HasPrefix(s, "/*") {
			if idx := strings.Index(s, "*/"); idx != -1 {
				s = strings.TrimSpace(s[idx+2:])
			} else {
				return ""
			}
		} else {
			break
		}
	}
	return s
}
