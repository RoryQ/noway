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

// SpannerConnectionParams parsed from Cloud Spanner JDBC or custom URLs.
type SpannerConnectionParams struct {
	ProjectID          string
	InstanceID         string
	DatabaseID         string
	Endpoint           string
	CredentialsFile    string
	UsePlainText       bool
	AutoConfigEmulator bool
}

// IsSpannerURL checks whether a raw URL targets Google Cloud Spanner.
func IsSpannerURL(rawURL string) bool {
	return strings.HasPrefix(rawURL, "jdbc:cloudspanner:") ||
		strings.HasPrefix(rawURL, "spanner://")
}

// ParseJDBCSpannerURL parses a Cloud Spanner JDBC or custom URL.
// Supports:
// - jdbc:cloudspanner:/projects/{project}/instances/{instance}/databases/{database}
// - jdbc:cloudspanner://localhost:9010/projects/{project}/instances/{instance}/databases/{database}
// - spanner://projects/{project}/instances/{instance}/databases/{database}
// - spanner://{project}/{instance}/{database}
func ParseJDBCSpannerURL(rawURL string) (*SpannerConnectionParams, error) {
	params := &SpannerConnectionParams{}
	if rawURL == "" {
		return params, nil
	}

	trimmed := rawURL

	// Strip prefix
	if strings.HasPrefix(trimmed, "jdbc:cloudspanner:") {
		trimmed = strings.TrimPrefix(trimmed, "jdbc:cloudspanner:")
	} else if strings.HasPrefix(trimmed, "spanner://") {
		trimmed = strings.TrimPrefix(trimmed, "spanner://")
	} else {
		return nil, fmt.Errorf("not a Cloud Spanner URL: %s", rawURL)
	}

	// Extract query string or semicolon parameters if present
	var queryStr string
	if qIdx := strings.IndexAny(trimmed, "?;"); qIdx != -1 {
		queryStr = trimmed[qIdx+1:]
		trimmed = trimmed[:qIdx]
	}

	// Parse host/endpoint if present (e.g., //localhost:9010/projects/...)
	if strings.HasPrefix(trimmed, "//") {
		trimmed = strings.TrimPrefix(trimmed, "//")
		slashIdx := strings.Index(trimmed, "/")
		if slashIdx != -1 {
			params.Endpoint = trimmed[:slashIdx]
			trimmed = trimmed[slashIdx:]
		}
	}

	// Clean path
	trimmed = strings.Trim(trimmed, "/")
	parts := strings.Split(trimmed, "/")

	if len(parts) >= 6 && parts[0] == "projects" && parts[2] == "instances" && parts[4] == "databases" {
		params.ProjectID = parts[1]
		params.InstanceID = parts[3]
		params.DatabaseID = parts[5]
	} else if len(parts) == 3 {
		// Shorthand: project/instance/database
		params.ProjectID = parts[0]
		params.InstanceID = parts[1]
		params.DatabaseID = parts[2]
	}

	// Parse query params (supports both ?a=b&c=d and ;a=b;c=d)
	if queryStr != "" {
		kvPairs := strings.FieldsFunc(queryStr, func(r rune) bool {
			return r == '&' || r == ';'
		})
		for _, pair := range kvPairs {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) != 2 {
				continue
			}
			k := strings.ToLower(strings.TrimSpace(kv[0]))
			v := strings.TrimSpace(kv[1])

			switch k {
			case "credentials", "credentials_file", "keyfile", "oauthpkeyfile":
				params.CredentialsFile = v
			case "autoconfigemulator", "auto_config_emulator":
				params.AutoConfigEmulator = strings.EqualFold(v, "true") || v == "1"
			case "useplaintext", "use_plain_text":
				params.UsePlainText = strings.EqualFold(v, "true") || v == "1"
			case "endpoint", "host":
				params.Endpoint = v
			case "project", "projectid", "project_id":
				if params.ProjectID == "" {
					params.ProjectID = v
				}
			case "instance", "instanceid", "instance_id":
				if params.InstanceID == "" {
					params.InstanceID = v
				}
			case "database", "databaseid", "database_id":
				if params.DatabaseID == "" {
					params.DatabaseID = v
				}
			}
		}
	}

	return params, nil
}

