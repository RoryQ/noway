package resolver

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/RoryQ/noway/pkg/checksum"
	"github.com/RoryQ/noway/pkg/version"
)

// ResolverConfig configures how migrations are discovered and parsed.
type ResolverConfig struct {
	Locations          []string // e.g. "filesystem:sql", "migrations"
	FS                 fs.FS    // Optional embedded filesystem
	Prefix             string   // default "V"
	RepeatablePrefix   string   // default "R"
	UndoPrefix         string   // default "U"
	BaselinePrefix     string   // default "B"
	Separator          string   // default "__"
	Suffixes           []string // default [".sql"]
	Encoding           string   // default "UTF-8"
	IgnoreMissingFiles bool
}

// DefaultResolverConfig returns standard defaults.
func DefaultResolverConfig() ResolverConfig {
	return ResolverConfig{
		Locations:        []string{"filesystem:sql"},
		Prefix:           "V",
		RepeatablePrefix: "R",
		UndoPrefix:       "U",
		BaselinePrefix:   "B",
		Separator:        "__",
		Suffixes:         []string{".sql"},
		Encoding:         "UTF-8",
	}
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
		cfg.Suffixes = []string{".sql"}
	}
	if len(cfg.Locations) == 0 && cfg.FS == nil {
		cfg.Locations = []string{"filesystem:sql"}
	}
	return &Resolver{config: cfg}
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
	Event            string // e.g. "beforeMigrate", "afterMigrate"
	Filename         string
	Content          string
	PhysicalLocation string
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
	"createSchema",
}

// Resolve scans configured locations and returns all resolved migrations and callbacks.
func (r *Resolver) Resolve() (*ResolveResult, error) {
	result := &ResolveResult{
		Callbacks: make(map[string][]ResolvedCallback),
	}

	seenVersions := make(map[string]string) // version string -> filename

	for _, loc := range r.config.Locations {
		cleanLoc := strings.TrimPrefix(loc, "filesystem:")
		cleanLoc = strings.TrimPrefix(cleanLoc, "classpath:")

		// Check if scanning from provided fs.FS
		if r.config.FS != nil {
			err := r.scanFS(r.config.FS, cleanLoc, result, seenVersions)
			if err != nil && !r.config.IgnoreMissingFiles {
				return nil, fmt.Errorf("error reading location '%s': %w", loc, err)
			}
			continue
		}

		// Filesystem scan
		info, err := os.Stat(cleanLoc)
		if err != nil {
			if os.IsNotExist(err) && r.config.IgnoreMissingFiles {
				continue
			}
			// If location does not exist, return error unless IgnoreMissingFiles
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("migration directory does not exist: %s", cleanLoc)
			}
			return nil, fmt.Errorf("error accessing migration directory '%s': %w", cleanLoc, err)
		}

		if info.IsDir() {
			err := r.scanDir(cleanLoc, result, seenVersions)
			if err != nil {
				return nil, err
			}
		} else {
			// Single file
			err := r.processFile(filepath.Dir(cleanLoc), info.Name(), cleanLoc, result, seenVersions)
			if err != nil {
				return nil, err
			}
		}
	}

	// Sort versioned migrations
	sort.Slice(result.VersionedMigrations, func(i, j int) bool {
		return result.VersionedMigrations[i].Version.Compare(*result.VersionedMigrations[j].Version) < 0
	})

	// Sort repeatable migrations by description, then script name
	sort.Slice(result.RepeatableMigrations, func(i, j int) bool {
		if result.RepeatableMigrations[i].Description == result.RepeatableMigrations[j].Description {
			return result.RepeatableMigrations[i].Script < result.RepeatableMigrations[j].Script
		}
		return result.RepeatableMigrations[i].Description < result.RepeatableMigrations[j].Description
	})

	return result, nil
}

func (r *Resolver) scanDir(dirPath string, result *ResolveResult, seenVersions map[string]string) error {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("error reading directory '%s': %w", dirPath, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// Subdirectories can be scanned recursively
			subPath := filepath.Join(dirPath, entry.Name())
			if err := r.scanDir(subPath, result, seenVersions); err != nil {
				return err
			}
			continue
		}

		fullPath := filepath.Join(dirPath, entry.Name())
		if err := r.processFile(dirPath, entry.Name(), fullPath, result, seenVersions); err != nil {
			return err
		}
	}

	return nil
}

func (r *Resolver) scanFS(fileSys fs.FS, dirPath string, result *ResolveResult, seenVersions map[string]string) error {
	cleanDir := strings.TrimPrefix(dirPath, "/")
	if cleanDir == "" {
		cleanDir = "."
	}

	return fs.WalkDir(fileSys, cleanDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		filename := d.Name()
		data, err := fs.ReadFile(fileSys, path)
		if err != nil {
			return fmt.Errorf("error reading file '%s': %w", path, err)
		}

		return r.parseAndAddMigration(filename, path, string(data), result, seenVersions)
	})
}

