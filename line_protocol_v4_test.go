package statok

import (
	"strings"
	"testing"
)

func TestEncodeLinePayloadV4SendsOnlyNewSeriesDefinitions(t *testing.T) {
	state := newDictionaryState()

	firstPayload := &Payload{
		Counters: []CounterEvent{{
			Metric:    "requests",
			Value:     1,
			Labels:    []string{Label("env", "prod")},
			Timestamp: 1730000000,
		}},
	}
	firstBody := string(encodeLinePayloadV4(firstPayload, state, false))
	if count := strings.Count(firstBody, "\nS|"); count != 1 {
		t.Fatalf("first payload series definitions = %d, want 1; body=%q", count, firstBody)
	}

	secondPayload := &Payload{
		Counters: []CounterEvent{
			{
				Metric:    "requests",
				Value:     2,
				Labels:    []string{Label("env", "prod")},
				Timestamp: 1730000001,
			},
			{
				Metric:    "latency_ms",
				Value:     123,
				Labels:    []string{Label("env", "prod")},
				Timestamp: 1730000001,
			},
		},
	}
	secondBody := string(encodeLinePayloadV4(secondPayload, state, false))
	if count := strings.Count(secondBody, "\nS|"); count != 1 {
		t.Fatalf("second payload series definitions = %d, want 1; body=%q", count, secondBody)
	}
	if !strings.Contains(secondBody, "\nS|1|latency_ms|") {
		t.Fatalf("second payload missing new series definition; body=%q", secondBody)
	}
	if strings.Contains(secondBody, "\nS|0|requests|") {
		t.Fatalf("second payload unexpectedly resent cached series definition; body=%q", secondBody)
	}
}
