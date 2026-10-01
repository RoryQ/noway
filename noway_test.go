package noway

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/RoryQ/noway/pkg/config"
	"github.com/RoryQ/noway/pkg/database/mock"
)

func TestNowayEndToEnd(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/V1__init.sql": &fstest.MapFile{
			Data: []byte("CREATE TABLE users (id INT64, email STRING);"),
		},
		"migrations/V2__add_index.sql": &fstest.MapFile{
			Data: []byte("ALTER TABLE users ADD COLUMN created_at TIMESTAMP;"),
		},
		"migrations/R__reporting_view.sql": &fstest.MapFile{
			Data: []byte("CREATE VIEW users_report AS SELECT id, email FROM users;"),
		},
	}

	db := mock.NewMockDatabase()
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "analytics"
	cfg.FS = mockFS
	cfg.Locations = []string{"migrations"}

	nw, err := New(
		WithConfig(cfg),
		WithDatabase(db),
	)
	if err != nil {
		t.Fatalf("failed to create noway: %v", err)
	}
	defer nw.Close()

	ctx := context.Background()

	// 1. Info before migrate
	infoBefore, err := nw.Info(ctx)
	if err != nil {
		t.Fatalf("Info before migrate error: %v", err)
	}
	if len(infoBefore.Migrations) != 3 {
		t.Errorf("expected 3 pending migrations in info, got %d", len(infoBefore.Migrations))
	}

	// 2. Migrate
	migRes, err := nw.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate error: %v", err)
	}
	if migRes.MigrationsExecuted != 3 {
		t.Errorf("expected 3 migrations applied, got %d", migRes.MigrationsExecuted)
	}

	// 3. Validate
	valRes, err := nw.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if !valRes.Valid {
		t.Errorf("expected Validate to pass, got errors: %v", valRes.Errors)
	}

	// 4. Info after migrate
	infoAfter, err := nw.Info(ctx)
	if err != nil {
		t.Fatalf("Info after migrate error: %v", err)
	}
	for _, m := range infoAfter.Migrations {
		if m.State != "Success" {
			t.Errorf("expected migration %s to be Success, got %s", m.Script, m.State)
		}
	}
}
