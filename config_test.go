package statok

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestEndpointFromHost(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"statok.dev0101.xyz", "https://statok.dev0101.xyz/api/i/batch"},
		{"https://statok.dev0101.xyz", "https://statok.dev0101.xyz/api/i/batch"},
		{"http://localhost:8085", "http://localhost:8085/api/i/batch"},
		{"https://collector.example.com/api/i/batch", "https://collector.example.com/api/i/batch"},
	}
	for _, tt := range tests {
		if got := EndpointFromHost(tt.in); got != tt.want {
			t.Fatalf("EndpointFromHost(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

type noopTransport struct{}

func (noopTransport) Send(context.Context, *Payload) error { return nil }

func TestApplyDefaultsRequiresAPIKeyForHTTPTransport(t *testing.T) {
	cfg := Config{
		Endpoint: "https://collector.example.com/api/i/batch",
	}
	err := cfg.applyDefaults()
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("applyDefaults() error = %v, want %v", err, ErrMissingAPIKey)
	}
}

func TestApplyDefaultsAllowsCustomNonHTTPTransportWithoutAPIKey(t *testing.T) {
	cfg := Config{
		Transport: noopTransport{},
	}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults() error = %v, want nil", err)
	}
}

func TestApplyDefaultsAllowsOpaqueAPIKey(t *testing.T) {
	tests := []struct {
		name   string
		apiKey string
	}{
		{name: "bearer_prefix", apiKey: "Bearer 123_secret"},
		{name: "space_inside", apiKey: "123 sec"},
		{name: "leading_trailing_whitespace", apiKey: " 123_xxx "},
		{name: "client_id_zero", apiKey: "0_secret"},
		{name: "non_numeric_client_id", apiKey: "abc_secret"},
		{name: "missing_separator", apiKey: "123secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Endpoint: "https://collector.example.com/api/i/batch",
				APIKey:   tt.apiKey,
			}
			if err := cfg.applyDefaults(); err != nil {
				t.Fatalf("applyDefaults() error = %v, want nil", err)
			}
			if cfg.APIKey != tt.apiKey {
				t.Fatalf("cfg.APIKey = %q, want %q", cfg.APIKey, tt.apiKey)
			}
		})
	}
}

func TestApplyDefaultsValidAPIKey(t *testing.T) {
	cfg := Config{
		Endpoint: "https://collector.example.com/api/i/batch",
		APIKey:   "123_xxx",
	}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults() error = %v, want nil", err)
	}
	if cfg.APIKey != "123_xxx" {
		t.Fatalf("cfg.APIKey = %q, want %q", cfg.APIKey, "123_xxx")
	}
	ht, ok := cfg.Transport.(*HTTPTransport)
	if !ok {
		t.Fatalf("cfg.Transport type = %T, want *HTTPTransport", cfg.Transport)
	}
	if ht.APIKey != "123_xxx" {
		t.Fatalf("transport API key = %q, want %q", ht.APIKey, "123_xxx")
	}
}

func TestApplyDefaultsAllowsUnderscoreInAPIKeySecret(t *testing.T) {
	cfg := Config{
		Endpoint: "https://collector.example.com/api/i/batch",
		APIKey:   "123_secret_part_with_underscores",
	}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults() error = %v, want nil", err)
	}
}

func TestApplyDefaultsPropagatesAPIKeyToExistingHTTPTransport(t *testing.T) {
	ht := &HTTPTransport{
		Endpoint: "https://collector.example.com/api/i/batch",
	}
	cfg := Config{
		APIKey:    "42_secret",
		Transport: ht,
	}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults() error = %v, want nil", err)
	}
	if ht.APIKey != "42_secret" {
		t.Fatalf("transport API key = %q, want %q", ht.APIKey, "42_secret")
	}
}

func TestApplyDefaultsKeepsExistingHTTPTransportAPIKey(t *testing.T) {
	ht := &HTTPTransport{
		Endpoint: "https://collector.example.com/api/i/batch",
		APIKey:   "99_existing",
	}
	cfg := Config{
		APIKey:    "42_secret",
		Transport: ht,
	}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults() error = %v, want nil", err)
	}
	if ht.APIKey != "99_existing" {
		t.Fatalf("transport API key = %q, want %q", ht.APIKey, "99_existing")
	}
}

func TestApplyDefaultsRejectsAuthorizationHeaderConflict(t *testing.T) {
	ht := &HTTPTransport{
		Endpoint: "https://collector.example.com/api/i/batch",
		Header: http.Header{
			"Authorization": []string{"Bearer custom"},
		},
	}
	cfg := Config{
		APIKey:    "42_secret",
		Transport: ht,
	}
	err := cfg.applyDefaults()
	if !errors.Is(err, ErrAPIKeyAuthorizationConflict) {
		t.Fatalf("applyDefaults() error = %v, want %v", err, ErrAPIKeyAuthorizationConflict)
	}
}
