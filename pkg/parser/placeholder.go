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

// Replace substitutes all placeholders in the input SQL.
func (p *PlaceholderReplacer) Replace(input string, builtins BuiltinPlaceholders) (string, error) {
	if !p.config.Enabled {
		return input, nil
	}

	prefix := p.config.Prefix
	suffix := p.config.Suffix
	sep := p.config.Separator
	escapedPrefix := "$" + prefix // e.g. $${

	// Build combined placeholder map
	values := make(map[string]string)
	for k, v := range p.config.Values {
		values[k] = v
	}

	// Add builtins
	if builtins.DefaultSchema != "" {
		values["flyway:defaultSchema"] = builtins.DefaultSchema
		values["noway:defaultSchema"] = builtins.DefaultSchema
	}
	if builtins.Table != "" {
		values["flyway:table"] = builtins.Table
		values["noway:table"] = builtins.Table
	}
	if builtins.User != "" {
		values["flyway:user"] = builtins.User
		values["noway:user"] = builtins.User
	}
	if builtins.Database != "" {
		values["flyway:database"] = builtins.Database
		values["noway:database"] = builtins.Database
	}
	ts := builtins.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	formattedTs := ts.Format("2006-01-02 15:04:05")
	values["flyway:timestamp"] = formattedTs
	values["noway:timestamp"] = formattedTs

	var sb strings.Builder
	sb.Grow(len(input))
	i := 0
	for i < len(input) {
		// Check for escaped prefix: $${...} -> ${...}
		if strings.HasPrefix(input[i:], escapedPrefix) {
			sb.WriteString(prefix)
			i += len(escapedPrefix)
			continue
		}

		// Check for standard placeholder prefix: ${...}
		if strings.HasPrefix(input[i:], prefix) {
			suffixIdx := strings.Index(input[i+len(prefix):], suffix)
			if suffixIdx != -1 {
				placeholderExpr := input[i+len(prefix) : i+len(prefix)+suffixIdx]
				if val, ok := values[placeholderExpr]; ok {
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

				if val, ok := values[key]; ok {
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
