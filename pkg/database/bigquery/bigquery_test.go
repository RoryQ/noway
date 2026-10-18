package bigquery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

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

func TestPollJobStatus(t *testing.T) {
	t.Run("ImmediateDone", func(t *testing.T) {
		calls := 0
		status, err := pollJobStatus(context.Background(), 1*time.Millisecond, 5*time.Millisecond, func(ctx context.Context) (*bigquery.JobStatus, error) {
			calls++
			return &bigquery.JobStatus{State: bigquery.Done}, nil
		})
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !status.Done() {
			t.Errorf("expected status.Done() to be true")
		}
		if calls != 1 {
			t.Errorf("expected 1 call, got %d", calls)
		}
	})

	t.Run("RunningThenDone", func(t *testing.T) {
		calls := 0
		status, err := pollJobStatus(context.Background(), 1*time.Millisecond, 5*time.Millisecond, func(ctx context.Context) (*bigquery.JobStatus, error) {
			calls++
			if calls < 3 {
				return &bigquery.JobStatus{State: bigquery.Running}, nil
			}
			return &bigquery.JobStatus{State: bigquery.Done}, nil
		})
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !status.Done() {
			t.Errorf("expected status.Done() to be true")
		}
		if calls != 3 {
			t.Errorf("expected 3 calls, got %d", calls)
		}
	})

	t.Run("TransientStatusErrorRecovers", func(t *testing.T) {
		calls := 0
		status, err := pollJobStatus(context.Background(), 1*time.Millisecond, 5*time.Millisecond, func(ctx context.Context) (*bigquery.JobStatus, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("rateLimitExceeded: too many metadata update operations")
			}
			return &bigquery.JobStatus{State: bigquery.Done}, nil
		})
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !status.Done() {
			t.Errorf("expected status.Done() to be true")
		}
		if calls != 2 {
			t.Errorf("expected 2 calls, got %d", calls)
		}
	})

	t.Run("NonRetryableStatusErrorFailsImmediately", func(t *testing.T) {
		calls := 0
		_, err := pollJobStatus(context.Background(), 1*time.Millisecond, 5*time.Millisecond, func(ctx context.Context) (*bigquery.JobStatus, error) {
			calls++
			return nil, errors.New("notFound: job not found in BigQuery")
		})
		if err == nil || !strings.Contains(err.Error(), "notFound") {
			t.Fatalf("expected notFound error, got %v", err)
		}
		if calls != 1 {
			t.Errorf("expected 1 call for non-retryable error, got %d", calls)
		}
	})

	t.Run("ExhaustsStatusRetries", func(t *testing.T) {
		calls := 0
		_, err := pollJobStatus(context.Background(), 1*time.Millisecond, 5*time.Millisecond, func(ctx context.Context) (*bigquery.JobStatus, error) {
			calls++
			return nil, errors.New("rateLimitExceeded: quota reached")
		})
		if err == nil || !strings.Contains(err.Error(), "failed to get job status after 10 retries") {
			t.Fatalf("expected retries exhausted error, got %v", err)
		}
		if calls != 11 { // 1 initial + 10 retries
			t.Errorf("expected 11 calls, got %d", calls)
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()

		_, err := pollJobStatus(ctx, 200*time.Millisecond, 500*time.Millisecond, func(ctx context.Context) (*bigquery.JobStatus, error) {
			return &bigquery.JobStatus{State: bigquery.Running}, nil
		})
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context timeout/cancellation, got %v", err)
		}
	})
}

func setJobStatusError(s *bigquery.JobStatus, err error) {
	val := reflect.ValueOf(s).Elem().FieldByName("err")
	reflect.NewAt(val.Type(), unsafe.Pointer(val.UnsafeAddr())).Elem().Set(reflect.ValueOf(err))
}

