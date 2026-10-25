package resolver

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/roryq/noway/pkg/checksum"
	"github.com/roryq/noway/pkg/parser"
	"github.com/roryq/noway/pkg/version"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	textunicode "golang.org/x/text/encoding/unicode"
)

// ResolverConfig configures how migrations are discovered and parsed.
type ResolverConfig struct {
	Locations          []string // e.g. "filesystem:sql", "migrations"
	FS                 fs.FS    // Optional embedded filesystem
	Prefix             string   // default "V"
	RepeatablePrefix   string   // default "R"
	UndoPrefix         string   // default "U"
	BaselinePrefix     string   // default "B"
	Separator               string   // default "__"
	Suffixes                []string // default [".sql", ".sh", ...]
	SQLMigrationSuffixes    []string // default [".sql"]
	Encoding                string   // default "UTF-8"
	FailOnMissingLocations  bool
	ValidateMigrationNaming bool
	PlaceholderReplacement  *bool
	Replacer                *parser.PlaceholderReplacer
	Builtins                parser.BuiltinPlaceholders
}

// DefaultResolverConfig returns standard defaults.
func DefaultResolverConfig() ResolverConfig {
	return ResolverConfig{
		Locations:            []string{"filesystem:sql"},
		Prefix:               "V",
		RepeatablePrefix:     "R",
		UndoPrefix:           "U",
		BaselinePrefix:       "B",
		Separator:            "__",
		Suffixes:             []string{".sql", ".sh", ".bash", ".cmd", ".ps1", ".bat", ".py"},
		SQLMigrationSuffixes: []string{".sql"},
		Encoding:             "UTF-8",
	}
}

func (r *Resolver) isSQLMigrationFile(filename string) bool {
	suffixes := r.config.SQLMigrationSuffixes
	if len(suffixes) == 0 {
		suffixes = []string{".sql"}
	}
	for _, suffix := range suffixes {
		if hasSuffixFold(filename, suffix) {
			return true
		}
	}
	return false
}

// Resolver scans and parses migration files.
type Resolver struct {
	config ResolverConfig
}

// NewResolver creates a new migration Resolver.
func NewResolver(cfg ResolverConfig) *Resolver {
	if cfg.Prefix == "" {
		cfg.Prefix = "V"
	}
	if cfg.RepeatablePrefix == "" {
		cfg.RepeatablePrefix = "R"
	}
	if cfg.UndoPrefix == "" {
		cfg.UndoPrefix = "U"
	}
	if cfg.BaselinePrefix == "" {
		cfg.BaselinePrefix = "B"
	}
	if cfg.Separator == "" {
		cfg.Separator = "__"
	}
	if len(cfg.Suffixes) == 0 {
		cfg.Suffixes = []string{".sql", ".sh", ".bash", ".cmd", ".ps1", ".bat", ".py"}
	}
	if len(cfg.Locations) == 0 && cfg.FS == nil {
		cfg.Locations = []string{"filesystem:sql"}
	}
	return &Resolver{config: cfg}
}

// SetBuiltins updates the builtin placeholders.
func (r *Resolver) SetBuiltins(builtins parser.BuiltinPlaceholders) {
	r.config.Builtins = builtins
}


// ResolveResult contains discovered migrations and callbacks.
type ResolveResult struct {
	VersionedMigrations  []ResolvedMigration
	RepeatableMigrations []ResolvedMigration
	UndoMigrations       []ResolvedMigration
	BaselineMigrations   []ResolvedMigration
	Callbacks            map[string][]ResolvedCallback
}

// ResolvedCallback represents a discovered callback script.
type ResolvedCallback struct {
	Event            string          // e.g. "beforeMigrate", "afterMigrate"
	Description      string          // e.g. "first step", or "" for plain callbacks
	Filename         string          // e.g. "beforeMigrate__first_step.sql"
	Content          string          // SQL or script contents
	PhysicalLocation string          // Full filesystem or FS path
	Type             MigrationType   // TypeSQL or TypeScript
	IsScript         bool            // true for .sh, .bash, .cmd, .ps1, .bat, .py
	Config           MigrationConfig // Configuration from .conf file if present
}

