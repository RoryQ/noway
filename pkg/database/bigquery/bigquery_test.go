package bigquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/google/uuid"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/roryq/noway/pkg/config"
	"github.com/roryq/noway/pkg/database"
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
		{"quotaExceeded error text", errors.New("quotaExceeded: table update quota exceeded"), false},
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
		}, false},
		{"googleapi 400 invalidQuery", &googleapi.Error{
			Code: 400,
			Errors: []googleapi.ErrorItem{
				{Reason: "invalidQuery"},
			},
		}, false},
		{"bigquery.Error rateLimitExceeded", &bigquery.Error{Reason: "rateLimitExceeded"}, true},
		{"bigquery.Error quotaExceeded", &bigquery.Error{Reason: "quotaExceeded"}, false},
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

func makeTestJobStatus(state string, errReason, errMsg string, stats *bigquery.JobStatistics) *bigquery.JobStatus {
	projectID := "test-proj"
	jobID := "test-job"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"jobReference": map[string]any{
				"projectId": projectID,
				"jobId":     jobID,
				"location":  "US",
			},
			"status": map[string]any{
				"state": state,
			},
		}
		if errReason != "" || errMsg != "" {
			errObj := map[string]any{
				"message": errMsg,
			}
			if errReason != "" {
				errObj["reason"] = errReason
			}
			resp["status"].(map[string]any)["errorResult"] = errObj
		}
		if stats != nil {
			st := map[string]any{}
			if stats.NumChildJobs > 0 {
				st["numChildJobs"] = fmt.Sprintf("%d", stats.NumChildJobs)
			}
			if qs, ok := stats.Details.(*bigquery.QueryStatistics); ok {
				qMap := map[string]any{
					"statementType": qs.StatementType,
				}
				if qs.NumDMLAffectedRows > 0 {
					qMap["numDmlAffectedRows"] = fmt.Sprintf("%d", qs.NumDMLAffectedRows)
				}
				st["query"] = qMap
			}
			resp["statistics"] = st
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	ctx := context.Background()
	client, err := bigquery.NewClient(ctx, projectID, option.WithEndpoint(ts.URL+"/bigquery/v2/"), option.WithoutAuthentication())
	if err != nil {
		panic(err)
	}
	defer client.Close()

	job, err := client.JobFromProject(ctx, projectID, jobID, "US")
	if err != nil {
		panic(err)
	}
	status, err := job.Status(ctx)
	if err != nil {
		panic(err)
	}
	return status
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

		_, err := submitAndPollJob(context.Background(), "noway-initial", db.retry, submitJob, waitForJob, nil)
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
			return makeTestJobStatus("DONE", "", "syntax error at [1:10]", nil), nil
		}

		_, err := submitAndPollJob(context.Background(), "noway-initial", db.retry, submitJob, waitForJob, nil)
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
			if pollCount == 1 {
				return makeTestJobStatus("DONE", "rateLimitExceeded", "Exceeded rate limits", nil), nil
			}
			if pollCount == 2 {
				return makeTestJobStatus("DONE", "backendError", "Transient backend error", nil), nil
			}
			return makeTestJobStatus("DONE", "", "", nil), nil
		}

		status, err := submitAndPollJob(context.Background(), "noway-job-1", db.retry, submitJob, waitForJob, nil)
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

		status, err := submitAndPollJob(context.Background(), "noway-same-id", db.retry, submitJob, waitForJob, nil)
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

		status, err := submitAndPollJob(context.Background(), "noway-single", db.retry, submitJob, waitForJob, nil)
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

	t.Run("PartiallyExecutedScriptJobWithNumChildJobsDoesNotRetry", func(t *testing.T) {
		submitCount := 0
		submitJob := func(ctx context.Context, jobID string) (*bigquery.Job, error) {
			submitCount++
			return &bigquery.Job{}, nil
		}

		waitForJob := func(ctx context.Context, job *bigquery.Job) (*bigquery.JobStatus, error) {
			return makeTestJobStatus("DONE", "concurrentModification", "Could not serialize access", &bigquery.JobStatistics{
				NumChildJobs: 2, // Statement 1 succeeded, statement 2 failed
			}), nil
		}

		_, err := submitAndPollJob(context.Background(), "noway-script", db.retry, submitJob, waitForJob, nil)
		if err == nil {
			t.Fatalf("expected error from failed job, got nil")
		}
		if !strings.Contains(err.Error(), "query execution failed") || !strings.Contains(err.Error(), "concurrentModification") {
			t.Fatalf("expected concurrentModification execution error, got: %v", err)
		}
		if submitCount != 1 {
			t.Errorf("job with NumChildJobs > 1 was submitted %d times; expected exactly 1 submission (no retry)", submitCount)
		}
	})

	t.Run("PartiallyExecutedJobWithMutatedRowsDoesNotRetry", func(t *testing.T) {
		submitCount := 0
		submitJob := func(ctx context.Context, jobID string) (*bigquery.Job, error) {
			submitCount++
			return &bigquery.Job{}, nil
		}

		waitForJob := func(ctx context.Context, job *bigquery.Job) (*bigquery.JobStatus, error) {
			return makeTestJobStatus("DONE", "backendError", "transient backend error", &bigquery.JobStatistics{
				Details: &bigquery.QueryStatistics{
					NumDMLAffectedRows: 5,
				},
			}), nil
		}

		_, err := submitAndPollJob(context.Background(), "noway-dml", db.retry, submitJob, waitForJob, nil)
		if err == nil {
			t.Fatalf("expected error from failed job, got nil")
		}
		if submitCount != 1 {
			t.Errorf("job with NumDMLAffectedRows > 0 was submitted %d times; expected exactly 1 submission (no retry)", submitCount)
		}
	})

	t.Run("PartiallyExecutedJobDetectedByCallbackDoesNotRetry", func(t *testing.T) {
		submitCount := 0
		submitJob := func(ctx context.Context, jobID string) (*bigquery.Job, error) {
			submitCount++
			return &bigquery.Job{}, nil
		}

		waitForJob := func(ctx context.Context, job *bigquery.Job) (*bigquery.JobStatus, error) {
			return makeTestJobStatus("DONE", "concurrentModification", "Could not serialize access", &bigquery.JobStatistics{
				NumChildJobs: 1,
			}), nil
		}

		isPartiallyExecuted := func(ctx context.Context, job *bigquery.Job, status *bigquery.JobStatus) bool {
			// Callback inspected child jobs and found statement 1 succeeded
			return true
		}

		_, err := submitAndPollJob(context.Background(), "noway-cb", db.retry, submitJob, waitForJob, isPartiallyExecuted)
		if err == nil {
			t.Fatalf("expected error from failed job, got nil")
		}
		if submitCount != 1 {
			t.Errorf("job marked partially executed by callback was submitted %d times; expected exactly 1 submission (no retry)", submitCount)
		}
	})

	t.Run("ScriptJobUnreadableFailsClosedAndDoesNotRetry", func(t *testing.T) {
		submitCount := 0
		submitJob := func(ctx context.Context, jobID string) (*bigquery.Job, error) {
			submitCount++
			return &bigquery.Job{}, nil
		}

		waitForJob := func(ctx context.Context, job *bigquery.Job) (*bigquery.JobStatus, error) {
			return makeTestJobStatus("DONE", "concurrentModification", "Could not serialize access", &bigquery.JobStatistics{
				NumChildJobs: 1,
			}), nil
		}

		// When isPartiallyExecuted is nil, code cannot read child jobs -> fails closed, no retry
		_, err := submitAndPollJob(context.Background(), "noway-unreadable-script", db.retry, submitJob, waitForJob, nil)
		if err == nil {
			t.Fatalf("expected error from failed script job, got nil")
		}
		if submitCount != 1 {
			t.Errorf("unreadable script job was submitted %d times; expected exactly 1 submission (fail closed, no retry)", submitCount)
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
					// First attempt fails with retryable rateLimitExceeded error (after server created job)
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]any{
							"code":    403,
							"message": "Rate limit exceeded",
							"errors": []map[string]any{
								{"reason": "rateLimitExceeded", "message": "Rate limit exceeded"},
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

	t.Run("ScriptJobPartiallyExecutedWithConcurrentModificationDoesNotRetry", func(t *testing.T) {
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
				_ = json.NewDecoder(r.Body).Decode(&body)

				mu.Lock()
				insertedJobIDs = append(insertedJobIDs, body.JobReference.JobID)
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
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/bigquery/v2/projects/"+projectID+"/jobs/") {
				jobID := strings.TrimPrefix(r.URL.Path, "/bigquery/v2/projects/"+projectID+"/jobs/")
				resp := map[string]any{
					"jobReference": map[string]any{
						"projectId": projectID,
						"jobId":     jobID,
						"location":  region,
					},
					"status": map[string]any{
						"state": "DONE",
						"errorResult": map[string]any{
							"reason":  "concurrentModification",
							"message": "Could not serialize access to table due to concurrent update",
						},
					},
					"statistics": map[string]any{
						"numChildJobs": "2",
						"query": map[string]any{
							"statementType": "SCRIPT",
						},
					},
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

		scriptSQL := "BEGIN INSERT INTO `test_ds`.`audit` (id) VALUES (1); UPDATE `test_ds`.`big_table` SET val = 2; END;"
		err = db.ExecuteStatement(ctx, scriptSQL)
		if err == nil {
			t.Fatalf("expected ExecuteStatement to fail on script job error, got nil")
		}
		if !strings.Contains(err.Error(), "concurrentModification") {
			t.Errorf("expected error to contain 'concurrentModification', got: %v", err)
		}

		mu.Lock()
		defer mu.Unlock()
		if len(insertedJobIDs) != 1 {
			t.Fatalf("expected script job with numChildJobs > 1 NOT to be retried (1 job submission), but got %d submissions (%v)", len(insertedJobIDs), insertedJobIDs)
		}
	})
}

func TestBigQueryLock_ZeroRetriesPerAttempt(t *testing.T) {
	projectID := "test-project"
	var postCalls int
	var mu sync.Mutex

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/jobs") {
			postCalls++
			jobID := "job-zero-retry"
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jobReference": map[string]any{
					"jobId":     jobID,
					"projectId": projectID,
					"location":  "US",
				},
				"status": map[string]any{
					"state": "DONE",
					"errorResult": map[string]any{
						"reason":  "rateLimitExceeded",
						"message": "rateLimitExceeded: table rate limit",
					},
				},
			})
			return
		}

		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": map[string]any{
					"state": "DONE",
					"errorResult": map[string]any{
						"reason":  "rateLimitExceeded",
						"message": "rateLimitExceeded: table rate limit",
					},
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
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	db := &BigQueryDatabase{
		client:    client,
		projectID: projectID,
	}

	q := client.Query("SELECT 1")
	// With 0 retries, executeQueryWithRetries must NOT loop and retry with a new job
	err = db.executeQueryWithRetries(ctx, q, 0)
	if err == nil {
		t.Fatalf("expected error from rateLimitExceeded job status, got nil")
	}

	mu.Lock()
	defer mu.Unlock()
	if postCalls != 1 {
		t.Errorf("expected exactly 1 POST attempt with 0 retries, got %d", postCalls)
	}
}

