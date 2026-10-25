package database_test

import (
	"context"
	"strings"
	"testing"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
	"github.com/roryq/noway/pkg/database/bigquery"
	"github.com/roryq/noway/pkg/database/spanner"
)

func TestCredentialOptions(t *testing.T) {
	// 1. OAuthType=0 requires key file or JSON
	cfg0 := config.NewDefaultConfiguration()
	o0 := 0
	cfg0.GCPOAuthType = &o0
	_, err := database.CredentialOptions(cfg0)
	if err == nil || !strings.Contains(err.Error(), "OAuthType=0 requires") {
		t.Fatalf("expected error for missing OAuthType=0 key, got %v", err)
	}

	cfg0.GCPCredentialsFile = "/path/to/key.json"
	opts0, err := database.CredentialOptions(cfg0)
	if err != nil || len(opts0) != 1 {
		t.Fatalf("expected 1 option for OAuthType=0 with file, got %v (err: %v)", opts0, err)
	}

	cfg0.GCPCredentialsFile = ""
	cfg0.GCPCredentialsJSON = `{"type": "service_account"}`
	opts0JSON, err := database.CredentialOptions(cfg0)
	if err != nil || len(opts0JSON) != 1 {
		t.Fatalf("expected 1 option for OAuthType=0 with JSON, got %v (err: %v)", opts0JSON, err)
	}

	// 2. OAuthType=2 requires access token
	cfg2 := config.NewDefaultConfiguration()
	o2 := 2
	cfg2.GCPOAuthType = &o2
	_, err = database.CredentialOptions(cfg2)
	if err == nil || !strings.Contains(err.Error(), "OAuthType=2 requires OAuthAccessToken") {
		t.Fatalf("expected error for missing access token, got %v", err)
	}

	cfg2.GCPAccessToken = "secret-token"
	opts2, err := database.CredentialOptions(cfg2)
	if err != nil || len(opts2) != 1 {
		t.Fatalf("expected 1 option for OAuthType=2 with token, got %v (err: %v)", opts2, err)
	}

	// 3. OAuthType=3 (ADC) rejects explicit credentials
	cfg3 := config.NewDefaultConfiguration()
	o3 := 3
	cfg3.GCPOAuthType = &o3
	opts3, err := database.CredentialOptions(cfg3)
	if err != nil || len(opts3) != 0 {
		t.Fatalf("expected 0 options for clean OAuthType=3, got %v (err: %v)", opts3, err)
	}

	cfg3.GCPCredentialsFile = "/path/to/key.json"
	_, err = database.CredentialOptions(cfg3)
	if err == nil || !strings.Contains(err.Error(), "OAuthType=3 uses Application Default Credentials; remove") {
		t.Fatalf("expected error when OAuthType=3 has explicit credentials, got %v", err)
	}

	// 4. Default without OAuthType: explicit key file implies OAuthType=0
	cfgDefault := config.NewDefaultConfiguration()
	cfgDefault.GCPCredentialsFile = "/path/to/key.json"
	optsDef, err := database.CredentialOptions(cfgDefault)
	if err != nil || len(optsDef) != 1 {
		t.Fatalf("expected explicit credentials without OAuthType to resolve as OAuthType=0, got %v (err: %v)", optsDef, err)
	}

	// 5. Unsupported OAuthType
	cfgUnsupported := config.NewDefaultConfiguration()
	badType := 99
	cfgUnsupported.GCPOAuthType = &badType
	_, err = database.CredentialOptions(cfgUnsupported)
	if err == nil || !strings.Contains(err.Error(), "OAuthType=99 is not supported") {
		t.Fatalf("expected error for unsupported OAuthType, got %v", err)
	}
}