// Standard Flyway callback event names
var StandardCallbackEvents = []string{
	"beforeMigrate",
	"afterMigrate",
	"beforeEachMigrate",
	"afterEachMigrate",
	"beforeEachMigrateError",
	"afterEachMigrateError",
	"afterMigrateError",
	"afterVersioned",
	"beforeRepeatables",
	"afterMigrateApplied",
	"beforeEachMigrateStatement",
	"afterEachMigrateStatement",
	"afterEachMigrateStatementError",
	"beforeClean",
	"afterClean",
	"beforeEachClean",
	"afterEachClean",
	"afterCleanError",
	"beforeInfo",
	"afterInfo",
	"afterInfoError",
	"beforeValidate",
	"afterValidate",
	"afterValidateError",
	"beforeBaseline",
	"afterBaseline",
	"afterBaselineError",
	"beforeRepair",
	"afterRepair",
	"afterRepairError",
	"beforeUndo",
	"afterUndo",
	"beforeEachUndo",
	"afterEachUndo",
	"afterUndoError",
	"beforeEachUndoStatement",
	"afterEachUndoStatement",
	"afterEachUndoStatementError",
	"beforeCreateSchema",
	"createSchema",
	"afterCreateSchema",
	"afterCreateSchemaError",
}

type seenTracking struct {
	versions         map[string]string // normalized version string -> fullPath
	repeatables      map[string]string // description -> fullPath
	baselineVersions map[string]string // normalized version string -> fullPath
}

// Resolve scans configured locations and returns all resolved migrations and callbacks.
func (r *Resolver) Resolve() (*ResolveResult, error) {
	result := &ResolveResult{
		Callbacks: make(map[string][]ResolvedCallback),
	}

	seen := &seenTracking{
		versions:         make(map[string]string),
		repeatables:      make(map[string]string),
		baselineVersions: make(map[string]string),
	}

	for _, loc := range r.config.Locations {
		cleanLoc := strings.TrimPrefix(loc, "filesystem:")
		cleanLoc = strings.TrimPrefix(cleanLoc, "classpath:")

		// Check if scanning from provided fs.FS
		if r.config.FS != nil {
			cleanDir := strings.TrimPrefix(cleanLoc, "/")
			if cleanDir == "" {
				cleanDir = "."
			}
			statInfo, statErr := fs.Stat(r.config.FS, cleanDir)
			if statErr != nil {
				if os.IsNotExist(statErr) || errors.Is(statErr, fs.ErrNotExist) {
					if !r.config.FailOnMissingLocations {
						slog.Error(fmt.Sprintf("migration location '%s' not found: %v", loc, statErr))
						continue
					}
					return nil, fmt.Errorf("migration location does not exist: %s", loc)
				}
				return nil, fmt.Errorf("error accessing location '%s': %w", loc, statErr)
			}
			if !statInfo.IsDir() {
				// Flyway skips locations that are files
				continue
			}
			err := r.scanFS(r.config.FS, cleanLoc, result, seen)
			if err != nil {
				return nil, err
			}
			continue
		}

		// Filesystem scan
		info, err := os.Stat(cleanLoc)
		if err != nil {
			if !r.config.FailOnMissingLocations {
				slog.Error(fmt.Sprintf("migration directory '%s' does not exist", cleanLoc))
				continue
			}
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("migration directory does not exist: %s", cleanLoc)
			}
			return nil, fmt.Errorf("error accessing migration directory '%s': %w", cleanLoc, err)
		}

		if !info.IsDir() {
			// Flyway skips locations that are files
			continue
		}

		visited := make(map[string]bool)
		err = r.scanDir(cleanLoc, cleanLoc, result, seen, visited)
		if err != nil {
			return nil, err
		}
	}

	// Sort versioned migrations
	sort.Slice(result.VersionedMigrations, func(i, j int) bool {
		return result.VersionedMigrations[i].Version.Compare(*result.VersionedMigrations[j].Version) < 0
	})

	// Sort repeatable migrations strictly by description
	sort.Slice(result.RepeatableMigrations, func(i, j int) bool {
		return result.RepeatableMigrations[i].Description < result.RepeatableMigrations[j].Description
	})

	// Sort baseline migrations by version
	sort.Slice(result.BaselineMigrations, func(i, j int) bool {
		return result.BaselineMigrations[i].Version.Compare(*result.BaselineMigrations[j].Version) < 0
	})

	// Sort callbacks for each event alphabetically by description (with deterministic fallback)
	for event := range result.Callbacks {
		sort.SliceStable(result.Callbacks[event], func(i, j int) bool {
			cbI := result.Callbacks[event][i]
			cbJ := result.Callbacks[event][j]
			if cbI.Description != cbJ.Description {
				return cbI.Description < cbJ.Description
			}
			if cbI.PhysicalLocation != cbJ.PhysicalLocation {
				return cbI.PhysicalLocation < cbJ.PhysicalLocation
			}
			return cbI.Filename < cbJ.Filename
		})
	}

	return result, nil
}

