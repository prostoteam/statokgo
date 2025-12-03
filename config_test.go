package statok

import "testing"

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
