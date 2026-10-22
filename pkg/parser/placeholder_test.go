package parser

import (
	"strings"
	"testing"
)

// TestPlaceholderReplacerEscapes_Regression verifies preservation of escaped
// placeholders ($${var}) when the variable is defined or undefined (F22).
func TestPlaceholderReplacerEscapes_Regression(t *testing.T) {
	cfg := PlaceholderConfig{
		Enabled:   true,
		Prefix:    "${",
		Suffix:    "}",
		Separator: ":",
		Values: map[string]string{
			"env":   "production",
			"table": "customers",
			"count": "42",
		},
	}
	replacer := NewPlaceholderReplacer(cfg)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "escaped placeholder whose variable IS defined in config",
			input:    "SELECT '$${env}', '${env}';",
			expected: "SELECT '${env}', 'production';",
		},
		{
			name:     "escaped placeholder whose variable is NOT defined",
			input:    "SELECT '$${not_defined}';",
			expected: "SELECT '${not_defined}';",
		},
		{
			name:     "consecutive escaped placeholders",
			input:    "PREFIX=$${env}$${table}_SUFFIX",
			expected: "PREFIX=${env}${table}_SUFFIX",
		},
		{
			name:     "mixed escaped and unescaped adjacent",
			input:    "${table}:$${table}:${env}:$${env}",
			expected: "customers:${table}:production:${env}",
		},
		{
			name:     "escaped placeholder with default value syntax",
			input:    "DEFAULT=$${missing:fallback}",
			expected: "DEFAULT=${missing:fallback}",
		},
		{
			name:     "case insensitive placeholder evaluation while escaped remains intact",
			input:    "SELECT '${ENV}', '$${ENV}', '${Table}';",
			expected: "SELECT 'production', '${ENV}', 'customers';",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := replacer.Replace(tt.input, BuiltinPlaceholders{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res != tt.expected {
				t.Errorf("got %q, want %q", res, tt.expected)
			}
		})
	}
}

// TestPlaceholderReplacerCustomPrefixDoubled_Regression verifies custom prefix
// escaping with doubled prefix (@@[) or dollar ($@[).
func TestPlaceholderReplacerCustomPrefixDoubled_Regression(t *testing.T) {
	cfg := PlaceholderConfig{
		Enabled:   true,
		Prefix:    "@[",
		Suffix:    "]",
		Separator: ":",
		Values: map[string]string{
			"env":   "staging",
			"table": "orders",
		},
	}
	replacer := NewPlaceholderReplacer(cfg)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "doubled custom prefix escaping @@[var]",
			input:    "SELECT '@@[env]', '@[env]';",
			expected: "SELECT '@[env]', 'staging';",
		},
		{
			name:     "dollar custom prefix escaping $@[var]",
			input:    "SELECT '$@[table]', '@[table]';",
			expected: "SELECT '@[table]', 'orders';",
		},
		{
			name:     "doubled custom prefix when variable is undefined",
			input:    "SELECT '@@[undefined_var]';",
			expected: "SELECT '@[undefined_var]';",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := replacer.Replace(tt.input, BuiltinPlaceholders{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res != tt.expected {
				t.Errorf("got %q, want %q", res, tt.expected)
			}
		})
	}
}

// TestPlaceholderReplacerDisabled_Regression verifies behavior when replacement is disabled globally.
func TestPlaceholderReplacerDisabled_Regression(t *testing.T) {
	cfg := PlaceholderConfig{
		Enabled: false,
		Prefix:  "${",
		Suffix:  "}",
		Values: map[string]string{
			"env": "production",
		},
	}
	replacer := NewPlaceholderReplacer(cfg)

	if replacer.IsEnabled() {
		t.Errorf("expected IsEnabled to return false")
	}

	input := "SELECT '${env}', '${missing}';"
	// Replace() returns input verbatim without error
	res, err := replacer.Replace(input, BuiltinPlaceholders{})
	if err != nil {
		t.Fatalf("unexpected error from Replace: %v", err)
	}
	if res != input {
		t.Errorf("expected %q, got %q", input, res)
	}

	// ReplaceContent() executes replacement unconditionally
	resContent, err := replacer.ReplaceContent("SELECT '${env}';", BuiltinPlaceholders{})
	if err != nil {
		t.Fatalf("unexpected error from ReplaceContent: %v", err)
	}
	expected := "SELECT 'production';"
	if resContent != expected {
		t.Errorf("expected %q, got %q", expected, resContent)
	}
}

func TestPlaceholderBuiltinFilenameAndWorkingDirectory(t *testing.T) {
	cfg := PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
	}
	replacer := NewPlaceholderReplacer(cfg)

	builtins := BuiltinPlaceholders{
		Filename:         "V1.0__init.sql",
		WorkingDirectory: "/app/migrations",
	}

	tests := []struct {
		input    string
		expected string
	}{
		{"SELECT '${flyway:filename}'", "SELECT 'V1.0__init.sql'"},
		{"SELECT '${noway:filename}'", "SELECT 'V1.0__init.sql'"},
		{"SELECT '${flyway:workingDirectory}'", "SELECT '/app/migrations'"},
		{"SELECT '${noway:workingDirectory}'", "SELECT '/app/migrations'"},
	}

	for _, tt := range tests {
		res, err := replacer.Replace(tt.input, builtins)
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", tt.input, err)
		}
		if res != tt.expected {
			t.Errorf("got %q, want %q", res, tt.expected)
		}
	}
}

func TestPlaceholderUndefinedFailsWithoutDefault(t *testing.T) {
	cfg := PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
	}
	replacer := NewPlaceholderReplacer(cfg)

	tests := []string{
		"SELECT '${missing:x}'",
		"SELECT '${foo:bar}'",
		"SELECT '${undefined}'",
	}

	for _, input := range tests {
		_, err := replacer.Replace(input, BuiltinPlaceholders{})
		if err == nil {
			t.Fatalf("expected error for undefined placeholder %q, got nil", input)
		}
		if !strings.Contains(err.Error(), "no value provided for placeholder") {
			t.Errorf("expected error mentioning no value provided for placeholder, got: %v", err)
		}
	}
}