func isScriptFile(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.HasSuffix(lower, ".sh") ||
		strings.HasSuffix(lower, ".bash") ||
		strings.HasSuffix(lower, ".cmd") ||
		strings.HasSuffix(lower, ".ps1") ||
		strings.HasSuffix(lower, ".bat") ||
		strings.HasSuffix(lower, ".py")
}

func hasSuffixFold(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return strings.EqualFold(s[len(s)-len(suffix):], suffix)
}

func trimSuffixFold(s, suffix string) string {
	if hasSuffixFold(s, suffix) {
		return s[:len(s)-len(suffix)]
	}
	return s
}

func javaCharset(name string) (encoding.Encoding, string, error) {
	upper := strings.ToUpper(strings.TrimSpace(name))
	switch upper {
	case "", "UTF-8", "UTF8":
		return nil, "utf-8", nil
	case "US-ASCII", "ASCII":
		return nil, "ascii", nil
	case "ISO-8859-1", "ISO8859_1", "LATIN1", "LATIN-1", "ISO-LATIN-1":
		return charmap.ISO8859_1, "", nil
	case "ISO-8859-2", "ISO8859_2", "LATIN2":
		return charmap.ISO8859_2, "", nil
	case "ISO-8859-3", "ISO8859_3", "LATIN3":
		return charmap.ISO8859_3, "", nil
	case "ISO-8859-4", "ISO8859_4", "LATIN4":
		return charmap.ISO8859_4, "", nil
	case "ISO-8859-5", "ISO8859_5":
		return charmap.ISO8859_5, "", nil
	case "ISO-8859-6", "ISO8859_6":
		return charmap.ISO8859_6, "", nil
	case "ISO-8859-7", "ISO8859_7":
		return charmap.ISO8859_7, "", nil
	case "ISO-8859-8", "ISO8859_8":
		return charmap.ISO8859_8, "", nil
	case "ISO-8859-9", "ISO8859_9", "LATIN5":
		return charmap.ISO8859_9, "", nil
	case "ISO-8859-10", "ISO8859_10", "LATIN6":
		return charmap.ISO8859_10, "", nil
	case "ISO-8859-13", "ISO8859_13", "LATIN7":
		return charmap.ISO8859_13, "", nil
	case "ISO-8859-14", "ISO8859_14", "LATIN8":
		return charmap.ISO8859_14, "", nil
	case "ISO-8859-15", "ISO8859_15", "LATIN9":
		return charmap.ISO8859_15, "", nil
	case "ISO-8859-16", "ISO8859_16", "LATIN10":
		return charmap.ISO8859_16, "", nil
	case "WINDOWS-1250", "CP1250":
		return charmap.Windows1250, "", nil
	case "WINDOWS-1251", "CP1251":
		return charmap.Windows1251, "", nil
	case "WINDOWS-1252", "CP1252":
		return charmap.Windows1252, "", nil
	case "WINDOWS-1253", "CP1253":
		return charmap.Windows1253, "", nil
	case "WINDOWS-1254", "CP1254":
		return charmap.Windows1254, "", nil
	case "WINDOWS-1255", "CP1255":
		return charmap.Windows1255, "", nil
	case "WINDOWS-1256", "CP1256":
		return charmap.Windows1256, "", nil
	case "WINDOWS-1257", "CP1257":
		return charmap.Windows1257, "", nil
	case "WINDOWS-1258", "CP1258":
		return charmap.Windows1258, "", nil
	case "UTF-16":
		// Java: use the BOM if there is one, and remove it. Otherwise big-endian.
		return textunicode.UTF16(textunicode.BigEndian, textunicode.UseBOM), "", nil
	case "UTF-16BE":
		return textunicode.UTF16(textunicode.BigEndian, textunicode.IgnoreBOM), "", nil
	case "UTF-16LE":
		return textunicode.UTF16(textunicode.LittleEndian, textunicode.IgnoreBOM), "", nil
	}
	return nil, "", fmt.Errorf("unsupported encoding '%s'", name)
}

