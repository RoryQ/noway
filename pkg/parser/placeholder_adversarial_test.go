package parser

import (
	"strings"
	"testing"
)

func TestAdversarial_EscapedVsUnescaped(t *testing.T) {
	cfg := PlaceholderConfig{
		Enabled:   true,
		Prefix:    "${",
		Suffix:    "}",
		Separator: ":",
		Values: map[string]string{
			"var":   "resolved_val",
			"inner": "target",
			"target": "nested_result",
		},
	}
	replacer := NewPlaceholderReplacer(cfg)

	tests := []struct {
		name        string
		input       string
		expected    string
		expectError bool
		errContains string
	}{
		{
			name:     "escaped placeholder with variable defined",
			input:    "SELECT '$${var}';",
			expected: "SELECT '${var}';",
		},
		{
			name:     "unescaped placeholder with variable defined",
			input:    "SELECT '${var}';",
			expected: "SELECT 'resolved_val';",
		},
		{
			name:     "escaped placeholder with variable UNDEFINED",
			input:    "SELECT '$${non_existent_var}';",
			expected: "SELECT '${non_existent_var}';",
		},
		{
			name:        "unescaped placeholder with variable UNDEFINED",
			input:       "SELECT '${non_existent_var}';",
			expectError: true,
			errContains: "no value provided for placeholder: ${non_existent_var}",
		},
		{
			name:     "escaped placeholder with default value syntax when undefined",
			input:    "$${missing:default_value}",
			expected: "${missing:default_value}",
		},
		{
			name:     "unescaped placeholder with default value syntax when undefined",
			input:    "${missing:default_value}",
			expected: "default_value",
		},
		{
			name:     "escaped placeholder with default value syntax when defined",
			input:    "$${var:fallback}",
			expected: "${var:fallback}",
		},
		{
			name:     "unescaped placeholder with default value syntax when defined",
			input:    "${var:fallback}",
			expected: "resolved_val",
		},
		{
			name:     "mixed adjacent: unescaped, escaped, unescaped",
			input:    "${var}:$${var}:${var}",
			expected: "resolved_val:${var}:resolved_val",
		},
		{
			name:     "consecutive escaped placeholders",
			input:    "$${var}$${var}$${missing}",
			expected: "${var}${var}${missing}",
		},
		{
			name:     "incomplete escaped placeholder without closing suffix",
			input:    "prefix $${var_no_closing suffix",
			expected: "prefix ${var_no_closing suffix",
		},
		{
			name:     "triple dollar before placeholder",
			input:    "$$${var}",
			expected: "$${var}",
		},
		{
			name:     "quadruple dollar before placeholder",
			input:    "$$$${var}",
			expected: "$$${var}",
		},
		{
			name:     "nested placeholder: escaped outer with unescaped inner",
			input:    "$${${inner}}",
			expected: "${target}",
		},
		{
			name:     "case insensitivity preserves escaped casing exactly",
			input:    "SELECT '${VAR}', '$${VAR}', '${Var}', '$${vAr}';",
			expected: "SELECT 'resolved_val', '${VAR}', 'resolved_val', '${vAr}';",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "single dollar",
			input:    "$",
			expected: "$",
		},
		{
			name:     "double dollar without prefix",
			input:    "$$",
			expected: "$$",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := replacer.Replace(tt.input, BuiltinPlaceholders{})
			if tt.expectError {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errContains)
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res != tt.expected {
				t.Fatalf("got %q, want %q", res, tt.expected)
			}
		})
	}
}

func TestAdversarial_DoubledPrefixEscaping(t *testing.T) {
	t.Run("bracket style prefix @[ and suffix ]", func(t *testing.T) {
		cfg := PlaceholderConfig{
			Enabled:   true,
			Prefix:    "@[",
			Suffix:    "]",
			Separator: ":",
			Values: map[string]string{
				"env": "production",
			},
		}
		replacer := NewPlaceholderReplacer(cfg)

		tests := []struct {
			input       string
			expected    string
			expectError bool
		}{
			{"@@[env]", "@[env]", false},
			{"@[env]", "production", false},
			{"@@[not_defined]", "@[not_defined]", false},
			{"$@[env]", "@[env]", false},
			{"$@[not_defined]", "@[not_defined]", false},
			{"@[not_defined]", "", true},
			{"@@[not_defined:fallback]", "@[not_defined:fallback]", false},
			{"@[not_defined:fallback]", "fallback", false},
			{"@@[env]@[env]$@[env]", "@[env]production@[env]", false},
		}

		for _, tt := range tests {
			res, err := replacer.Replace(tt.input, BuiltinPlaceholders{})
			if tt.expectError {
				if err == nil {
					t.Errorf("input %q: expected error, got nil", tt.input)
				}
			} else {
				if err != nil {
					t.Errorf("input %q: unexpected error: %v", tt.input, err)
				}
				if res != tt.expected {
					t.Errorf("input %q: got %q, want %q", tt.input, res, tt.expected)
				}
			}
		}
	})

	t.Run("hash style prefix #{ and suffix }", func(t *testing.T) {
		cfg := PlaceholderConfig{
			Enabled:   true,
			Prefix:    "#{",
			Suffix:    "}",
			Separator: ":",
			Values: map[string]string{
				"schema": "my_dataset",
			},
		}
		replacer := NewPlaceholderReplacer(cfg)

		tests := []struct {
			input       string
			expected    string
			expectError bool
		}{
			{"##{schema}", "#{schema}", false},
			{"#{schema}", "my_dataset", false},
			{"##{missing}", "#{missing}", false},
			{"$#{schema}", "#{schema}", false},
			{"$#{missing}", "#{missing}", false},
			{"#{missing}", "", true},
		}

		for _, tt := range tests {
			res, err := replacer.Replace(tt.input, BuiltinPlaceholders{})
			if tt.expectError {
				if err == nil {
					t.Errorf("input %q: expected error, got nil", tt.input)
				}
			} else {
				if err != nil {
					t.Errorf("input %q: unexpected error: %v", tt.input, err)
				}
				if res != tt.expected {
					t.Errorf("input %q: got %q, want %q", tt.input, res, tt.expected)
				}
			}
		}
	})

	t.Run("delimiter prefix @ and suffix @", func(t *testing.T) {
		cfg := PlaceholderConfig{
			Enabled:   true,
			Prefix:    "@",
			Suffix:    "@",
			Separator: ":",
			Values: map[string]string{
				"tbl": "orders",
			},
		}
		replacer := NewPlaceholderReplacer(cfg)

		tests := []struct {
			input       string
			expected    string
			expectError bool
		}{
			{"@@tbl@@", "@tbl@", false},
			{"@tbl@", "orders", false},
			{"$@tbl@", "@tbl@", false},
			{"@missing@", "", true},
			{"@@missing@@", "@missing@", false},
		}

		for _, tt := range tests {
			res, err := replacer.Replace(tt.input, BuiltinPlaceholders{})
			if tt.expectError {
				if err == nil {
					t.Errorf("input %q: expected error, got nil", tt.input)
				}
			} else {
				if err != nil {
					t.Errorf("input %q: unexpected error: %v", tt.input, err)
				}
				if res != tt.expected {
					t.Errorf("input %q: got %q, want %q", tt.input, res, tt.expected)
				}
			}
		}
	})
}
