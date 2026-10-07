package mock

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/resolver"
	"github.com/roryq/noway/pkg/version"
)

// MockDatabase is an in-memory implementation of Database for unit testing.
type MockDatabase struct {
	mu            sync.RWMutex
	schemas       map[string]bool
	tables        map[string]map[string]bool // schema -> table -> exists
	history       map[string][]resolver.AppliedMigration
	currentUser   string
	executedSQL   []string
	isLocked      bool
	failOnExecute string
}

// NewMockDatabase creates a new MockDatabase.
func NewMockDatabase() *MockDatabase {
	return &MockDatabase{
		schemas:     make(map[string]bool),
		tables:      make(map[string]map[string]bool),
		history:     make(map[string][]resolver.AppliedMigration),
		currentUser: "mock-user@google.com",
		executedSQL: make([]string, 0),
	}
}

func (m *MockDatabase) Close() error {
	return nil
}

func (m *MockDatabase) SetFailOnExecute(substr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failOnExecute = substr
}

func (m *MockDatabase) ExecutedStatements() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	copied := make([]string, len(m.executedSQL))
	copy(copied, m.executedSQL)
	return copied
}

func (m *MockDatabase) Quote(identifiers ...string) string {
	quoted := make([]string, len(identifiers))
	for i, id := range identifiers {
		escaped := strings.ReplaceAll(id, "`", "\\`")
		quoted[i] = "`" + escaped + "`"
	}
	return strings.Join(quoted, ".")
}

func (m *MockDatabase) GetCurrentUser(ctx context.Context) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentUser, nil
}

func (m *MockDatabase) EnsureSchema(ctx context.Context, schema string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schemas[schema] = true
	if _, ok := m.tables[schema]; !ok {
		m.tables[schema] = make(map[string]bool)
	}
	return nil
}

func (m *MockDatabase) AddTable(schema, table string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schemas[schema] = true
	if _, ok := m.tables[schema]; !ok {
		m.tables[schema] = make(map[string]bool)
	}
	m.tables[schema][table] = true
}

func (m *MockDatabase) SchemaExists(ctx context.Context, schema string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.schemas[schema], nil
}

func (m *MockDatabase) SchemaEmpty(ctx context.Context, schema string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.schemas[schema] {
		return true, nil
	}
	tables := m.tables[schema]
	count := 0
	for t := range tables {
		if t != "flyway_schema_history" {
			count++
		}
	}
	return count == 0, nil
}

func (m *MockDatabase) DropSchema(ctx context.Context, schema string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.schemas, schema)
	delete(m.tables, schema)
	delete(m.history, schema)
	return nil
}

func (m *MockDatabase) CleanSchema(ctx context.Context, schema string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tables[schema] = make(map[string]bool)
	delete(m.history, schema)
	return nil
}

func (m *MockDatabase) EnsureHistoryTable(ctx context.Context, schema, table string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schemas[schema] = true
	if _, ok := m.tables[schema]; !ok {
		m.tables[schema] = make(map[string]bool)
	}
	m.tables[schema][table] = true
	return nil
}

func (m *MockDatabase) HistoryTableExists(ctx context.Context, schema, table string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.schemas[schema] {
		return false, nil
	}
	return m.tables[schema][table], nil
}

func (m *MockDatabase) FetchHistory(ctx context.Context, schema, table string) ([]resolver.AppliedMigration, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := schema + "." + table
	records := m.history[key]
	copied := make([]resolver.AppliedMigration, len(records))
	copy(copied, records)
	return copied, nil
}

func (m *MockDatabase) InsertHistory(ctx context.Context, schema, table string, record database.HistoryRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var vPtr *version.Version
	if record.Version != nil {
		v, err := version.Parse(*record.Version)
		if err == nil {
			vPtr = &v
		}
	}

	app := resolver.AppliedMigration{
		InstalledRank: record.InstalledRank,
		Version:       vPtr,
		Description:   record.Description,
		Type:          record.Type,
		Script:        record.Script,
		Checksum:      record.Checksum,
		InstalledBy:   record.InstalledBy,
		InstalledOn:   time.Now(),
		ExecutionTime: record.ExecutionTime,
		Success:       record.Success,
	}

	key := schema + "." + table
	m.history[key] = append(m.history[key], app)
	return nil
}

func (m *MockDatabase) UpdateHistory(ctx context.Context, schema, table string, record database.HistoryRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := schema + "." + table
	for i, r := range m.history[key] {
		if r.InstalledRank == record.InstalledRank {
			m.history[key][i].Description = record.Description
			m.history[key][i].Type = record.Type
			m.history[key][i].Checksum = record.Checksum
			return nil
		}
	}
	return nil
}

func (m *MockDatabase) DeleteHistory(ctx context.Context, schema, table string, installedRank int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := schema + "." + table
	var updated []resolver.AppliedMigration
	for _, r := range m.history[key] {
		if r.InstalledRank != installedRank {
			updated = append(updated, r)
		}
	}
	m.history[key] = updated
	return nil
}

func (m *MockDatabase) Lock(ctx context.Context, schema, table string) (database.UnlockFunc, error) {
	m.mu.Lock()
	if m.isLocked {
		m.mu.Unlock()
		return nil, fmt.Errorf("already locked")
	}
	m.isLocked = true
	m.mu.Unlock()

	return func(ctx context.Context) error {
		m.mu.Lock()
		m.isLocked = false
		m.mu.Unlock()
		return nil
	}, nil
}

func (m *MockDatabase) ExecuteStatement(ctx context.Context, sql string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.failOnExecute != "" && strings.Contains(sql, m.failOnExecute) {
		return fmt.Errorf("mock error executing SQL containing %q", m.failOnExecute)
	}

	m.executedSQL = append(m.executedSQL, sql)
	return nil
}
