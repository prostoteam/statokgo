package statok

import (
	"strings"
	"testing"
)

func TestEncodeLinePayloadDictionary(t *testing.T) {
	payload := &Payload{
		Counters: []CounterEvent{
			{
				Metric:    "requests_total",
				Value:     1,
				Labels:    []string{Label("env", "prod")},
				Timestamp: 1730000000,
			},
			{
				Metric:    "requests_total",
				Value:     2,
				Labels:    []string{Label("env", "prod")},
				Timestamp: 1730000001,
			},
		},
		Values: []ValueEvent{
			{
				Metric:    "latency_ms",
				Value:     12.5,
				Timestamp: 1730000002,
			},
		},
	}

	data := encodeLinePayload(payload)
	if data == nil {
		t.Fatalf("encodeLinePayload returned nil")
	}

	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	want := []string{
		"H|2|s",
		"S|0|requests_total|env=prod",
		"S|1|latency_ms",
		"c|0|1|1730000000",
		"c|0|2|1730000001",
		"v|1|12.50|1730000002",
	}
	if len(lines) != len(want) {
		t.Fatalf("encoded lines = %d, want %d\n%s", len(lines), len(want), string(data))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d mismatch:\n got %q\nwant %q", i, lines[i], want[i])
		}
	}
}
