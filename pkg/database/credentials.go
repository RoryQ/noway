package database

import (
	"errors"
	"fmt"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"

	"github.com/roryq/noway/pkg/config"
)

// CredentialOptions resolves GCP client credentials options for BigQuery and Spanner.
//
// Rules:
// - If no OAuthType is specified, but an explicit credentials file or JSON is provided,
//   it defaults to OAuthType=0 (Service Account Key).
// - OAuthType=0 requires a key file (OAuthPvtKeyPath / gcpCredentialsFile) or gcpCredentialsJson.
// - OAuthType=2 requires OAuthAccessToken.
// - OAuthType=3 uses Application Default Credentials (ADC) and rejects explicit credentials files/JSON.
func CredentialOptions(cfg *config.Configuration) ([]option.ClientOption, error) {
	hasExplicit := cfg.GCPCredentialsJSON != "" || cfg.GCPCredentialsFile != ""

	oauthType := 3 // JDBC default: Application Default Credentials
	if cfg.GCPOAuthType != nil {
		oauthType = *cfg.GCPOAuthType
	} else if hasExplicit {
		// An explicit key file or JSON without OAuthType set means OAuthType=0
		oauthType = 0
	}

	switch oauthType {
	case 0: // service account key
		switch {
		case cfg.GCPCredentialsJSON != "":
			return []option.ClientOption{option.WithCredentialsJSON([]byte(cfg.GCPCredentialsJSON))}, nil
		case cfg.GCPCredentialsFile != "":
			return []option.ClientOption{option.WithCredentialsFile(cfg.GCPCredentialsFile)}, nil
		}
		return nil, errors.New("OAuthType=0 requires OAuthPvtKeyPath or gcpCredentialsJson")
	case 2: // pre-generated access token
		if cfg.GCPAccessToken == "" {
			return nil, errors.New("OAuthType=2 requires OAuthAccessToken")
		}
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.GCPAccessToken})
		return []option.ClientOption{option.WithTokenSource(ts)}, nil
	case 3: // Application Default Credentials
		if hasExplicit {
			return nil, errors.New("OAuthType=3 uses Application Default Credentials; remove gcpCredentialsFile/gcpCredentialsJson or set OAuthType=0")
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("OAuthType=%d is not supported", oauthType)
	}
}