func decodeUTF8(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		sb.WriteRune(r) // utf8.RuneError (U+FFFD) for an invalid byte
		b = b[size:]
	}
	return sb.String()
}

func decodeASCII(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		if c <= 0x7F {
			sb.WriteByte(c)
		} else {
			sb.WriteRune(utf8.RuneError)
		}
	}
	return sb.String()
}

func decodeContent(rawData []byte, encodingName string) (string, error) {
	enc, special, err := javaCharset(encodingName)
	if err != nil {
		return "", err
	}
	var text string
	switch special {
	case "utf-8":
		text = decodeUTF8(rawData)
	case "ascii":
		text = decodeASCII(rawData)
	default:
		decoded, err := enc.NewDecoder().Bytes(rawData)
		if err != nil {
			return "", fmt.Errorf("error decoding with '%s': %w", encodingName, err)
		}
		text = string(decoded)
	}
	// For every encoding, strip leading BOM before checksum and SQL execution
	text = strings.TrimPrefix(text, "\uFEFF")
	return text, nil
}

func (r *Resolver) loadMigrationConfig(dirPath, filename string, fileSys fs.FS) MigrationConfig {
	cfg := MigrationConfig{
		CustomProperties: make(map[string]string),
	}

	candidates := []string{
		filename + ".conf", // e.g. V1__init.sql.conf or V1__init.sh.conf
	}
	for _, suffix := range r.config.Suffixes {
		if hasSuffixFold(filename, suffix) {
			candidates = append(candidates, trimSuffixFold(filename, suffix)+".conf")
			break
		}
	}

	for _, candidate := range candidates {
		var content []byte
		var err error
		if fileSys != nil {
			candPath := candidate
			if dirPath != "" && dirPath != "." {
				candPath = filepath.Join(dirPath, candidate)
			}
			candPath = strings.TrimPrefix(candPath, "/")
			content, err = fs.ReadFile(fileSys, candPath)
		} else {
			candPath := filepath.Join(dirPath, candidate)
			content, err = os.ReadFile(candPath)
		}
		if err == nil {
			parseMigrationConfigFile(string(content), &cfg)
			return cfg
		}
	}

	return cfg
}

func parseMigrationConfigFile(content string, cfg *MigrationConfig) {
	if cfg.CustomProperties == nil {
		cfg.CustomProperties = make(map[string]string)
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "--") {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch strings.ToLower(key) {
		case "shouldexecute":
			cfg.ShouldExecute = val
		case "executeintransaction":
			b := strings.EqualFold(val, "true") || val == "1"
			cfg.ExecuteInTransaction = &b
		case "placeholderreplacement":
			b := strings.EqualFold(val, "true") || val == "1"
			cfg.PlaceholderReplacement = &b
		case "encoding":
			cfg.Encoding = val
		default:
			cfg.CustomProperties[key] = val
		}
	}
}

func (r *Resolver) scanDir(locRoot, dirPath string, result *ResolveResult, seen *seenTracking, visited map[string]bool) error {
	real, err := filepath.EvalSymlinks(dirPath)
	if err != nil {
		return fmt.Errorf("error resolving directory '%s': %w", dirPath, err)
	}
	if visited[real] {
		return nil // already scanned through another path, or a cycle
	}
	visited[real] = true

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("error reading directory '%s': %w", dirPath, err)
	}

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		fullPath := filepath.Join(dirPath, entry.Name())
		isDir := entry.IsDir()
		if !isDir {
			if fi, err := os.Stat(fullPath); err == nil && fi.IsDir() {
				isDir = true
			}
		}

		if isDir {
			if err := r.scanDir(locRoot, fullPath, result, seen, visited); err != nil {
				return err
			}
			continue
		}

		relPath, err := filepath.Rel(locRoot, fullPath)
		if err != nil {
			relPath = entry.Name()
		}
		scriptPath := filepath.ToSlash(relPath)
		if err := r.processFile(dirPath, entry.Name(), fullPath, scriptPath, result, seen); err != nil {
			return err
		}
	}

	return nil
}