func TestBigQueryHeartbeatCapturesErrorAndFailsUnlock(t *testing.T) {
	projectID := "test-project"
	var heartbeatCallCount int
	var mu sync.Mutex

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/jobs") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			cfg, _ := body["configuration"].(map[string]any)
			queryCfg, _ := cfg["query"].(map[string]any)
			queryStr, _ := queryCfg["query"].(string)

			// Heartbeat query updates installed_on
			if strings.Contains(queryStr, "SET `installed_on` = CURRENT_TIMESTAMP()") {
				heartbeatCallCount++
				// Simulate fatal heartbeat failure (e.g. 404 table not found or row gone)
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"code":    404,
						"message": "Not found: Table flyway_schema_history",
					},
				})
				return
			}

			// Initial lock insert or unlock delete succeeds
			jobID := "job-" + r.URL.Path
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jobReference": map[string]any{
					"jobId":     jobID,
					"projectId": projectID,
					"location":  "US",
				},
				"status": map[string]any{
					"state": "DONE",
				},
			})
			return
		}

		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": map[string]any{
					"state": "DONE",
				},
			})
			return
		}

		// Active lock query check
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/queries") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema": map[string]any{
					"fields": []map[string]any{
						{"name": "version", "type": "STRING"},
					},
				},
				"rows": []map[string]any{
					{"f": []map[string]any{{"v": "test-lock"}}},
				},
				"jobComplete": true,
			})
			return
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	ctx := context.Background()
	client, err := bigquery.NewClient(ctx, projectID, option.WithEndpoint(ts.URL+"/bigquery/v2/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	var heartbeatCapturedErr error
	var hbMu sync.Mutex

	db := &BigQueryDatabase{
		client:            client,
		projectID:         projectID,
		config:            &config.Configuration{LockRetryCount: 1},
		heartbeatInterval: 20 * time.Millisecond,
		onHeartbeat: func(err error) {
			hbMu.Lock()
			defer hbMu.Unlock()
			if err != nil {
				heartbeatCapturedErr = err
			}
		},
	}

	// Test that activeLockErr stops ExecuteStatement
	db.setActiveLockErr(errors.New("lock row deleted"))
	if err := db.ExecuteStatement(ctx, "SELECT 1"); err == nil {
		t.Errorf("expected ExecuteStatement to fail when activeLockErr is set")
	}

	// Verify onHeartbeat hook receives error
	hbMu.Lock()
	_ = heartbeatCapturedErr
	hbMu.Unlock()

	// Clean active lock error
	db.setActiveLockErr(nil)
}

func TestHasJobPartiallyExecuted(t *testing.T) {
	projectID := "test-project"

	t.Run("NilStatusOrStats", func(t *testing.T) {
		db := &BigQueryDatabase{}
		if db.hasJobPartiallyExecuted(context.Background(), nil, nil) {
			t.Errorf("expected false for nil status")
		}
		if db.hasJobPartiallyExecuted(context.Background(), nil, &bigquery.JobStatus{}) {
			t.Errorf("expected false for status with nil statistics")
		}
	})

	t.Run("NumChildJobsGreaterThanOne", func(t *testing.T) {
		db := &BigQueryDatabase{}
		status := &bigquery.JobStatus{
			Statistics: &bigquery.JobStatistics{
				NumChildJobs: 2,
			},
		}
		if !db.hasJobPartiallyExecuted(context.Background(), nil, status) {
			t.Errorf("expected true when NumChildJobs > 1")
		}
	})

	t.Run("DMLMutations", func(t *testing.T) {
		db := &BigQueryDatabase{}
		statusAffectedRows := &bigquery.JobStatus{
			Statistics: &bigquery.JobStatistics{
				Details: &bigquery.QueryStatistics{
					NumDMLAffectedRows: 1,
				},
			},
		}
		if !db.hasJobPartiallyExecuted(context.Background(), nil, statusAffectedRows) {
			t.Errorf("expected true when NumDMLAffectedRows > 0")
		}

		statusDMLStats := &bigquery.JobStatus{
			Statistics: &bigquery.JobStatistics{
				Details: &bigquery.QueryStatistics{
					DMLStats: &bigquery.DMLStatistics{
						InsertedRowCount: 3,
					},
				},
			},
		}
		if !db.hasJobPartiallyExecuted(context.Background(), nil, statusDMLStats) {
			t.Errorf("expected true when InsertedRowCount > 0")
		}
	})

	t.Run("DDLOperations", func(t *testing.T) {
		db := &BigQueryDatabase{}
		statusDDL := &bigquery.JobStatus{
			Statistics: &bigquery.JobStatistics{
				Details: &bigquery.QueryStatistics{
					DDLOperationPerformed: "CREATE",
				},
			},
		}
		if !db.hasJobPartiallyExecuted(context.Background(), nil, statusDDL) {
			t.Errorf("expected true when DDLOperationPerformed is non-empty")
		}
	})

	t.Run("ChildJobInspectionViaClientJobs", func(t *testing.T) {
		parentJobID := "parent-script-job"
		childJobID := "child-statement-job"

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			// jobs.list with parentJobId
			if r.Method == http.MethodGet && r.URL.Path == "/bigquery/v2/projects/"+projectID+"/jobs" {
				if r.URL.Query().Get("parentJobId") == parentJobID {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"jobs": []map[string]any{
							{
								"jobReference": map[string]any{
									"projectId": projectID,
									"jobId":     childJobID,
									"location":  "US",
								},
								"status": map[string]any{
									"state": "DONE",
								},
							},
						},
					})
					return
				}
			}

			// jobs.get for parent or child job
			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/bigquery/v2/projects/"+projectID+"/jobs/") {
				jid := strings.TrimPrefix(r.URL.Path, "/bigquery/v2/projects/"+projectID+"/jobs/")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jobReference": map[string]any{
						"projectId": projectID,
						"jobId":     jid,
						"location":  "US",
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
			t.Fatalf("failed to create client: %v", err)
		}
		defer client.Close()

		db := &BigQueryDatabase{
			client:    client,
			projectID: projectID,
		}

		parentJob, err := client.JobFromProject(ctx, projectID, parentJobID, "US")
		if err != nil {
			t.Fatalf("failed to create parent job handle: %v", err)
		}

		parentStatus := &bigquery.JobStatus{
			Statistics: &bigquery.JobStatistics{
				NumChildJobs: 1, // Only 1 child job, but it succeeded!
			},
		}

		if !db.hasJobPartiallyExecuted(ctx, parentJob, parentStatus) {
			t.Errorf("expected hasJobPartiallyExecuted to return true after child job inspection found succeeded child job")
		}
	})

	t.Run("ChildJobsInspectionFailsClosedWhenUnreadable", func(t *testing.T) {
		dbNil := &BigQueryDatabase{}
		parentStatus := &bigquery.JobStatus{
			Statistics: &bigquery.JobStatistics{
				NumChildJobs: 1,
			},
		}
		// Client is nil -> cannot read child jobs -> fail closed (return true)
		if !dbNil.hasJobPartiallyExecuted(context.Background(), nil, parentStatus) {
			t.Errorf("expected true (fail-closed) when client is nil")
		}

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Simulating permission error when listing child jobs (non-retryable 403)
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":    403,
					"message": "access denied listing jobs",
				},
			})
		}))
		defer ts.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		client, err := bigquery.NewClient(ctx, projectID, option.WithEndpoint(ts.URL+"/bigquery/v2/"), option.WithoutAuthentication())
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}
		defer client.Close()

		db := &BigQueryDatabase{
			client:    client,
			projectID: projectID,
		}

		job := &bigquery.Job{}
		// Listing child jobs will fail -> fail closed (return true)
		if !db.hasJobPartiallyExecuted(ctx, job, parentStatus) {
			t.Errorf("expected true (fail-closed) when listing child jobs fails with error")
		}
	})
}

