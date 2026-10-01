package resolver

import (
	"testing"
	"testing/fstest"
)

func TestResolverFS(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/V1.1__add_col.sql": &fstest.MapFile{
			Data: []byte("ALTER TABLE users ADD COLUMN name STRING;"),
		},
		"migrations/R__views.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW user_view AS SELECT * FROM users;"),
		},
		"migrations/U1.1__undo_add_col.sql": &fstest.MapFile{
			Data: []byte("ALTER TABLE users DROP COLUMN name;"),
		},
		"migrations/beforeMigrate.sql": &fstest.MapFile{
			Data: []byte("-- before migrate hook"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if len(resolved.VersionedMigrations) != 2 {
		t.Errorf("expected 2 versioned migrations, got %d", len(resolved.VersionedMigrations))
	}
	if resolved.VersionedMigrations[0].Version.String() != "1" {
		t.Errorf("expected V1 first, got %s", resolved.VersionedMigrations[0].Version.String())
	}
	if resolved.VersionedMigrations[1].Version.String() != "1.1" {
		t.Errorf("expected V1.1 second, got %s", resolved.VersionedMigrations[1].Version.String())
	}

	if len(resolved.RepeatableMigrations) != 1 {
		t.Errorf("expected 1 repeatable migration, got %d", len(resolved.RepeatableMigrations))
	}
	if resolved.RepeatableMigrations[0].Description != "views" {
		t.Errorf("expected desc 'views', got %s", resolved.RepeatableMigrations[0].Description)
	}

	if len(resolved.UndoMigrations) != 1 {
		t.Errorf("expected 1 undo migration, got %d", len(resolved.UndoMigrations))
	}

	if len(resolved.Callbacks["beforeMigrate"]) != 1 {
		t.Errorf("expected beforeMigrate callback")
	}
}

func TestDuplicateVersions(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/V1_0__duplicate.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users2 (id INT64);"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	_, err := res.Resolve()
	if err == nil {
		t.Errorf("expected error for duplicate versions (1 and 1.0), got nil")
	}
}