func (r *Resolver) scanFS(fileSys fs.FS, dirPath string, result *ResolveResult, seen *seenTracking) error {
	cleanDir := strings.TrimPrefix(dirPath, "/")
	if cleanDir == "" {
		cleanDir = "."
	}

	return fs.WalkDir(fileSys, cleanDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != cleanDir && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}

		filename := d.Name()
		if hasSuffixFold(filename, ".conf") {
			return nil
		}

		hasValidSuffix := false
		for _, suffix := range r.config.Suffixes {
			if hasSuffixFold(filename, suffix) {
				hasValidSuffix = true
				break
			}
		}
		if !hasValidSuffix {
			return nil
		}

		data, err := fs.ReadFile(fileSys, path)
		if err != nil {
			return fmt.Errorf("error reading file '%s': %w", path, err)
		}

		dir := filepath.Dir(path)
		relPath, err := filepath.Rel(cleanDir, path)
		if err != nil {
			relPath = filename
		}
		scriptPath := filepath.ToSlash(relPath)
		return r.parseAndAddMigration(dir, filename, path, scriptPath, data, fileSys, result, seen)
	})
}

func (r *Resolver) processFile(dir, filename, fullPath, scriptPath string, result *ResolveResult, seen *seenTracking) error {
	if hasSuffixFold(filename, ".conf") {
		return nil
	}

	hasValidSuffix := false
	for _, suffix := range r.config.Suffixes {
		if hasSuffixFold(filename, suffix) {
			hasValidSuffix = true
			break
		}
	}
	if !hasValidSuffix {
		return nil
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Errorf("error reading file '%s': %w", fullPath, err)
	}

	return r.parseAndAddMigration(dir, filename, fullPath, scriptPath, data, nil, result, seen)
}

func (r *Resolver) calculateChecksum(content string, migConfig MigrationConfig, isRepeatable bool, isScript bool, filename string) (int64, *int64, error) {
	rawCs, err := checksum.CalculateString(content)
	if err != nil {
		return 0, nil, err
	}

	if !isRepeatable {
		return rawCs, nil, nil
	}

	eqCs := rawCs
	calcContent := content
	if r.config.Replacer != nil {
		shouldReplace := r.config.Replacer.IsEnabled()
		if r.config.PlaceholderReplacement != nil {
			shouldReplace = *r.config.PlaceholderReplacement
		}
		if migConfig.PlaceholderReplacement != nil {
			shouldReplace = *migConfig.PlaceholderReplacement
		}
		if shouldReplace {
			builtins := r.config.Builtins
			if builtins.Filename == "" {
				builtins.Filename = filename
			}
			replacer := r.config.Replacer
			if isScript {
				replacer = replacer.ScriptReplacer()
			}
			replaced, err := replacer.ReplaceContent(content, builtins)
			if err != nil {
				return 0, nil, fmt.Errorf("failed to replace placeholders in repeatable migration: %w", err)
			}
			calcContent = replaced
		}
	}
	cs, err := checksum.CalculateString(calcContent)
	if err != nil {
		return 0, nil, err
	}
	return cs, &eqCs, nil
}