func (r *Resolver) processFile(dir, filename, fullPath string, result *ResolveResult, seenVersions map[string]string) error {
	hasValidSuffix := false
	for _, suffix := range r.config.Suffixes {
		if strings.HasSuffix(filename, suffix) {
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

	return r.parseAndAddMigration(filename, fullPath, string(data), result, seenVersions)
}

func (r *Resolver) parseAndAddMigration(filename, fullPath, content string, result *ResolveResult, seenVersions map[string]string) error {
	// Check for standard callbacks first (e.g. beforeMigrate.sql, afterMigrate.sql)
	baseWithoutExt := filename
	for _, suffix := range r.config.Suffixes {
		if strings.HasSuffix(baseWithoutExt, suffix) {
			baseWithoutExt = strings.TrimSuffix(baseWithoutExt, suffix)
			break
		}
	}

	for _, event := range StandardCallbackEvents {
		if baseWithoutExt == event {
			result.Callbacks[event] = append(result.Callbacks[event], ResolvedCallback{
				Event:            event,
				Filename:         filename,
				Content:          content,
				PhysicalLocation: fullPath,
			})
			return nil
		}
	}

	// Check for repeatable migration: R__description.sql
	if strings.HasPrefix(filename, r.config.RepeatablePrefix+r.config.Separator) {
		desc := strings.TrimPrefix(filename, r.config.RepeatablePrefix+r.config.Separator)
		for _, suffix := range r.config.Suffixes {
			if strings.HasSuffix(desc, suffix) {
				desc = strings.TrimSuffix(desc, suffix)
				break
			}
		}
		desc = strings.ReplaceAll(desc, "_", " ")
		cs, err := checksum.CalculateString(content)
		if err != nil {
			return fmt.Errorf("error calculating checksum for '%s': %w", filename, err)
		}

		result.RepeatableMigrations = append(result.RepeatableMigrations, ResolvedMigration{
			Version:          nil,
			Description:      desc,
			Script:           filename,
			Checksum:         cs,
			Type:             TypeRepeatable,
			Content:          content,
			PhysicalLocation: fullPath,
			IsRepeatable:     true,
		})
		return nil
	}

	// Check for undo migration: U1.2__description.sql
	if strings.HasPrefix(filename, r.config.UndoPrefix) {
		verDesc := strings.TrimPrefix(filename, r.config.UndoPrefix)
		for _, suffix := range r.config.Suffixes {
			if strings.HasSuffix(verDesc, suffix) {
				verDesc = strings.TrimSuffix(verDesc, suffix)
				break
			}
		}

		parts := strings.SplitN(verDesc, r.config.Separator, 2)
		if len(parts) == 2 {
			verStr := parts[0]
			desc := strings.ReplaceAll(parts[1], "_", " ")
			ver, err := version.Parse(verStr)
			if err == nil {
				cs, err := checksum.CalculateString(content)
				if err != nil {
					return fmt.Errorf("error calculating checksum for '%s': %w", filename, err)
				}
				result.UndoMigrations = append(result.UndoMigrations, ResolvedMigration{
					Version:          &ver,
					Description:      desc,
					Script:           filename,
					Checksum:         cs,
					Type:             TypeUndo,
					Content:          content,
					PhysicalLocation: fullPath,
					IsUndo:           true,
				})
				return nil
			}
		}
	}

	// Check for baseline migration: B1.2__description.sql
	if strings.HasPrefix(filename, r.config.BaselinePrefix) {
		verDesc := strings.TrimPrefix(filename, r.config.BaselinePrefix)
		for _, suffix := range r.config.Suffixes {
			if strings.HasSuffix(verDesc, suffix) {
				verDesc = strings.TrimSuffix(verDesc, suffix)
				break
			}
		}

		parts := strings.SplitN(verDesc, r.config.Separator, 2)
		if len(parts) == 2 {
			verStr := parts[0]
			desc := strings.ReplaceAll(parts[1], "_", " ")
			ver, err := version.Parse(verStr)
			if err == nil {
				cs, err := checksum.CalculateString(content)
				if err != nil {
					return fmt.Errorf("error calculating checksum for '%s': %w", filename, err)
				}
				result.BaselineMigrations = append(result.BaselineMigrations, ResolvedMigration{
					Version:          &ver,
					Description:      desc,
					Script:           filename,
					Checksum:         cs,
					Type:             TypeBaseline,
					Content:          content,
					PhysicalLocation: fullPath,
					IsBaseline:       true,
				})
				return nil
			}
		}
	}

	// Check for versioned migration: V1.2__description.sql or V1.2.sql
	if strings.HasPrefix(filename, r.config.Prefix) {
		verDesc := strings.TrimPrefix(filename, r.config.Prefix)
		for _, suffix := range r.config.Suffixes {
			if strings.HasSuffix(verDesc, suffix) {
				verDesc = strings.TrimSuffix(verDesc, suffix)
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
				return fmt.Errorf("invalid version '%s' in migration file '%s': %w", verStr, filename, err)
			}

			canonicalVer := ver.Normalized()
			if prevFile, exists := seenVersions[canonicalVer]; exists {
				return fmt.Errorf("found more than one migration with version %s\nOffenders:\n-> %s\n-> %s", canonicalVer, prevFile, fullPath)
			}
			seenVersions[canonicalVer] = fullPath

			cs, err := checksum.CalculateString(content)
			if err != nil {
				return fmt.Errorf("error calculating checksum for '%s': %w", filename, err)
			}

			result.VersionedMigrations = append(result.VersionedMigrations, ResolvedMigration{
				Version:          &ver,
				Description:      desc,
				Script:           filename,
				Checksum:         cs,
				Type:             TypeSQL,
				Content:          content,
				PhysicalLocation: fullPath,
			})
			return nil
		}
	}

	return nil
}
