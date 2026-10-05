package resolver

import (
	"testing"

	"github.com/roryq/noway/pkg/parser"
)

func TestEvaluateShouldExecute(t *testing.T) {
	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
		Values: map[string]string{
			"env":       "production",
			"run_seed":  "false",
			"tier":      "3",
			"is_active": "true",
		},
	})
	builtins := parser.BuiltinPlaceholders{
		User:          "admin",
		DefaultSchema: "analytics",
	}

	tests := []struct {
		name     string
		expr     string
		expected bool
	}{
		{"empty is true", "", true},
		{"true literal", "true", true},
		{"false literal", "false", false},
		{"case insensitive TRUE", "TRUE", true},
		{"case insensitive False", "False", false},
		{"equality match", "'prod' == 'prod'", true},
		{"equality mismatch", "'prod' == 'dev'", false},
		{"inequality match", "'prod' != 'dev'", true},
		{"single equals", "'prod' = 'prod'", true},
		{"placeholder equality", "${env} == 'production'", true},
		{"placeholder mismatch", "${env} == 'staging'", false},
		{"placeholder boolean", "${is_active} == true", true},
		{"placeholder boolean false", "${run_seed} == true", false},
		{"numeric comparison greater", "${tier} > 1", true},
		{"numeric comparison less equal", "3 <= 3", true},
		{"numeric comparison less", "10 < 5", false},
		{"logical AND true", "${env} == 'production' && ${is_active} == true", true},
		{"logical AND false", "${env} == 'production' && ${run_seed} == true", false},
		{"logical OR true", "${env} == 'staging' || ${is_active} == true", true},
		{"logical OR false", "${env} == 'staging' || ${run_seed} == true", false},
		{"logical NOT", "!(${env} == 'staging')", true},
		{"parenthesized expression", "(${env} == 'production' || ${tier} == 1) && ${is_active} == true", true},
		{"builtin placeholder", "${flyway:user} == 'admin'", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := EvaluateShouldExecute(tt.expr, replacer, builtins)
			if err != nil {
				t.Fatalf("unexpected error evaluating '%s': %v", tt.expr, err)
			}
			if actual != tt.expected {
				t.Errorf("EvaluateShouldExecute(%q) = %v; want %v", tt.expr, actual, tt.expected)
			}
		})
	}
}