func TestSpannerAndBigQuery_DriverCredentialOptionsParity(t *testing.T) {
	ctx := context.Background()

	// Both drivers fail with missing access token when OAuthType=2
	cfg := config.NewDefaultConfiguration()
	cfg.GCPProjectID = "test-proj"
	cfg.GCPSpannerInstanceID = "test-inst"
	cfg.GCPSpannerDatabaseID = "test-db"
	o2 := 2
	cfg.GCPOAuthType = &o2

	_, errSpanner := spanner.New(ctx, cfg)
	if errSpanner == nil || !strings.Contains(errSpanner.Error(), "OAuthType=2 requires OAuthAccessToken") {
		t.Errorf("expected Spanner to reject OAuthType=2 without token, got: %v", errSpanner)
	}

	_, errBQ := bigquery.New(ctx, cfg)
	if errBQ == nil || !strings.Contains(errBQ.Error(), "OAuthType=2 requires OAuthAccessToken") {
		t.Errorf("expected BigQuery to reject OAuthType=2 without token, got: %v", errBQ)
	}

	// Both drivers reject explicit credentials under OAuthType=3
	o3 := 3
	cfg.GCPOAuthType = &o3
	cfg.GCPCredentialsFile = "/path/to/key.json"

	_, errSpannerADC := spanner.New(ctx, cfg)
	if errSpannerADC == nil || !strings.Contains(errSpannerADC.Error(), "OAuthType=3 uses Application Default Credentials; remove") {
		t.Errorf("expected Spanner to reject OAuthType=3 with credentials file, got: %v", errSpannerADC)
	}

	_, errBQADC := bigquery.New(ctx, cfg)
	if errBQADC == nil || !strings.Contains(errBQADC.Error(), "OAuthType=3 uses Application Default Credentials; remove") {
		t.Errorf("expected BigQuery to reject OAuthType=3 with credentials file, got: %v", errBQADC)
	}
}

func TestCredentialOptions_GoogleApplicationCredentialsEnv(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/secrets/sa.json")

	cfg := config.NewDefaultConfiguration()
	cfg.URL = "jdbc:bigquery://https://www.googleapis.com/bigquery/v2:443;ProjectId=p;OAuthType=3;DefaultDataset=ds"
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}

	opts, err := database.CredentialOptions(cfg)
	if err != nil {
		t.Fatalf("expected OAuthType=3 with GOOGLE_APPLICATION_CREDENTIALS to succeed, got: %v", err)
	}
	if len(opts) != 0 {
		t.Fatalf("expected 0 options for ADC, got %d", len(opts))
	}

	// But if gcpCredentialsFile is explicitly set, OAuthType=3 must fail
	cfg.GCPCredentialsFile = "/explicit/path.json"
	_, err = database.CredentialOptions(cfg)
	if err == nil || !strings.Contains(err.Error(), "OAuthType=3 uses Application Default Credentials; remove") {
		t.Fatalf("expected error when explicit credentials file is set with OAuthType=3, got: %v", err)
	}
}

func TestPlainHTTPEndpointWithoutEmulatorFails(t *testing.T) {
	ctx := context.Background()

	// 1. BigQuery rejects plain HTTP when emulator=false
	cfgBQ := config.NewDefaultConfiguration()
	cfgBQ.GCPProjectID = "test-project"
	cfgBQ.GCPBigQueryEndpoint = "http://localhost:4588/bigquery/v2/"
	cfgBQ.GCPUseEmulator = false

	_, errBQ := bigquery.New(ctx, cfgBQ)
	if errBQ == nil || !strings.Contains(errBQ.Error(), "uses plain HTTP; set emulator=true") {
		t.Fatalf("expected BigQuery driver to reject plain http endpoint without emulator, got: %v", errBQ)
	}

	// 2. Spanner rejects plain HTTP when emulator=false
	cfgSpanner := config.NewDefaultConfiguration()
	cfgSpanner.GCPProjectID = "test-project"
	cfgSpanner.GCPSpannerInstanceID = "test-instance"
	cfgSpanner.GCPSpannerDatabaseID = "test-db"
	cfgSpanner.GCPSpannerEndpoint = "http://localhost:9010"
	cfgSpanner.GCPUseEmulator = false

	_, errSpanner := spanner.New(ctx, cfgSpanner)
	if errSpanner == nil || !strings.Contains(errSpanner.Error(), "uses plain HTTP; set emulator=true") {
		t.Fatalf("expected Spanner driver to reject plain http endpoint without emulator, got: %v", errSpanner)
	}
}