func TestSubmitAndPollJob(t *testing.T) {
	db := &BigQueryDatabase{
		retryInitialBackoff: 1 * time.Millisecond,
		retryMaxBackoff:     5 * time.Millisecond,
	}

	t.Run("StatusPollingErrorDoesNotResubmitJob", func(t *testing.T) {
		submitCount := 0
		submitJob := func(ctx context.Context, jobID string) (*bigquery.Job, error) {
			submitCount++
			return &bigquery.Job{}, nil
		}

		pollCount := 0
		waitForJob := func(ctx context.Context, job *bigquery.Job) (*bigquery.JobStatus, error) {
			pollCount++
			return nil, errors.New("rateLimitExceeded: status query error")
		}

		_, err := submitAndPollJob(context.Background(), "noway-initial", db.retry, submitJob, waitForJob)
		if err == nil {
			t.Fatalf("expected error from waitForJob, got nil")
		}
		if submitCount != 1 {
			t.Errorf("job was submitted %d times; expected exactly 1 submission", submitCount)
		}
		if pollCount != 1 {
			t.Errorf("poll was called %d times; expected 1", pollCount)
		}
	})

	t.Run("NonRetryableJobExecutionFailureDoesNotResubmitJob", func(t *testing.T) {
		submitCount := 0
		submitJob := func(ctx context.Context, jobID string) (*bigquery.Job, error) {
			submitCount++
			return &bigquery.Job{}, nil
		}

		waitForJob := func(ctx context.Context, job *bigquery.Job) (*bigquery.JobStatus, error) {
			s := &bigquery.JobStatus{State: bigquery.Done}
			setJobStatusError(s, errors.New("syntax error at [1:10]"))
			return s, nil
		}

		_, err := submitAndPollJob(context.Background(), "noway-initial", db.retry, submitJob, waitForJob)
		if err == nil || !strings.Contains(err.Error(), "query execution failed") {
			t.Fatalf("expected query execution failed error, got %v", err)
		}
		if submitCount != 1 {
			t.Errorf("job was submitted %d times; expected exactly 1 submission", submitCount)
		}
	})

	t.Run("RetryableJobExecutionFailureRetriesWithNewJobID", func(t *testing.T) {
		var submittedJobIDs []string
		submitJob := func(ctx context.Context, jobID string) (*bigquery.Job, error) {
			submittedJobIDs = append(submittedJobIDs, jobID)
			return &bigquery.Job{}, nil
		}

		pollCount := 0
		waitForJob := func(ctx context.Context, job *bigquery.Job) (*bigquery.JobStatus, error) {
			pollCount++
			s := &bigquery.JobStatus{State: bigquery.Done}
			if pollCount == 1 {
				setJobStatusError(s, &bigquery.Error{Reason: "rateLimitExceeded", Message: "Exceeded rate limits"})
				return s, nil
			}
			if pollCount == 2 {
				setJobStatusError(s, &bigquery.Error{Reason: "backendError", Message: "Transient backend error"})
				return s, nil
			}
			return s, nil
		}

		status, err := submitAndPollJob(context.Background(), "noway-job-1", db.retry, submitJob, waitForJob)
		if err != nil {
			t.Fatalf("expected retry to succeed after transient status.Err() failures, got %v", err)
		}
		if !status.Done() {
			t.Errorf("expected status.Done() to be true")
		}
		if len(submittedJobIDs) != 3 {
			t.Fatalf("expected 3 job submissions, got %d (%v)", len(submittedJobIDs), submittedJobIDs)
		}
		if submittedJobIDs[0] != "noway-job-1" {
			t.Errorf("expected first job ID to be noway-job-1, got %q", submittedJobIDs[0])
		}
		seen := make(map[string]bool)
		for i, id := range submittedJobIDs {
			if id == "" {
				t.Errorf("attempt %d had empty job ID", i)
			}
			if seen[id] {
				t.Errorf("expected a new job ID after failed job, but job ID %q was reused across attempts (%v)", id, submittedJobIDs)
			}
			seen[id] = true
		}
	})

	t.Run("SubmissionRetriedOnTransientSubmissionFailureKeepsSameJobID", func(t *testing.T) {
		var submittedJobIDs []string
		submitJob := func(ctx context.Context, jobID string) (*bigquery.Job, error) {
			submittedJobIDs = append(submittedJobIDs, jobID)
			if len(submittedJobIDs) < 3 {
				return nil, errors.New("backendError: submission failed")
			}
			return &bigquery.Job{}, nil
		}

		pollCount := 0
		waitForJob := func(ctx context.Context, job *bigquery.Job) (*bigquery.JobStatus, error) {
			pollCount++
			return &bigquery.JobStatus{State: bigquery.Done}, nil
		}

		status, err := submitAndPollJob(context.Background(), "noway-same-id", db.retry, submitJob, waitForJob)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !status.Done() {
			t.Errorf("expected status.Done() to be true")
		}
		if len(submittedJobIDs) != 3 {
			t.Fatalf("expected 3 submissions, got %d", len(submittedJobIDs))
		}
		for i, id := range submittedJobIDs {
			if id != "noway-same-id" {
				t.Errorf("attempt %d used job ID %q; expected same job ID 'noway-same-id'", i, id)
			}
		}
		if pollCount != 1 {
			t.Errorf("expected 1 poll, got %d", pollCount)
		}
	})

	t.Run("SuccessSingleAttempt", func(t *testing.T) {
		submitCount := 0
		submitJob := func(ctx context.Context, jobID string) (*bigquery.Job, error) {
			submitCount++
			return &bigquery.Job{}, nil
		}

		waitForJob := func(ctx context.Context, job *bigquery.Job) (*bigquery.JobStatus, error) {
			return &bigquery.JobStatus{State: bigquery.Done}, nil
		}

		status, err := submitAndPollJob(context.Background(), "noway-single", db.retry, submitJob, waitForJob)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !status.Done() {
			t.Errorf("expected status.Done() to be true")
		}
		if submitCount != 1 {
			t.Errorf("expected 1 submission, got %d", submitCount)
		}
	})
}

