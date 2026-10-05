package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/roryq/noway/pkg/parser"
)

func TestAdversarial_EnvPlaceholderVariations(t *testing.T) {
	// Set up distinct environment variables covering FLYWAY_ and NOWAY_, singular and plural, upper/lower/mixed
	t.Setenv("FLYWAY_PLACEHOLDERS_UPPER_PLURAL", "v1")
	t.Setenv("FLYWAY_PLACEHOLDER_UPPER_SINGULAR", "v2")
	t.Setenv("NOWAY_PLACEHOLDERS_NOWAY_PLURAL", "v3")
	t.Setenv("NOWAY_PLACEHOLDER_NOWAY_SINGULAR", "v4")

	t.Setenv("flyway_placeholders_lower_plural", "v5")
	t.Setenv("flyway_placeholder_lower_singular", "v6")
	t.Setenv("noway_placeholders_noway_lower_plural", "v7")
	t.Setenv("noway_placeholder_noway_lower_singular", "v8")

	t.Setenv("Flyway_Placeholders_Mixed_Plural", "v9")
	t.Setenv("Noway_Placeholder_Mixed_Singular", "v10")

	t.Setenv("FLYWAY_PLACEHOLDERS_EMPTY_VAL", "")
	t.Setenv("FLYWAY_PLACEHOLDERS_COMPLEX_VAL", "a=b:c d;e'f\"g")

	cfg := NewDefaultConfiguration()
	LoadFromEnv(cfg)

	checkKey := func(key, expected string) {
		val, ok := cfg.Placeholders[key]
		if !ok {
			t.Errorf("key %q not found in cfg.Placeholders", key)
			return
		}
		if val != expected {
			t.Errorf("key %q: got %q, want %q", key, val, expected)
		}
	}

	checkKey("UPPER_PLURAL", "v1")
	checkKey("UPPER_SINGULAR", "v2")
	checkKey("NOWAY_PLURAL", "v3")
	checkKey("NOWAY_SINGULAR", "v4")
	checkKey("lower_plural", "v5")
	checkKey("lower_singular", "v6")
	checkKey("noway_lower_plural", "v7")
	checkKey("noway_lower_singular", "v8")
	checkKey("Mixed_Plural", "v9")
	checkKey("Mixed_Singular", "v10")
	checkKey("EMPTY_VAL", "")
	checkKey("COMPLEX_VAL", "a=b:c d;e'f\"g")

	// Now test replacer with these placeholders: case-insensitive matching in SQL
	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled:   cfg.PlaceholderReplacement,
		Prefix:    cfg.PlaceholderPrefix,
		Suffix:    cfg.PlaceholderSuffix,
		Separator: cfg.PlaceholderSeparator,
		Values:    cfg.Placeholders,
	})

	sql := "SELECT '${upper_plural}', '${UPPER_SINGULAR}', '${NOWAY_plural}', '${noway_singular}', '${LOWER_PLURAL}', '${mixed_plural}', '${empty_val}', '${complex_val}';"
	replaced, err := replacer.Replace(sql, parser.BuiltinPlaceholders{})
	if err != nil {
		t.Fatalf("unexpected error replacing placeholders: %v", err)
	}

	expectedReplaced := "SELECT 'v1', 'v2', 'v3', 'v4', 'v5', 'v9', '', 'a=b:c d;e'f\"g';"
	if replaced != expectedReplaced {
		t.Fatalf("got %q, want %q", replaced, expectedReplaced)
	}
}

func TestAdversarial_NowayEnvPrefixMapping(t *testing.T) {
	t.Setenv("NOWAY_PLACEHOLDERS_FOO_PLURAL", "noway_foo")
	t.Setenv("NOWAY_PLACEHOLDER_BAR_SINGULAR", "noway_bar")

	cfg := NewDefaultConfiguration()
	LoadFromEnv(cfg)

	if cfg.Placeholders["FOO_PLURAL"] != "noway_foo" {
		t.Errorf("expected FOO_PLURAL 'noway_foo', got %q", cfg.Placeholders["FOO_PLURAL"])
	}
	if cfg.Placeholders["BAR_SINGULAR"] != "noway_bar" {
		t.Errorf("expected BAR_SINGULAR 'noway_bar', got %q", cfg.Placeholders["BAR_SINGULAR"])
	}

	replacer := parser.NewPlaceholderReplacer(parser.PlaceholderConfig{
		Enabled: true,
		Prefix:  "${",
		Suffix:  "}",
		Values:  cfg.Placeholders,
	})

	res, err := replacer.Replace("SELECT '${foo_plural}', '${Bar_Singular}';", parser.BuiltinPlaceholders{})
	if err != nil {
		t.Fatalf("unexpected replace error: %v", err)
	}
	if res != "SELECT 'noway_foo', 'noway_bar';" {
		t.Fatalf("got %q, want \"SELECT 'noway_foo', 'noway_bar';\"", res)
	}
}

func TestAdversarial_PropertiesFileVariations(t *testing.T) {
	conf := `
# Diverse property key prefixes and casings
flyway.placeholders.key1=val1
flyway.placeholder.key2=val2
noway.placeholders.key3=val3
noway.placeholder.key4=val4
placeholders.key5=val5
placeholder.key6=val6
Flyway.Placeholders.Key7=val7
NOWAY.PLACEHOLDER.Key8=val8
flyway.placeholders.quotedSingle='single quoted'
flyway.placeholders.quotedDouble="double quoted"
`
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "flyway.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0644); err != nil {
		t.Fatalf("failed to write conf: %v", err)
	}

	cfg, err := LoadFromFile(confPath)
	if err != nil {
		t.Fatalf("failed to load conf: %v", err)
	}

	expectedKeys := map[string]string{
		"key1":         "val1",
		"key2":         "val2",
		"key3":         "val3",
		"key4":         "val4",
		"key5":         "val5",
		"key6":         "val6",
		"Key7":         "val7",
		"Key8":         "val8",
		"quotedSingle": "single quoted",
		"quotedDouble": "double quoted",
	}

	for k, expectedVal := range expectedKeys {
		actualVal, ok := cfg.Placeholders[k]
		if !ok {
			t.Errorf("expected key %q in cfg.Placeholders", k)
			continue
		}
		if actualVal != expectedVal {
			t.Errorf("key %q: got %q, want %q", k, actualVal, expectedVal)
		}
	}
}
