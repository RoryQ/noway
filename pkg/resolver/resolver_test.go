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
		"migrations/V2.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE logs (id INT64);"),
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

	if len(resolved.VersionedMigrations) != 3 {
		t.Errorf("expected 3 versioned migrations, got %d", len(resolved.VersionedMigrations))
	}
	if resolved.VersionedMigrations[0].Version.String() != "1" {
		t.Errorf("expected V1 first, got %s", resolved.VersionedMigrations[0].Version.String())
	}
	if resolved.VersionedMigrations[1].Version.String() != "1.1" {
		t.Errorf("expected V1.1 second, got %s", resolved.VersionedMigrations[1].Version.String())
	}
	if resolved.VersionedMigrations[2].Version.String() != "2" || resolved.VersionedMigrations[2].Description != "" {
		t.Errorf("expected V2 with empty description, got %+v", resolved.VersionedMigrations[2])
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

func TestResolverNonMigrationFiles(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/Views.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW v AS SELECT 1;"),
		},
		"migrations/Validate.sql": &fstest.MapFile{
			Data: []byte("SELECT 1;"),
		},
	}

	res := NewResolver(ResolverConfig{
		FS:        mockFS,
		Locations: []string{"migrations"},
	})

	resolved, err := res.Resolve()
	if err != nil {
		t.Fatalf("expected Resolve to succeed ignoring non-migration files, got: %v", err)
	}

	if len(resolved.VersionedMigrations) != 1 {
		t.Errorf("expected 1 versioned migration, got %d", len(resolved.VersionedMigrations))
	}
}

func TestResolverScriptMigrationsAndSqlConf(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64);"),
		},
		"migrations/V1__init.sql.conf": &fstest.MapFile{
			Data: []byte("shouldExecute=${env} == 'production'\nexecuteInTransaction=false"),
		},
		"migrations/V2__load_data.sh": &fstest.MapFile{
			Data: []byte("#!/bin/bash\necho 'Loading BigQuery seed data'"),
		},
		"migrations/V2__load_data.sh.conf": &fstest.MapFile{
			Data: []byte("shouldExecute=true"),
		},
		"migrations/R__refresh_cache.bash": &fstest.MapFile{
			Data: []byte("#!/bin/bash\necho 'Refreshing cache'"),
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
		t.Fatalf("expected 2 versioned migrations, got %d", len(resolved.VersionedMigrations))
	}

	// Check V1 (SQL)
	v1 := resolved.VersionedMigrations[0]
	if v1.Type != TypeSQL || v1.IsScript {
		t.Errorf("expected V1 to be TypeSQL, got %s (isScript=%v)", v1.Type, v1.IsScript)
	}
	if v1.Config.ShouldExecute != "${env} == 'production'" {
		t.Errorf("expected V1 shouldExecute '${env} == \\'production\\'', got %q", v1.Config.ShouldExecute)
	}
	if v1.Config.ExecuteInTransaction == nil || *v1.Config.ExecuteInTransaction != false {
		t.Errorf("expected V1 executeInTransaction to be false, got %v", v1.Config.ExecuteInTransaction)
	}

	// Check V2 (Bash Script)
	v2 := resolved.VersionedMigrations[1]
	if v2.Type != TypeScript || !v2.IsScript {
		t.Errorf("expected V2 to be TypeScript, got %s (isScript=%v)", v2.Type, v2.IsScript)
	}
	if v2.Config.ShouldExecute != "true" {
		t.Errorf("expected V2 shouldExecute 'true', got %q", v2.Config.ShouldExecute)
	}

	// Check Repeatable Bash script
	if len(resolved.RepeatableMigrations) != 1 {
		t.Fatalf("expected 1 repeatable migration, got %d", len(resolved.RepeatableMigrations))
	}
	r1 := resolved.RepeatableMigrations[0]
	if r1.Type != TypeScript || !r1.IsScript {
		t.Errorf("expected repeatable to be TypeScript, got %s", r1.Type)
	}
}