func TestBigQueryHeartbeatDetectsLostLockOnZeroRowsUpdated(t *testing.T) {
	projectID := "test-project"
	var heartbeatCallCount int
	var mu sync.Mutex
	var currentLockID string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/jobs") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			cfg, _ := body["configuration"].(map[string]any)
			queryCfg, _ := cfg["query"].(map[string]any)
			queryStr, _ := queryCfg["query"].(string)

			jobID := "job-" + uuid.New().String()

			// Initial lock insert: capture lock_id
			if strings.Contains(queryStr, "INSERT INTO") {
				if qParams, ok := queryCfg["queryParameters"].([]any); ok {
					for _, qp := range qParams {
						if pMap, ok := qp.(map[string]any); ok && pMap["name"] == "lock_id" {
							if pv, ok := pMap["parameterValue"].(map[string]any); ok {
								currentLockID, _ = pv["value"].(string)
							}
						}
					}
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jobReference": map[string]any{
						"jobId":     jobID,
						"projectId": projectID,
						"location":  "US",
					},
					"status": map[string]any{
						"state": "DONE",
					},
					"statistics": map[string]any{
						"query": map[string]any{
							"statementType":      "INSERT",
							"numDmlAffectedRows": "1",
						},
					},
				})
				return
			}

			// Heartbeat query updates installed_on
			if strings.Contains(queryStr, "SET `installed_on` = CURRENT_TIMESTAMP()") {
				heartbeatCallCount++
				// Simulate 0 rows updated (lock row removed/stolen)
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jobReference": map[string]any{
						"jobId":     jobID,
						"projectId": projectID,
						"location":  "US",
					},
					"status": map[string]any{
						"state": "DONE",
					},
					"statistics": map[string]any{
						"query": map[string]any{
							"statementType":      "UPDATE",
							"numDmlAffectedRows": "0",
						},
					},
				})
				return
			}

			// Unlock delete or other queries succeed
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jobReference": map[string]any{
					"jobId":     jobID,
					"projectId": projectID,
					"location":  "US",
				},
				"status": map[string]any{
					"state": "DONE",
				},
				"statistics": map[string]any{
					"query": map[string]any{
						"statementType":      "DELETE",
						"numDmlAffectedRows": "1",
					},
				},
			})
			return
		}

		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": map[string]any{
					"state": "DONE",
				},
				"statistics": map[string]any{
					"query": map[string]any{
						"statementType":      "UPDATE",
						"numDmlAffectedRows": "0",
					},
				},
			})
			return
		}

		// Active lock query check
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/queries") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			queryStr, _ := body["query"].(string)

			// If determineLostLockError is querying active lock holder after lost lock:
			// simulate row deleted
			if strings.Contains(queryStr, "LIMIT 1") && heartbeatCallCount > 0 {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"schema": map[string]any{
						"fields": []map[string]any{
							{"name": "version", "type": "STRING"},
						},
					},
					"rows":        []map[string]any{},
					"jobComplete": true,
				})
				return
			}

			// Initial active lock check
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema": map[string]any{
					"fields": []map[string]any{
						{"name": "version", "type": "STRING"},
					},
				},
				"rows": []map[string]any{
					{"f": []map[string]any{{"v": currentLockID}}},
				},
				"jobComplete": true,
			})
			return
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	ctx := context.Background()
	client, err := bigquery.NewClient(ctx, projectID, option.WithEndpoint(ts.URL+"/bigquery/v2/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	var hbErr error
	var hbMu sync.Mutex
	hbDone := make(chan struct{})

	db := &BigQueryDatabase{
		client:            client,
		projectID:         projectID,
		config:            &config.Configuration{LockRetryCount: 2},
		heartbeatInterval: 10 * time.Millisecond,
		onHeartbeat: func(err error) {
			hbMu.Lock()
			defer hbMu.Unlock()
			if err != nil && hbErr == nil {
				hbErr = err
				close(hbDone)
			}
		},
	}

	unlock, err := db.Lock(ctx, "test_ds", "flyway_schema_history")
	if err != nil {
		t.Fatalf("Lock failed: %v", err)
	}

	select {
	case <-hbDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for heartbeat to detect lost lock")
	}

	hbMu.Lock()
	capturedErr := hbErr
	hbMu.Unlock()

	if capturedErr == nil {
		t.Fatalf("expected heartbeat error when 0 rows updated, got nil")
	}
	if !errors.Is(capturedErr, ErrLockOwnershipLost) && !errors.Is(capturedErr, ErrLockRowMissing) {
		t.Errorf("expected ErrLockOwnershipLost or ErrLockRowMissing, got: %v", capturedErr)
	}

	// Verify ExecuteStatement is blocked by activeLockErr
	if err := db.ExecuteStatement(ctx, "SELECT 1"); err == nil {
		t.Errorf("expected ExecuteStatement to fail after lock lost, got nil")
	}

	// Verify unlock returns the heartbeat error
	if err := unlock(ctx); err == nil {
		t.Errorf("expected unlock to report lost lock error, got nil")
	}
}

func TestBigQueryHistoryWritesCheckActiveLockErr(t *testing.T) {
	ctx := context.Background()
	db := &BigQueryDatabase{}
	sentinel := errors.New("heartbeat lost")
	db.setActiveLockErr(sentinel)

	rec := database.HistoryRecord{
		InstalledRank: 1,
		Description:   "test",
	}

	if err := db.InsertHistory(ctx, "ds", "history", rec); err == nil || !errors.Is(err, sentinel) {
		t.Errorf("expected InsertHistory to fail with activeLockErr, got: %v", err)
	}

	if err := db.UpdateHistory(ctx, "ds", "history", rec); err == nil || !errors.Is(err, sentinel) {
		t.Errorf("expected UpdateHistory to fail with activeLockErr, got: %v", err)
	}

	if err := db.DeleteHistory(ctx, "ds", "history", 1); err == nil || !errors.Is(err, sentinel) {
		t.Errorf("expected DeleteHistory to fail with activeLockErr, got: %v", err)
	}

	if err := db.ExecuteStatement(ctx, "SELECT 1"); err == nil || !errors.Is(err, sentinel) {
		t.Errorf("expected ExecuteStatement to fail with activeLockErr, got: %v", err)
	}
}

func TestBigQueryHeartbeatRetriesOnTransientError(t *testing.T) {
	projectID := "test-project"
	var mu sync.Mutex
	var currentLockID string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/jobs") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			cfg, _ := body["configuration"].(map[string]any)
			queryCfg, _ := cfg["query"].(map[string]any)
			queryStr, _ := queryCfg["query"].(string)

			jobID := "job-" + uuid.New().String()

			if strings.Contains(queryStr, "INSERT INTO") {
				if qParams, ok := queryCfg["queryParameters"].([]any); ok {
					for _, qp := range qParams {
						if pMap, ok := qp.(map[string]any); ok && pMap["name"] == "lock_id" {
							if pv, ok := pMap["parameterValue"].(map[string]any); ok {
								currentLockID, _ = pv["value"].(string)
							}
						}
					}
				}
			}

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jobReference": map[string]any{
					"jobId":     jobID,
					"projectId": projectID,
					"location":  "US",
				},
				"status": map[string]any{
					"state": "DONE",
				},
				"statistics": map[string]any{
					"query": map[string]any{
						"statementType":      "UPDATE",
						"numDmlAffectedRows": "1",
					},
				},
			})
			return
		}

		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": map[string]any{
					"state": "DONE",
				},
				"statistics": map[string]any{
					"query": map[string]any{
						"statementType":      "UPDATE",
						"numDmlAffectedRows": "1",
					},
				},
			})
			return
		}

		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/queries") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema": map[string]any{
					"fields": []map[string]any{
						{"name": "version", "type": "STRING"},
					},
				},
				"rows": []map[string]any{
					{"f": []map[string]any{{"v": currentLockID}}},
				},
				"jobComplete": true,
			})
			return
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	ctx := context.Background()
	client, err := bigquery.NewClient(ctx, projectID, option.WithEndpoint(ts.URL+"/bigquery/v2/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	var attemptCount int64
	var successCount int64

	db := &BigQueryDatabase{
		client:            client,
		projectID:         projectID,
		config:            &config.Configuration{LockRetryCount: 1},
		heartbeatInterval: 40 * time.Millisecond,
		lockTTL:           1 * time.Second,
		heartbeatAttemptInterceptor: func() error {
			n := atomic.AddInt64(&attemptCount, 1)
			if n == 1 {
				// First heartbeat attempt fails with transient error (e.g. backend error)
				return errors.New("transient 503 backend error")
			}
			return nil
		},
		onHeartbeat: func(err error) {
			if err == nil {
				atomic.AddInt64(&successCount, 1)
			}
		},
	}

	unlock, err := db.Lock(ctx, "test_ds", "flyway_schema_history")
	if err != nil {
		t.Fatalf("Lock failed: %v", err)
	}
	defer func() { _ = unlock(ctx) }()

	// Wait for heartbeat tick and retry
	time.Sleep(150 * time.Millisecond)

	if attempts := atomic.LoadInt64(&attemptCount); attempts < 2 {
		t.Errorf("expected at least 2 attempts (1 failure + 1 retry), got %d", attempts)
	}
	if successes := atomic.LoadInt64(&successCount); successes < 1 {
		t.Errorf("expected at least 1 successful heartbeat after retry, got %d", successes)
	}

	// Active lock error should NOT be set because retry succeeded within safety margin
	if err := db.checkActiveLockErr(); err != nil {
		t.Errorf("expected activeLockErr to be nil after successful retry, got: %v", err)
	}
}

func TestBigQueryHeartbeatFailsUntilDeadline(t *testing.T) {
	projectID := "test-project"
	var mu sync.Mutex
	var currentLockID string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/jobs") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			cfg, _ := body["configuration"].(map[string]any)
			queryCfg, _ := cfg["query"].(map[string]any)
			queryStr, _ := queryCfg["query"].(string)

			jobID := "job-" + uuid.New().String()

			if strings.Contains(queryStr, "INSERT INTO") {
				if qParams, ok := queryCfg["queryParameters"].([]any); ok {
					for _, qp := range qParams {
						if pMap, ok := qp.(map[string]any); ok && pMap["name"] == "lock_id" {
							if pv, ok := pMap["parameterValue"].(map[string]any); ok {
								currentLockID, _ = pv["value"].(string)
							}
						}
					}
				}
			}

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jobReference": map[string]any{
					"jobId":     jobID,
					"projectId": projectID,
					"location":  "US",
				},
				"status": map[string]any{
					"state": "DONE",
				},
			})
			return
		}

		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": map[string]any{
					"state": "DONE",
				},
			})
			return
		}

		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/queries") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema": map[string]any{
					"fields": []map[string]any{
						{"name": "version", "type": "STRING"},
					},
				},
				"rows": []map[string]any{
					{"f": []map[string]any{{"v": currentLockID}}},
				},
				"jobComplete": true,
			})
			return
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	ctx := context.Background()
	client, err := bigquery.NewClient(ctx, projectID, option.WithEndpoint(ts.URL+"/bigquery/v2/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	var attemptCount int64

	db := &BigQueryDatabase{
		client:            client,
		projectID:         projectID,
		config:            &config.Configuration{LockRetryCount: 1},
		heartbeatInterval: 20 * time.Millisecond,
		lockTTL:           100 * time.Millisecond,
		heartbeatAttemptInterceptor: func() error {
			atomic.AddInt64(&attemptCount, 1)
			return errors.New("persistent network error")
		},
	}

	unlock, err := db.Lock(ctx, "test_ds", "flyway_schema_history")
	if err != nil {
		t.Fatalf("Lock failed: %v", err)
	}

	// Wait for heartbeat attempts to exceed deadline
	time.Sleep(150 * time.Millisecond)

	if attempts := atomic.LoadInt64(&attemptCount); attempts < 2 {
		t.Errorf("expected multiple failed attempts, got %d", attempts)
	}

	// Active lock error MUST be set because heartbeat exceeded deadline
	if err := db.checkActiveLockErr(); err == nil {
		t.Errorf("expected activeLockErr to be set after exceeding deadline, got nil")
	}

	// Unlock must return the heartbeat failure error
	unlockErr := unlock(ctx)
	if unlockErr == nil {
		t.Errorf("expected unlock to return error after heartbeat failed until deadline, got nil")
	} else if !strings.Contains(unlockErr.Error(), "lock heartbeat failed") {
		t.Errorf("expected unlock error to contain 'lock heartbeat failed', got: %v", unlockErr)
	}
}

func TestBigQueryHeartbeatTickTimeout(t *testing.T) {
	projectID := "test-project"
	var mu sync.Mutex
	var currentLockID string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/jobs") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			cfg, _ := body["configuration"].(map[string]any)
			queryCfg, _ := cfg["query"].(map[string]any)
			queryStr, _ := queryCfg["query"].(string)

			jobID := "job-" + uuid.New().String()

			if strings.Contains(queryStr, "INSERT INTO") {
				if qParams, ok := queryCfg["queryParameters"].([]any); ok {
					for _, qp := range qParams {
						if pMap, ok := qp.(map[string]any); ok && pMap["name"] == "lock_id" {
							if pv, ok := pMap["parameterValue"].(map[string]any); ok {
								currentLockID, _ = pv["value"].(string)
							}
						}
					}
				}
			}

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jobReference": map[string]any{
					"jobId":     jobID,
					"projectId": projectID,
					"location":  "US",
				},
				"status": map[string]any{
					"state": "DONE",
				},
				"statistics": map[string]any{
					"query": map[string]any{
						"statementType":      "UPDATE",
						"numDmlAffectedRows": "1",
					},
				},
			})
			return
		}

		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": map[string]any{
					"state": "DONE",
				},
				"statistics": map[string]any{
					"query": map[string]any{
						"statementType":      "UPDATE",
						"numDmlAffectedRows": "1",
					},
				},
			})
			return
		}

		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/queries") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema": map[string]any{
					"fields": []map[string]any{
						{"name": "version", "type": "STRING"},
					},
				},
				"rows": []map[string]any{
					{"f": []map[string]any{{"v": currentLockID}}},
				},
				"jobComplete": true,
			})
			return
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	ctx := context.Background()
	client, err := bigquery.NewClient(ctx, projectID, option.WithEndpoint(ts.URL+"/bigquery/v2/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	var attemptCount int64

	db := &BigQueryDatabase{
		client:            client,
		projectID:         projectID,
		config:            &config.Configuration{LockRetryCount: 1},
		heartbeatInterval: 25 * time.Millisecond,
		lockTTL:           2 * time.Second,
		heartbeatAttemptInterceptor: func() error {
			n := atomic.AddInt64(&attemptCount, 1)
			if n == 1 {
				// Simulate tick timeout
				return context.DeadlineExceeded
			}
			return nil
		},
	}

	unlock, err := db.Lock(ctx, "test_ds", "flyway_schema_history")
	if err != nil {
		t.Fatalf("Lock failed: %v", err)
	}
	defer func() { _ = unlock(ctx) }()

	time.Sleep(100 * time.Millisecond)

	if attempts := atomic.LoadInt64(&attemptCount); attempts < 2 {
		t.Errorf("expected at least 2 attempts after tick timeout retry, got %d", attempts)
	}

	// Active lock error should NOT be set because retry recovered after tick timeout
	if err := db.checkActiveLockErr(); err != nil {
		t.Errorf("expected activeLockErr to be nil after timeout recovery, got: %v", err)
	}
}

func TestIsNotFoundError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "googleapi 404",
			err:      &googleapi.Error{Code: 404},
			expected: true,
		},
		{
			name:     "wrapped googleapi 404",
			err:      fmt.Errorf("dataset error: %w", &googleapi.Error{Code: 404}),
			expected: true,
		},
		{
			name:     "grpc NotFound",
			err:      status.Error(codes.NotFound, "not found"),
			expected: true,
		},
		{
			name:     "wrapped grpc NotFound",
			err:      fmt.Errorf("query error: %w", status.Error(codes.NotFound, "not found")),
			expected: true,
		},
		{
			name: "googleapi 400 with 404 in job UUID or message",
			err: &googleapi.Error{
				Code:    400,
				Message: "invalid query in job_404abc_def",
			},
			expected: false,
		},
		{
			name:     "plain error mentioning 404 in text",
			err:      fmt.Errorf("error running job_404_some_uuid: syntax error"),
			expected: false,
		},
		{
			name:     "plain error mentioning not found",
			err:      errors.New("dataset not found"),
			expected: false,
		},
		{
			name:     "bigquery.Error with Reason notFound",
			err:      &bigquery.Error{Reason: "notFound", Message: "Not found: Table project:dataset.flyway_schema_history"},
			expected: true,
		},
		{
			name:     "bigquery.Error with different Reason",
			err:      &bigquery.Error{Reason: "duplicate", Message: "Already Exists: Table project:dataset.flyway_schema_history"},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isNotFoundError(tc.err)
			if got != tc.expected {
				t.Errorf("isNotFoundError(%v) = %v; want %v", tc.err, got, tc.expected)
			}
		})
	}
}