func TestExecuteQuery_RetriesFailedJobWithNewJobIDAndRecovers409OutsideUSAndEU(t *testing.T) {
	const (
		projectID = "test-project"
		region    = "australia-southeast1"
	)

	t.Run("ExecuteQueryRetriesFailedJobWithNewJobID", func(t *testing.T) {
		var mu sync.Mutex
		var insertedJobIDs []string

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			if r.Method == http.MethodPost && r.URL.Path == "/bigquery/v2/projects/"+projectID+"/jobs" {
				var body struct {
					JobReference struct {
						ProjectID string `json:"projectId"`
						JobID     string `json:"jobId"`
						Location  string `json:"location"`
					} `json:"jobReference"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}

				mu.Lock()
				insertedJobIDs = append(insertedJobIDs, body.JobReference.JobID)
				attempt := len(insertedJobIDs)
				mu.Unlock()

				resp := map[string]any{
					"jobReference": map[string]any{
						"projectId": projectID,
						"jobId":     body.JobReference.JobID,
						"location":  region,
					},
					"status": map[string]any{
						"state": "RUNNING",
					},
				}
				if attempt == 1 {
					// First job will fail with rateLimitExceeded when polled
					_ = json.NewEncoder(w).Encode(resp)
					return
				}
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/bigquery/v2/projects/"+projectID+"/jobs/") {
				jobID := strings.TrimPrefix(r.URL.Path, "/bigquery/v2/projects/"+projectID+"/jobs/")
				mu.Lock()
				firstJobID := ""
				if len(insertedJobIDs) > 0 {
					firstJobID = insertedJobIDs[0]
				}
				mu.Unlock()

				status := map[string]any{
					"state": "DONE",
				}
				if jobID == firstJobID {
					status["errorResult"] = map[string]any{
						"reason":  "rateLimitExceeded",
						"message": "Exceeded rate limits: too many table update operations",
					}
				}
				resp := map[string]any{
					"jobReference": map[string]any{
						"projectId": projectID,
						"jobId":     jobID,
						"location":  region,
					},
					"status": status,
				}
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			http.NotFound(w, r)
		}))
		defer ts.Close()

		ctx := context.Background()
		client, err := bigquery.NewClient(ctx, projectID, option.WithEndpoint(ts.URL+"/bigquery/v2/"), option.WithoutAuthentication())
		if err != nil {
			t.Fatalf("failed to create bigquery client: %v", err)
		}
		defer client.Close()

		db := &BigQueryDatabase{
			client:              client,
			projectID:           projectID,
			retryInitialBackoff: 1 * time.Millisecond,
			retryMaxBackoff:     5 * time.Millisecond,
		}

		if err := db.ExecuteStatement(ctx, "CREATE TABLE `test_ds`.`t` (id INT64)"); err != nil {
			t.Fatalf("expected ExecuteStatement to succeed after retrying failed job, got: %v", err)
		}

		mu.Lock()
		defer mu.Unlock()
		if len(insertedJobIDs) != 2 {
			t.Fatalf("expected 2 jobs to be inserted, got %d (%v)", len(insertedJobIDs), insertedJobIDs)
		}
		if insertedJobIDs[0] == insertedJobIDs[1] {
			t.Fatalf("expected second attempt to use a new job ID, but both used %q", insertedJobIDs[0])
		}
	})

	t.Run("409RecoveryWithoutGCPLocation_ViaDatasetMetadata", func(t *testing.T) {
		var mu sync.Mutex
		var getJobLocations []string

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			// Dataset metadata endpoint returns non-US/EU location
			if r.Method == http.MethodGet && r.URL.Path == "/bigquery/v2/projects/"+projectID+"/datasets/regional_schema" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"datasetReference": map[string]any{
						"projectId": projectID,
						"datasetId": "regional_schema",
					},
					"location": region,
				})
				return
			}

			// Job insert returns 409 (without location in error text to test dataset metadata resolution)
			if r.Method == http.MethodPost && r.URL.Path == "/bigquery/v2/projects/"+projectID+"/jobs" {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"code":    409,
						"message": "Already Exists: duplicate job",
					},
				})
				return
			}

			// Job get: outside US/EU, BigQuery returns 404 if location is not specified!
			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/bigquery/v2/projects/"+projectID+"/jobs/") {
				jobID := strings.TrimPrefix(r.URL.Path, "/bigquery/v2/projects/"+projectID+"/jobs/")
				loc := r.URL.Query().Get("location")
				mu.Lock()
				getJobLocations = append(getJobLocations, loc)
				mu.Unlock()

				if loc != region {
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]any{
							"code":    404,
							"message": "Not found: Job " + projectID + ":" + jobID,
						},
					})
					return
				}

				_ = json.NewEncoder(w).Encode(map[string]any{
					"jobReference": map[string]any{
						"projectId": projectID,
						"jobId":     jobID,
						"location":  region,
					},
					"status": map[string]any{
						"state": "DONE",
					},
				})
				return
			}

			http.NotFound(w, r)
		}))
		defer ts.Close()

		ctx := context.Background()
		client, err := bigquery.NewClient(ctx, projectID, option.WithEndpoint(ts.URL+"/bigquery/v2/"), option.WithoutAuthentication())
		if err != nil {
			t.Fatalf("failed to create bigquery client: %v", err)
		}
		defer client.Close()

		// GCPLocation / db.location is intentionally left empty!
		db := &BigQueryDatabase{
			client:              client,
			projectID:           projectID,
			config:              &config.Configuration{DefaultSchema: "regional_schema"},
			retryInitialBackoff: 1 * time.Millisecond,
			retryMaxBackoff:     5 * time.Millisecond,
		}

		if err := db.ExecuteStatement(ctx, "CREATE TABLE `regional_schema`.`t` (id INT64)"); err != nil {
			t.Fatalf("expected 409 recovery outside US/EU without GCPLocation to succeed, got: %v", err)
		}

		mu.Lock()
		defer mu.Unlock()
		if len(getJobLocations) == 0 {
			t.Fatalf("expected JobFromProject / Status to be called")
		}
		for i, loc := range getJobLocations {
			if loc != region {
				t.Errorf("jobs.get call %d used location %q; want %q", i, loc, region)
			}
		}
	})

	t.Run("409RecoveryWithoutGCPLocation_ViaJobsListFallback", func(t *testing.T) {
		const tokyoRegion = "asia-northeast1"
		var mu sync.Mutex
		var lastInsertedJobID string
		var getJobLocations []string

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			// Job insert returns generic 409 (no location in error message, no dataset configured)
			if r.Method == http.MethodPost && r.URL.Path == "/bigquery/v2/projects/"+projectID+"/jobs" {
				var body struct {
					JobReference struct {
						JobID string `json:"jobId"`
					} `json:"jobReference"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				lastInsertedJobID = body.JobReference.JobID
				mu.Unlock()

				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"code":    409,
						"message": "Already Exists: duplicate job",
					},
				})
				return
			}

			// Jobs.List endpoint returns the job with its asia-northeast1 location
			if r.Method == http.MethodGet && r.URL.Path == "/bigquery/v2/projects/"+projectID+"/jobs" {
				mu.Lock()
				jid := lastInsertedJobID
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jobs": []map[string]any{
						{
							"jobReference": map[string]any{
								"projectId": projectID,
								"jobId":     jid,
								"location":  tokyoRegion,
							},
							"status": map[string]any{
								"state": "DONE",
							},
						},
					},
				})
				return
			}

			// Jobs.Get requires location=asia-northeast1
			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/bigquery/v2/projects/"+projectID+"/jobs/") {
				jobID := strings.TrimPrefix(r.URL.Path, "/bigquery/v2/projects/"+projectID+"/jobs/")
				loc := r.URL.Query().Get("location")
				mu.Lock()
				getJobLocations = append(getJobLocations, loc)
				mu.Unlock()

				if loc != tokyoRegion {
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]any{
							"code":    404,
							"message": "Not found: Job " + projectID + ":" + jobID,
						},
					})
					return
				}

				_ = json.NewEncoder(w).Encode(map[string]any{
					"jobReference": map[string]any{
						"projectId": projectID,
						"jobId":     jobID,
						"location":  tokyoRegion,
					},
					"status": map[string]any{
						"state": "DONE",
					},
				})
				return
			}

			http.NotFound(w, r)
		}))
		defer ts.Close()

		ctx := context.Background()
		client, err := bigquery.NewClient(ctx, projectID, option.WithEndpoint(ts.URL+"/bigquery/v2/"), option.WithoutAuthentication())
		if err != nil {
			t.Fatalf("failed to create bigquery client: %v", err)
		}
		defer client.Close()

		// Neither GCPLocation nor DefaultSchema is set
		db := &BigQueryDatabase{
			client:              client,
			projectID:           projectID,
			retryInitialBackoff: 1 * time.Millisecond,
			retryMaxBackoff:     5 * time.Millisecond,
		}

		if err := db.ExecuteStatement(ctx, "CREATE TABLE `tokyo_ds`.`t` (id INT64)"); err != nil {
			t.Fatalf("expected 409 recovery via Jobs.List fallback to succeed, got: %v", err)
		}

		mu.Lock()
		defer mu.Unlock()
		if len(getJobLocations) == 0 {
			t.Fatalf("expected jobs.get to be called with discovered location")
		}
		for i, loc := range getJobLocations {
			if loc != tokyoRegion {
				t.Errorf("jobs.get call %d used location %q; want %q", i, loc, tokyoRegion)
			}
		}
	})

	t.Run("SubmissionRetryHits409AndRecoversVia409ErrorLocation", func(t *testing.T) {
		var mu sync.Mutex
		var postJobIDs []string
		var getJobLocations []string

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			if r.Method == http.MethodPost && r.URL.Path == "/bigquery/v2/projects/"+projectID+"/jobs" {
				var body struct {
					JobReference struct {
						JobID string `json:"jobId"`
					} `json:"jobReference"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				postJobIDs = append(postJobIDs, body.JobReference.JobID)
				attempt := len(postJobIDs)
				mu.Unlock()

				if attempt == 1 {
					// First attempt fails with retryable quotaExceeded error (after server created job)
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]any{
							"code":    403,
							"message": "Quota exceeded",
							"errors": []map[string]any{
								{"reason": "quotaExceeded", "message": "Quota exceeded"},
							},
						},
					})
					return
				}

				// Second attempt with the SAME job ID hits 409 Already Exists
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"code":    409,
						"message": "Already Exists: Job " + projectID + ":" + region + "." + body.JobReference.JobID + ", duplicate",
					},
				})
				return
			}

			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/bigquery/v2/projects/"+projectID+"/jobs/") {
				jobID := strings.TrimPrefix(r.URL.Path, "/bigquery/v2/projects/"+projectID+"/jobs/")
				loc := r.URL.Query().Get("location")
				mu.Lock()
				getJobLocations = append(getJobLocations, loc)
				mu.Unlock()

				if loc != region {
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]any{
							"code":    404,
							"message": "Not found: Job " + projectID + ":" + jobID,
						},
					})
					return
				}

				_ = json.NewEncoder(w).Encode(map[string]any{
					"jobReference": map[string]any{
						"projectId": projectID,
						"jobId":     jobID,
						"location":  region,
					},
					"status": map[string]any{
						"state": "DONE",
					},
				})
				return
			}

			http.NotFound(w, r)
		}))
		defer ts.Close()

		ctx := context.Background()
		client, err := bigquery.NewClient(ctx, projectID, option.WithEndpoint(ts.URL+"/bigquery/v2/"), option.WithoutAuthentication())
		if err != nil {
			t.Fatalf("failed to create bigquery client: %v", err)
		}
		defer client.Close()

		db := &BigQueryDatabase{
			client:              client,
			projectID:           projectID,
			retryInitialBackoff: 1 * time.Millisecond,
			retryMaxBackoff:     5 * time.Millisecond,
		}

		if err := db.ExecuteStatement(ctx, "CREATE TABLE `sydney_ds`.`t` (id INT64)"); err != nil {
			t.Fatalf("expected submission retry + 409 recovery to succeed, got: %v", err)
		}

		mu.Lock()
		defer mu.Unlock()
		if len(postJobIDs) != 2 {
			t.Fatalf("expected 2 POST /jobs calls, got %d (%v)", len(postJobIDs), postJobIDs)
		}
		if postJobIDs[0] != postJobIDs[1] {
			t.Errorf("expected submission retry to reuse job ID %q, got %q", postJobIDs[0], postJobIDs[1])
		}
		if len(getJobLocations) == 0 {
			t.Fatalf("expected JobFromProject to be called")
		}
		for i, loc := range getJobLocations {
			if loc != region {
				t.Errorf("jobs.get call %d used location %q; want %q", i, loc, region)
			}
		}
	})
}
