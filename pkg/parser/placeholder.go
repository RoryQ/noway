package parser

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// PlaceholderConfig holds the configuration for placeholder replacement.
type PlaceholderConfig struct {
	Enabled        bool
	Prefix         string
	Suffix         string
	Separator      string
	Values         map[string]string
	DisableEscapes bool
	ScriptMode     bool
	ScriptPrefix   string
	ScriptSuffix   string
}

// DefaultPlaceholderConfig returns the standard Flyway placeholder settings.
func DefaultPlaceholderConfig() PlaceholderConfig {
	return PlaceholderConfig{
		Enabled:      true,
		Prefix:       "${",
		Suffix:       "}",
		Separator:    ":",
		ScriptPrefix: "FP__",
		ScriptSuffix: "__",
		Values:       make(map[string]string),
	}
}

// PlaceholderReplacer handles replacing placeholders in SQL scripts.
type PlaceholderReplacer struct {
	config PlaceholderConfig
}

// NewPlaceholderReplacer creates a new replacer.
func NewPlaceholderReplacer(cfg PlaceholderConfig) *PlaceholderReplacer {
	if cfg.Prefix == "" {
		cfg.Prefix = "${"
	}
	if cfg.Suffix == "" {
		cfg.Suffix = "}"
	}
	if cfg.Separator == "" {
		cfg.Separator = ":"
	}
	if cfg.ScriptPrefix == "" {
		cfg.ScriptPrefix = "FP__"
	}
	if cfg.ScriptSuffix == "" {
		cfg.ScriptSuffix = "__"
	}
	if cfg.Values == nil {
		cfg.Values = make(map[string]string)
	}
	return &PlaceholderReplacer{config: cfg}
}

// ScriptReplacer returns a replacer configured to replace script placeholders of the form FP__<key>__.
func (p *PlaceholderReplacer) ScriptReplacer() *PlaceholderReplacer {
	scriptValues := make(map[string]string)
	for k, v := range p.config.Values {
		scriptValues[strings.ReplaceAll(k, ":", "_")] = v
	}
	sPrefix := p.config.ScriptPrefix
	if sPrefix == "" {
		sPrefix = "FP__"
	}
	sSuffix := p.config.ScriptSuffix
	if sSuffix == "" {
		sSuffix = "__"
	}
	cfg := PlaceholderConfig{
		Enabled:        p.config.Enabled,
		Prefix:         sPrefix,
		Suffix:         sSuffix,
		Separator:      "",
		Values:         scriptValues,
		DisableEscapes: true,
		ScriptMode:     true,
		ScriptPrefix:   sPrefix,
		ScriptSuffix:   sSuffix,
	}
	return NewPlaceholderReplacer(cfg)
}

// BuiltinPlaceholders contains contextual values for built-in placeholders.
type BuiltinPlaceholders struct {
	DefaultSchema    string
	Table            string
	User             string
	Database         string
	Timestamp        time.Time
	Filename         string
	WorkingDirectory string
	Environment      string
}

// IsEnabled returns whether placeholder replacement is enabled.
func (p *PlaceholderReplacer) IsEnabled() bool {
	return p.config.Enabled
}

// Replace substitutes all placeholders in the input SQL if replacement is enabled.
func (p *PlaceholderReplacer) Replace(input string, builtins BuiltinPlaceholders) (string, error) {
	if !p.config.Enabled {
		return input, nil
	}
	return p.ReplaceContent(input, builtins)
}

