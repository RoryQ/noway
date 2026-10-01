package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// BigQueryConnectionParams parsed from JDBC or GCP connection strings.
type BigQueryConnectionParams struct {
	ProjectID          string
	DefaultDataset     string
	Location           string
	OAuthType          int    // 0 = Service Account, 1 = User, 2 = AccessToken, 3 = ADC
	ServiceAccountFile string // OAuthPKeyFile / KeyFile
	ServiceAccountJSON string // Key JSON string
	ServiceAccountEmail string
	AccessToken        string
	TimeoutSeconds     int
	Endpoint           string
}

// ParseJDBCBigQueryURL parses a JDBC or custom BigQuery URL into connection parameters.
// Supports:
// - jdbc:bigquery://https://www.googleapis.com/bigquery/v2:443;ProjectId=proj;OAuthType=0;...
// - jdbc:bigquery:;ProjectId=proj;DefaultDataset=ds;...
// - bigquery://project-id/dataset_name?location=US
// - bigquery://project-id
func ParseJDBCBigQueryURL(rawURL string) (*BigQueryConnectionParams, error) {
	params := &BigQueryConnectionParams{
		OAuthType: 3, // Default to ADC
	}

	if rawURL == "" {
		return params, nil
	}

	// Case 1: Standard JDBC URL "jdbc:bigquery:..."
	if strings.HasPrefix(rawURL, "jdbc:bigquery:") {
		trimmed := strings.TrimPrefix(rawURL, "jdbc:bigquery:")
		// Strip leading "//https://www.googleapis.com/bigquery/v2:443" or similar if present
		if strings.HasPrefix(trimmed, "//") {
			trimmed = strings.TrimPrefix(trimmed, "//")
			semiIdx := strings.Index(trimmed, ";")
			if semiIdx != -1 {
				endpoint := trimmed[:semiIdx]
				params.Endpoint = endpoint
				trimmed = trimmed[semiIdx+1:]
			} else {
				params.Endpoint = trimmed
				trimmed = ""
			}
		} else if strings.HasPrefix(trimmed, ";") {
			trimmed = strings.TrimPrefix(trimmed, ";")
		}

		// Split by semicolons
		pairs := strings.Split(trimmed, ";")
		for _, pair := range pairs {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) != 2 {
				continue
			}
			key := strings.TrimSpace(kv[0])
			val := strings.TrimSpace(kv[1])

			switch strings.ToLower(key) {
			case "projectid", "project_id", "project":
				params.ProjectID = val
			case "defaultdataset", "default_dataset", "dataset", "schema":
				params.DefaultDataset = val
			case "location", "region":
				params.Location = val
			case "oauthtype", "oauth_type":
				if num, err := strconv.Atoi(val); err == nil {
					params.OAuthType = num
				}
			case "oauthpkeyfile", "oauth_pkey_file", "keyfile", "credentials_file":
				params.ServiceAccountFile = val
			case "oauthserviceacctemail", "oauth_service_acct_email", "service_account_email":
				params.ServiceAccountEmail = val
			case "oauthaccesstoken", "oauth_access_token", "access_token":
				params.AccessToken = val
			case "timeout":
				if num, err := strconv.Atoi(val); err == nil {
					params.TimeoutSeconds = num
				}
			}
		}

		return params, nil
	}

	// Case 2: Custom URI like "bigquery://my-project/my_dataset?location=US"
	if strings.HasPrefix(rawURL, "bigquery://") {
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, fmt.Errorf("invalid BigQuery URL: %w", err)
		}

		params.ProjectID = u.Host
		path := strings.TrimPrefix(u.Path, "/")
		if path != "" {
			params.DefaultDataset = path
		}

		q := u.Query()
		if loc := q.Get("location"); loc != "" {
			params.Location = loc
		}
		if ds := q.Get("dataset"); ds != "" {
			params.DefaultDataset = ds
		}
		if kf := q.Get("credentials_file"); kf != "" {
			params.ServiceAccountFile = kf
		}

		return params, nil
	}

	return params, nil
}