func (r *Resolver) parseAndAddMigration(dir, filename, fullPath, scriptPath string, rawData []byte, fileSys fs.FS, result *ResolveResult, seen *seenTracking) error {
	migConfig := r.loadMigrationConfig(dir, filename, fileSys)
	encoding := r.config.Encoding
	if migConfig.Encoding != "" {
		encoding = migConfig.Encoding
	}
	content, err := decodeContent(rawData, encoding)
	if err != nil {
		return fmt.Errorf("failed to decode '%s': %w", filename, err)
	}

	isScript := isScriptFile(filename)

	defaultType := TypeSQL
	if isScript {
		defaultType = TypeScript
	}

	// Check for standard callbacks (e.g. beforeMigrate.sql, beforeMigrate__desc.sql, afterMigrate.sh)
	baseWithoutExt := filename
	for _, suffix := range r.config.Suffixes {
		if hasSuffixFold(baseWithoutExt, suffix) {
			baseWithoutExt = trimSuffixFold(baseWithoutExt, suffix)
			break
		}
	}

	var eventCandidate string
	var desc string
	sep := r.config.Separator
	if sep == "" {
		sep = "__"
	}
	sepIdx := strings.Index(baseWithoutExt, sep)
	if sepIdx >= 0 {
		eventCandidate = baseWithoutExt[:sepIdx]
		desc = baseWithoutExt[sepIdx+len(sep):]
		desc = strings.ReplaceAll(desc, "_", " ")
	} else {
		eventCandidate = baseWithoutExt
		desc = ""
	}

	for _, event := range StandardCallbackEvents {
		if eventCandidate == event {
			result.Callbacks[event] = append(result.Callbacks[event], ResolvedCallback{
				Event:            event,
				Description:      desc,
				Filename:         filename,
				Content:          content,
				PhysicalLocation: fullPath,
				Type:             defaultType,
				IsScript:         isScript,
				Config:           migConfig,
			})
			return nil
		}
	}

	// Check for repeatable migration: R__description.sql / R__description.sh
	if strings.HasPrefix(filename, r.config.RepeatablePrefix+r.config.Separator) {
		desc := strings.TrimPrefix(filename, r.config.RepeatablePrefix+r.config.Separator)
		for _, suffix := range r.config.Suffixes {
			if hasSuffixFold(desc, suffix) {
				desc = trimSuffixFold(desc, suffix)
				break
			}
		}
		desc = strings.ReplaceAll(desc, "_", " ")

		if prevPath, exists := seen.repeatables[desc]; exists {
			return fmt.Errorf("found more than one repeatable migration with description '%s'\nOffenders:\n-> %s\n-> %s", desc, prevPath, fullPath)
		}
		seen.repeatables[desc] = fullPath

		cs, eqCs, err := r.calculateChecksum(content, migConfig, true, isScript, filename)
		if err != nil {
			return fmt.Errorf("error calculating checksum for '%s': %w", filename, err)
		}

		repType := TypeSQL
		if isScript {
			repType = TypeScript
		}

		result.RepeatableMigrations = append(result.RepeatableMigrations, ResolvedMigration{
			Version:            nil,
			Description:        desc,
			Script:             scriptPath,
			Checksum:           cs,
			EquivalentChecksum: eqCs,
			Type:               repType,
			Content:            content,
			PhysicalLocation:   fullPath,
			IsRepeatable:       true,
			IsScript:           isScript,
			Config:             migConfig,
		})
		slog.Debug("Resolved repeatable migration", "script", scriptPath, "desc", desc)
		return nil
	} else if strings.HasPrefix(filename, r.config.RepeatablePrefix) {
		if r.config.ValidateMigrationNaming && r.isSQLMigrationFile(filename) {
			return fmt.Errorf("invalid repeatable migration name: %s", filename)
		}
		slog.Info(fmt.Sprintf("Skipping file '%s' (not a valid migration name)", filename))
		return nil
	}

	// Check for undo migration: U1.2__description.sql / U1.2__description.sh
	if strings.HasPrefix(filename, r.config.UndoPrefix) {
		verDesc := strings.TrimPrefix(filename, r.config.UndoPrefix)
		for _, suffix := range r.config.Suffixes {
			if hasSuffixFold(verDesc, suffix) {
				verDesc = trimSuffixFold(verDesc, suffix)
				break
			}
		}

		parts := strings.SplitN(verDesc, r.config.Separator, 2)
		if len(parts) == 2 {
			verStr := parts[0]
			desc := strings.ReplaceAll(parts[1], "_", " ")
			ver, err := version.Parse(verStr)
			if err == nil {
				cs, _, err := r.calculateChecksum(content, migConfig, false, isScript, filename)
				if err != nil {
					return fmt.Errorf("error calculating checksum for '%s': %w", filename, err)
				}
				undoType := TypeUndoSQL
				if isScript {
					undoType = TypeUndoScript
				}
				result.UndoMigrations = append(result.UndoMigrations, ResolvedMigration{
					Version:          &ver,
					Description:      desc,
					Script:           scriptPath,
					Checksum:         cs,
					Type:             undoType,
					Content:          content,
					PhysicalLocation: fullPath,
					IsUndo:           true,
					IsScript:         isScript,
					Config:           migConfig,
				})
				slog.Debug("Resolved undo migration", "script", scriptPath, "version", ver.String())
				return nil
			}
		}
		if r.config.ValidateMigrationNaming && r.isSQLMigrationFile(filename) {
			return fmt.Errorf("invalid undo migration name: %s", filename)
		}
		slog.Info(fmt.Sprintf("Skipping file '%s' (not a valid migration name)", filename))
		return nil
	}

	// Check for baseline migration: B1.2__description.sql or B__1.2__description.sql
	if strings.HasPrefix(filename, r.config.BaselinePrefix) {
		verDesc := strings.TrimPrefix(filename, r.config.BaselinePrefix)
		verDesc = strings.TrimPrefix(verDesc, r.config.Separator)
		for _, suffix := range r.config.Suffixes {
			if hasSuffixFold(verDesc, suffix) {
				verDesc = trimSuffixFold(verDesc, suffix)
				break
			}
		}

		parts := strings.SplitN(verDesc, r.config.Separator, 2)
		if len(parts) == 2 {
			verStr := parts[0]
			desc := strings.ReplaceAll(parts[1], "_", " ")
			ver, err := version.Parse(verStr)
			if err == nil {
				canonicalVer := ver.Normalized()
				if prevPath, exists := seen.baselineVersions[canonicalVer]; exists {
					return fmt.Errorf("found more than one baseline migration with version %s\nOffenders:\n-> %s\n-> %s", canonicalVer, prevPath, fullPath)
				}
				seen.baselineVersions[canonicalVer] = fullPath

				cs, _, err := r.calculateChecksum(content, migConfig, false, isScript, filename)
				if err != nil {
					return fmt.Errorf("error calculating checksum for '%s': %w", filename, err)
				}
				result.BaselineMigrations = append(result.BaselineMigrations, ResolvedMigration{
					Version:          &ver,
					Description:      desc,
					Script:           scriptPath,
					Checksum:         cs,
					Type:             TypeBaseline,
					Content:          content,
					PhysicalLocation: fullPath,
					IsBaseline:       true,
					IsScript:         isScript,
					Config:           migConfig,
				})
				slog.Debug("Resolved baseline migration", "script", scriptPath, "version", ver.String())
				return nil
			}
		}
		if r.config.ValidateMigrationNaming && r.isSQLMigrationFile(filename) {
			return fmt.Errorf("invalid baseline migration name: %s", filename)
		}
		slog.Info(fmt.Sprintf("Skipping file '%s' (not a valid migration name)", filename))
		return nil
	}

	// Check for versioned migration: V1.2__description.sql / V1.2__description.sh or V1.2.sql
	if strings.HasPrefix(filename, r.config.Prefix) {
		verDesc := strings.TrimPrefix(filename, r.config.Prefix)
		for _, suffix := range r.config.Suffixes {
			if hasSuffixFold(verDesc, suffix) {
				verDesc = trimSuffixFold(verDesc, suffix)
				break
			}
		}

		var verStr, desc string
		sepIdx := strings.Index(verDesc, r.config.Separator)
		if sepIdx >= 0 {
			verStr = verDesc[:sepIdx]
			desc = strings.ReplaceAll(verDesc[sepIdx+len(r.config.Separator):], "_", " ")
		} else {
			verStr = verDesc
			desc = ""
		}

		if verStr != "" && unicode.IsDigit(rune(verStr[0])) {
			ver, err := version.Parse(verStr)
			if err != nil {
				if r.config.ValidateMigrationNaming && r.isSQLMigrationFile(filename) {
					return fmt.Errorf("invalid version '%s' in migration file '%s': %w", verStr, filename, err)
				}
				slog.Info(fmt.Sprintf("Skipping file '%s' (invalid migration version: %v)", filename, err))
				return nil
			}

			canonicalVer := ver.Normalized()
			if prevFile, exists := seen.versions[canonicalVer]; exists {
				return fmt.Errorf("found more than one migration with version %s\nOffenders:\n-> %s\n-> %s", canonicalVer, prevFile, fullPath)
			}
			seen.versions[canonicalVer] = fullPath

			cs, _, err := r.calculateChecksum(content, migConfig, false, isScript, filename)
			if err != nil {
				return fmt.Errorf("error calculating checksum for '%s': %w", filename, err)
			}

			result.VersionedMigrations = append(result.VersionedMigrations, ResolvedMigration{
				Version:          &ver,
				Description:      desc,
				Script:           scriptPath,
				Checksum:         cs,
				Type:             defaultType,
				Content:          content,
				PhysicalLocation: fullPath,
				IsScript:         isScript,
				Config:           migConfig,
			})
			slog.Debug("Resolved versioned migration", "script", scriptPath, "version", ver.String())
			return nil
		}

		if r.config.ValidateMigrationNaming && r.isSQLMigrationFile(filename) {
			return fmt.Errorf("invalid versioned migration name: %s", filename)
		}
		slog.Info(fmt.Sprintf("Skipping file '%s' (not a valid migration name)", filename))
		return nil
	}

	if r.config.ValidateMigrationNaming && r.isSQLMigrationFile(filename) {
		return fmt.Errorf("invalid migration name: %s", filename)
	}
	slog.Info(fmt.Sprintf("Skipping file '%s' (not a valid migration name)", filename))
	return nil
}