// ReplaceContent substitutes placeholders in the input SQL unconditionally.
func (p *PlaceholderReplacer) ReplaceContent(input string, builtins BuiltinPlaceholders) (string, error) {
	prefix := p.config.Prefix
	suffix := p.config.Suffix
	var escapedPrefix1, escapedPrefix2 string
	if !p.config.DisableEscapes {
		escapedPrefix1 = "$" + prefix // e.g. $${ or $@[
		if len(prefix) > 0 {
			escapedPrefix2 = string(prefix[0]) + prefix // e.g. @@[ or $${
		}
	}

	// Build combined placeholder map and case-insensitive index (Flyway placeholder matching is case-insensitive)
	values := make(map[string]string)
	lowerValues := make(map[string]string)
	for k, v := range p.config.Values {
		values[k] = v
		lowerValues[strings.ToLower(k)] = v
	}

	// Helper to add builtins
	addBuiltin := func(k, v string) {
		if v != "" {
			values[k] = v
			lowerValues[strings.ToLower(k)] = v
		}
	}

	defaultSchema := builtins.DefaultSchema
	if defaultSchema == "" {
		defaultSchema = "flyway"
	}
	addBuiltin("flyway:defaultSchema", defaultSchema)
	addBuiltin("noway:defaultSchema", defaultSchema)
	if p.config.ScriptMode {
		addBuiltin("flyway_defaultSchema", defaultSchema)
		addBuiltin("noway_defaultSchema", defaultSchema)
	}

	table := builtins.Table
	if table == "" {
		table = "flyway_schema_history"
	}
	addBuiltin("flyway:table", table)
	addBuiltin("noway:table", table)
	if p.config.ScriptMode {
		addBuiltin("flyway_table", table)
		addBuiltin("noway_table", table)
	}

	user := builtins.User
	if user == "" {
		user = "flyway"
	}
	addBuiltin("flyway:user", user)
	addBuiltin("noway:user", user)
	if p.config.ScriptMode {
		addBuiltin("flyway_user", user)
		addBuiltin("noway_user", user)
	}

	database := builtins.Database
	if database == "" {
		database = defaultSchema
	}
	addBuiltin("flyway:database", database)
	addBuiltin("noway:database", database)
	if p.config.ScriptMode {
		addBuiltin("flyway_database", database)
		addBuiltin("noway_database", database)
	}

	ts := builtins.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	formattedTs := ts.Format("2006-01-02 15:04:05")
	addBuiltin("flyway:timestamp", formattedTs)
	addBuiltin("noway:timestamp", formattedTs)
	if p.config.ScriptMode {
		addBuiltin("flyway_timestamp", formattedTs)
		addBuiltin("noway_timestamp", formattedTs)
	}

	filename := builtins.Filename
	if filename != "" {
		addBuiltin("flyway:filename", filename)
		addBuiltin("noway:filename", filename)
		if p.config.ScriptMode {
			addBuiltin("flyway_filename", filename)
			addBuiltin("noway_filename", filename)
		}
	}

	workingDir := builtins.WorkingDirectory
	if workingDir == "" {
		if wd, err := os.Getwd(); err == nil {
			workingDir = wd
		}
	}
	if workingDir != "" {
		addBuiltin("flyway:workingDirectory", workingDir)
		addBuiltin("noway:workingDirectory", workingDir)
		if p.config.ScriptMode {
			addBuiltin("flyway_workingDirectory", workingDir)
			addBuiltin("noway_workingDirectory", workingDir)
		}
	}

	if builtins.Environment != "" {
		addBuiltin("flyway:environment", builtins.Environment)
		addBuiltin("noway:environment", builtins.Environment)
		if p.config.ScriptMode {
			addBuiltin("flyway_environment", builtins.Environment)
			addBuiltin("noway_environment", builtins.Environment)
		}
	}

	lookupValue := func(key string) (string, bool) {
		if val, ok := values[key]; ok {
			return val, true
		}
		if val, ok := lowerValues[strings.ToLower(key)]; ok {
			return val, true
		}
		return "", false
	}

	var sb strings.Builder
	sb.Grow(len(input))
	i := 0
	for i < len(input) {
		// Check for doubled prefix escaping or $ escaping if escapes are enabled
		if !p.config.DisableEscapes {
			if escapedPrefix2 != "" && escapedPrefix2 != escapedPrefix1 && strings.HasPrefix(input[i:], escapedPrefix2) {
				sb.WriteString(prefix)
				i += len(escapedPrefix2)
				continue
			}
			if strings.HasPrefix(input[i:], escapedPrefix1) {
				sb.WriteString(prefix)
				i += len(escapedPrefix1)
				continue
			}
		}

		// Check for standard placeholder prefix: ${...} or FP__...__
		if strings.HasPrefix(input[i:], prefix) {
			suffixIdx := strings.Index(input[i+len(prefix):], suffix)
			if suffixIdx != -1 {
				placeholderExpr := input[i+len(prefix) : i+len(prefix)+suffixIdx]
				if val, ok := lookupValue(placeholderExpr); ok {
					sb.WriteString(val)
					i += len(prefix) + suffixIdx + len(suffix)
					continue
				}

				return "", fmt.Errorf("no value provided for placeholder: %s%s%s. Check your configuration or migration script", prefix, placeholderExpr, suffix)
			}
		}

		sb.WriteByte(input[i])
		i++
	}

	return sb.String(), nil
}
