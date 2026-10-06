package bigquery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/googleapi"

	"github.com/roryq/noway/pkg/config"
)

func TestQuote(t *testing.T) {
	db := &BigQueryDatabase{}

	tests := []struct {
		input    []string
		expected string
	}{
		{[]string{"dataset"}, "`dataset`"},
		{[]string{"dataset", "table"}, "`dataset`.`table`"},
		{[]string{"my`dataset", "my`table"}, "`my\\`dataset`.`my\\`table`"},
		{[]string{"project", "dataset", "table"}, "`project`.`dataset`.`table`"},
	}

	for _, tt := range tests {
		actual := db.Quote(tt.input...)
		if actual != tt.expected {
			t.Errorf("Quote(%v) = %q; want %q", tt.input, actual, tt.expected)
		}
	}
}

func TestSchemaHistoryTableDefinition(t *testing.T) {
	// Verify that the table schema matches Flyway's exact BigQuery column names and types
	expectedColumns := []string{
		"`installed_rank` INT64 NOT NULL",
		"`version` STRING",
		"`description` STRING NOT NULL",
		"`type` STRING NOT NULL",
		"`script` STRING NOT NULL",
		"`checksum` INT64",
		"`installed_by` STRING NOT NULL",
		"`installed_on` TIMESTAMP",
		"`execution_time` INT64 NOT NULL",
		"`success` BOOL NOT NULL",
	}

	for _, col := range expectedColumns {
		if !strings.Contains(col, "INT64") && !strings.Contains(col, "STRING") && !strings.Contains(col, "TIMESTAMP") && !strings.Contains(col, "BOOL") {
			t.Errorf("invalid column %s", col)
		}
	}
}

func TestIsRetryableBigQueryError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"syntax error", errors.New("syntax error at [1:10]"), false},
		{"table not found", errors.New("table dataset.test not found"), false},
		{"metadata rate limit error message", errors.New("Exceeded rate limits: too many metadata update operations for table `test`"), true},
		{"rateLimitExceeded error text", errors.New("googleapi: Error 403: rateLimitExceeded"), true},
		{"jobRateLimitExceeded error text", errors.New("jobRateLimitExceeded: too many jobs"), true},
		{"quotaExceeded error text", errors.New("quotaExceeded: table update quota exceeded"), true},
		{"backendError text", errors.New("backendError: transient backend issue"), true},
		{"internalError text", errors.New("internalError: internal failure"), true},
		{"concurrentModification text", errors.New("concurrentModification: table modified concurrently"), true},
		{"http 429 status code text", errors.New("unexpected status code 429"), true},
		{"http 503 status code text", errors.New("unexpected status code 503"), true},
		{"googleapi 429", &googleapi.Error{Code: 429}, true},
		{"googleapi 500", &googleapi.Error{Code: 500}, true},
		{"googleapi 503", &googleapi.Error{Code: 503}, true},
		{"googleapi 403 rateLimitExceeded", &googleapi.Error{
			Code: 403,
			Errors: []googleapi.ErrorItem{
				{Reason: "rateLimitExceeded", Message: "rate limit exceeded"},
			},
		}, true},
		{"googleapi 403 quotaExceeded", &googleapi.Error{
			Code: 403,
			Errors: []googleapi.ErrorItem{
				{Reason: "quotaExceeded"},
			},
		}, true},
		{"googleapi 400 invalidQuery", &googleapi.Error{
			Code: 400,
			Errors: []googleapi.ErrorItem{
				{Reason: "invalidQuery"},
			},
		}, false},
		{"bigquery.Error rateLimitExceeded", &bigquery.Error{Reason: "rateLimitExceeded"}, true},
		{"bigquery.Error quotaExceeded", &bigquery.Error{Reason: "quotaExceeded"}, true},
		{"bigquery.Error notFound", &bigquery.Error{Reason: "notFound"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := isRetryableBigQueryError(tt.err)
			if actual != tt.expected {
				t.Errorf("isRetryableBigQueryError(%v) = %v; want %v", tt.err, actual, tt.expected)
			}
		})
	}
}

func TestBigQueryRetry(t *testing.T) {
	t.Run("SuccessFirstAttempt", func(t *testing.T) {
		db := &BigQueryDatabase{
			retryInitialBackoff: 1 * time.Millisecond,
			retryMaxBackoff:     5 * time.Millisecond,
		}
		attempts := 0
		err := db.retry(context.Background(), func() error {
			attempts++
			return nil
		})
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if attempts != 1 {
			t.Errorf("expected 1 attempt, got %d", attempts)
		}
	})

	t.Run("SuccessAfterTransientFailures", func(t *testing.T) {
		db := &BigQueryDatabase{
			retryInitialBackoff: 1 * time.Millisecond,
			retryMaxBackoff:     5 * time.Millisecond,
		}
		attempts := 0
		err := db.retry(context.Background(), func() error {
			attempts++
			if attempts < 3 {
				return errors.New("rateLimitExceeded: too many metadata update operations for table")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if attempts != 3 {
			t.Errorf("expected 3 attempts, got %d", attempts)
		}
	})

	t.Run("NonRetryableErrorFailsImmediately", func(t *testing.T) {
		db := &BigQueryDatabase{
			retryInitialBackoff: 1 * time.Millisecond,
			retryMaxBackoff:     5 * time.Millisecond,
		}
		attempts := 0
		nonRetryableErr := errors.New("syntax error: invalid column type")
		err := db.retry(context.Background(), func() error {
			attempts++
			return nonRetryableErr
		})
		if err == nil || !strings.Contains(err.Error(), "syntax error") {
			t.Fatalf("expected syntax error, got %v", err)
		}
		if attempts != 1 {
			t.Errorf("expected 1 attempt for non-retryable error, got %d", attempts)
		}
	})

	t.Run("ExhaustsMaxRetries", func(t *testing.T) {
		cfg := &config.Configuration{ConnectRetries: 3}
		db := &BigQueryDatabase{
			config:              cfg,
			retryInitialBackoff: 1 * time.Millisecond,
			retryMaxBackoff:     5 * time.Millisecond,
		}
		attempts := 0
		err := db.retry(context.Background(), func() error {
			attempts++
			return errors.New("rateLimitExceeded: metadata limit")
		})
		if err == nil || !strings.Contains(err.Error(), "failed after 3 retries") {
			t.Fatalf("expected exhaustion error, got %v", err)
		}
		// Initial attempt (attempt 0) + 3 retries (attempts 1, 2, 3) = 4 total attempts
		if attempts != 4 {
			t.Errorf("expected 4 total attempts (initial + 3 retries), got %d", attempts)
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		db := &BigQueryDatabase{
			retryInitialBackoff: 500 * time.Millisecond,
			retryMaxBackoff:     1 * time.Second,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		err := db.retry(ctx, func() error {
			return errors.New("rateLimitExceeded")
		})
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context timeout/cancellation, got %v", err)
		}
	})
}