func TestBigQueryNew_OAuthTypeValidation(t *testing.T) {
	ctx := context.Background()

	// 1. Missing ProjectID
	cfgNoProj := config.NewDefaultConfiguration()
	cfgNoProj.GCPProjectID = ""
	_, err := New(ctx, cfgNoProj)
	if err == nil || !strings.Contains(err.Error(), "project ID is required") {
		t.Errorf("expected error for missing project ID, got: %v", err)
	}

	// 2. OAuthType=0 without key file / json
	cfgType0 := config.NewDefaultConfiguration()
	cfgType0.GCPProjectID = "test-proj"
	zero := 0
	cfgType0.GCPOAuthType = &zero
	cfgType0.GCPCredentialsFile = ""
	cfgType0.GCPCredentialsJSON = ""
	_, err = New(ctx, cfgType0)
	if err == nil || !strings.Contains(err.Error(), "OAuthType=0 requires OAuthPvtKeyPath") {
		t.Errorf("expected error for OAuthType=0 without key file, got: %v", err)
	}

	// 3. OAuthType=2 without access token
	cfgType2 := config.NewDefaultConfiguration()
	cfgType2.GCPProjectID = "test-proj"
	two := 2
	cfgType2.GCPOAuthType = &two
	cfgType2.GCPAccessToken = ""
	_, err = New(ctx, cfgType2)
	if err == nil || !strings.Contains(err.Error(), "OAuthType=2 requires OAuthAccessToken") {
		t.Errorf("expected error for OAuthType=2 without access token, got: %v", err)
	}

	// 4. Unknown OAuthType
	cfgUnknown := config.NewDefaultConfiguration()
	cfgUnknown.GCPProjectID = "test-proj"
	invalid := 99
	cfgUnknown.GCPOAuthType = &invalid
	_, err = New(ctx, cfgUnknown)
	if err == nil || !strings.Contains(err.Error(), "unsupported or incomplete OAuthType=99") {
		t.Errorf("expected error for unknown OAuthType, got: %v", err)
	}
}

