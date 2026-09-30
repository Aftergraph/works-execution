package main

import (
	"net/http"
	"testing"
)

func TestValidateEnrollmentConfiguration(t *testing.T) {
	tests := []struct {
		name      string
		secret    string
		apiURL    string
		allowDev  bool
		wantError bool
	}{
		{name: "missing secret fails by default", apiURL: "http://127.0.0.1:8080", wantError: true},
		{name: "blank secret fails by default", secret: "  ", apiURL: "http://127.0.0.1:8080", wantError: true},
		{name: "configured secret supports remote API", secret: "configured", apiURL: "https://works.example"},
		{name: "configured secret refuses remote plaintext HTTP", secret: "configured", apiURL: "http://works.example", wantError: true},
		{name: "configured secret allows loopback HTTP", secret: "configured", apiURL: "http://127.0.0.1:8080"},
		{name: "explicit dev mode permits loopback without secret", apiURL: "http://127.0.0.1:8080", allowDev: true},
		{name: "explicit dev mode permits localhost", apiURL: "http://localhost:8080", allowDev: true},
		{name: "explicit dev mode permits IPv6 loopback", apiURL: "http://[::1]:8080", allowDev: true},
		{name: "explicit dev mode refuses remote API", secret: "configured", apiURL: "https://works.example", allowDev: true, wantError: true},
		{name: "unsupported API scheme is rejected", secret: "configured", apiURL: "ftp://works.example", wantError: true},
		{name: "embedded API credentials are rejected", secret: "configured", apiURL: "https://user:password@works.example", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEnrollmentConfiguration(tt.secret, tt.apiURL, tt.allowDev)
			if (err != nil) != tt.wantError {
				t.Fatalf("validateEnrollmentConfiguration() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestValidateWorkerAuthenticationWithMTLS(t *testing.T) {
	complete := []string{"ca.pem", "worker.pem", "worker-key.pem", "works-mtls.aftergraph.org"}
	if err := validateWorkerAuthentication("", "https://127.0.0.1:18080", false, true, complete[0], complete[1], complete[2], complete[3]); err != nil {
		t.Fatalf("complete loopback HTTPS mTLS configuration rejected: %v", err)
	}
	if err := validateWorkerAuthentication("", "http://127.0.0.1:18080", false, true, complete[0], complete[1], complete[2], complete[3]); err == nil {
		t.Fatal("mTLS accepted plaintext HTTP")
	}
	if err := validateWorkerAuthentication("", "https://works-mtls.aftergraph.org", false, true, complete[0], complete[1], complete[2], ""); err == nil {
		t.Fatal("mTLS accepted a missing server name")
	}
	if err := validateWorkerAuthentication("", "https://127.0.0.1:18080", true, true, complete[0], complete[1], complete[2], complete[3]); err == nil {
		t.Fatal("mTLS accepted unauthenticated development mode")
	}
}

func TestAllowUnauthenticatedFallbackRequiresExplicitLoopbackDevMode(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		allowDev bool
		want     bool
	}{
		{name: "503 without opt-in is denied", status: http.StatusServiceUnavailable},
		{name: "503 with opt-in is allowed", status: http.StatusServiceUnavailable, allowDev: true, want: true},
		{name: "401 remains denied in dev mode", status: http.StatusUnauthorized, allowDev: true},
		{name: "500 remains a transient error", status: http.StatusInternalServerError, allowDev: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allowUnauthenticatedFallback(tt.status, tt.allowDev); got != tt.want {
				t.Fatalf("allowUnauthenticatedFallback() = %v, want %v", got, tt.want)
			}
		})
	}
}
