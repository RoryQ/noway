package parser

import (
	"fmt"
	"strings"
	"time"
)

// PlaceholderConfig holds the configuration for placeholder replacement.
type PlaceholderConfig struct {
	Enabled   bool
	Prefix    string
	Suffix    string
	Separator string
	Values    map[string]string
}

// DefaultPlaceholderConfig returns the standard Flyway placeholder settings.
func DefaultPlaceholderConfig() PlaceholderConfig {
	return PlaceholderConfig{
		Enabled:   true,
		Prefix:    "${",
		Suffix:    "}",
		Separator: ":",
		Values:    make(map[string]string),
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
	if cfg.Values == nil {
		cfg.Values = make(map[string]string)
	}
	return &PlaceholderReplacer{config: cfg}
}

// BuiltinPlaceholders contains contextual values for built-in placeholders.
type BuiltinPlaceholders struct {
	DefaultSchema string
	Table         string
	User          string
	Database      string
	Timestamp     time.Time
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
	sep := p.config.Separator
	escapedPrefix1 := "$" + prefix // e.g. $${ or $@[
	var escapedPrefix2 string
	if len(prefix) > 0 {
		escapedPrefix2 = string(prefix[0]) + prefix // e.g. @@[ or $${
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

	addBuiltin("flyway:defaultSchema", builtins.DefaultSchema)
	addBuiltin("noway:defaultSchema", builtins.DefaultSchema)
	addBuiltin("flyway:table", builtins.Table)
	addBuiltin("noway:table", builtins.Table)
	addBuiltin("flyway:user", builtins.User)
	addBuiltin("noway:user", builtins.User)
	addBuiltin("flyway:database", builtins.Database)
	addBuiltin("noway:database", builtins.Database)

	ts := builtins.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	formattedTs := ts.Format("2006-01-02 15:04:05")
	addBuiltin("flyway:timestamp", formattedTs)
	addBuiltin("noway:timestamp", formattedTs)

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
		// Check for doubled prefix escaping or $ escaping: e.g. $${...} -> ${...}, @@[...] -> @[...]
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

		// Check for standard placeholder prefix: ${...}
		if strings.HasPrefix(input[i:], prefix) {
			suffixIdx := strings.Index(input[i+len(prefix):], suffix)
			if suffixIdx != -1 {
				placeholderExpr := input[i+len(prefix) : i+len(prefix)+suffixIdx]
				if val, ok := lookupValue(placeholderExpr); ok {
					sb.WriteString(val)
					i += len(prefix) + suffixIdx + len(suffix)
					continue
				}

				var key, defaultVal string
				hasDefault := false

				if sep != "" && strings.Contains(placeholderExpr, sep) {
					parts := strings.SplitN(placeholderExpr, sep, 2)
					key = parts[0]
					defaultVal = parts[1]
					hasDefault = true
				} else {
					key = placeholderExpr
				}

				if val, ok := lookupValue(key); ok {
					sb.WriteString(val)
					i += len(prefix) + suffixIdx + len(suffix)
					continue
				} else if hasDefault {
					sb.WriteString(defaultVal)
					i += len(prefix) + suffixIdx + len(suffix)
					continue
				} else {
					return "", fmt.Errorf("no value provided for placeholder: %s%s%s. Check your configuration or migration script", prefix, key, suffix)
				}
			}
		}

		sb.WriteByte(input[i])
		i++
	}

	return sb.String(), nil
}