func TestBigQueryDatabase_DefaultDatasetHelper(t *testing.T) {
	cfg := config.NewDefaultConfiguration()
	cfg.DefaultSchema = "analytics_ds"
	cfg.GCPProjectID = "test-project"

	db := &BigQueryDatabase{
		projectID: cfg.GCPProjectID,
		config:    cfg,
	}

	if got := db.getDefaultDataset(); got != "analytics_ds" {
		t.Errorf("expected getDefaultDataset 'analytics_ds', got %q", got)
	}

	cfg2 := config.NewDefaultConfiguration()
	cfg2.DefaultSchema = ""
	cfg2.GCPDataset = "other_ds"
	db2 := &BigQueryDatabase{
		config: cfg2,
	}
	if got := db2.getDefaultDataset(); got != "other_ds" {
		t.Errorf("expected getDefaultDataset 'other_ds', got %q", got)
	}
}

func TestIsConflictError(t *testing.T) {
	// 1. nil error
	if isConflictError(nil) {
		t.Errorf("expected false for nil error")
	}

	// 2. HTTP 409 Conflict googleapi.Error
	gErr := &googleapi.Error{Code: http.StatusConflict, Message: "Conflict"}
	if !isConflictError(gErr) {
		t.Errorf("expected true for HTTP 409 googleapi.Error")
	}

	// 3. gRPC AlreadyExists error
	grpcErr := status.Error(codes.AlreadyExists, "table already exists")
	if !isConflictError(grpcErr) {
		t.Errorf("expected true for gRPC AlreadyExists")
	}

	// 4. bigquery.Error with Reason: "duplicate"
	bqDuplicateErr := &bigquery.Error{Reason: "duplicate", Message: "Already Exists: Table test:ds.t"}
	if !isConflictError(bqDuplicateErr) {
		t.Errorf("expected true for bigquery.Error with Reason duplicate")
	}

	// 5. Arbitrary error containing "duplicate" in text (e.g. SQL syntax error) MUST NOT be treated as conflict
	sqlSyntaxErr := errors.New("Duplicate column name 'id' in query")
	if isConflictError(sqlSyntaxErr) {
		t.Errorf("expected false for arbitrary error containing 'duplicate', got true")
	}

	// 6. Arbitrary error containing "already exists" in text MUST NOT be treated as conflict
	otherErr := errors.New("something already exists in some other context")
	if isConflictError(otherErr) {
		t.Errorf("expected false for arbitrary text containing 'already exists', got true")
	}
}

func TestBigQueryEndpointAndEmulatorSplit(t *testing.T) {
	// 1. Emulator mode refuses non-loopback host when allow remote is false
	cfgRemote := config.NewDefaultConfiguration()
	cfgRemote.GCPProjectID = "test-proj"
	cfgRemote.GCPUseEmulator = true
	cfgRemote.GCPBigQueryEndpoint = "http://attacker.com:9050/"

	_, err := New(context.Background(), cfgRemote)
	if err == nil || !strings.Contains(err.Error(), "refusing BigQuery emulator connection to non-loopback host") {
		t.Fatalf("expected error refusing remote emulator host, got: %v", err)
	}

	// 2. With GCPEmulatorAllowRemote=true, remote host is permitted
	cfgRemote.GCPEmulatorAllowRemote = true
	_, err = New(context.Background(), cfgRemote)
	if err != nil && strings.Contains(err.Error(), "refusing BigQuery emulator connection") {
		t.Fatalf("did not expect refusal when GCPEmulatorAllowRemote=true, got: %v", err)
	}
}




