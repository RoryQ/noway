package database_test

import (
	"testing"

	"github.com/roryq/noway/pkg/database"
)

func TestIsLoopbackHost(t *testing.T) {
	tests := []struct {
		endpoint string
		want     bool
	}{
		{"localhost", true},
		{"localhost:9010", true},
		{"127.0.0.1", true},
		{"127.0.0.1:8080", true},
		{"[::1]:9010", true},
		{"http://localhost:9050/", true},
		{"https://127.0.0.1:9050/bigquery/v2/", true},
		{"spanner.p.googleapis.com:443", false},
		{"10.0.0.5:9010", false},
		{"192.168.1.1:8080", false},
		{"attacker.com", false},
	}

	for _, tt := range tests {
		got := database.IsLoopbackHost(tt.endpoint)
		if got != tt.want {
			t.Errorf("IsLoopbackHost(%q) = %v; want %v", tt.endpoint, got, tt.want)
		}
	}
}
