package statok

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPTransportSendLargePayload(t *testing.T) {
	const (
		numCounters = 2000
		numValues   = 2000
		baseTS      = 1730000000
	)

	payload := &Payload{
		Counters: make([]CounterEvent, numCounters),
		Values:   make([]ValueEvent, numValues),
	}
	for i := 0; i < numCounters; i++ {
		payload.Counters[i] = CounterEvent{
			Metric:    fmt.Sprintf("counter_metric_%d", i),
			Value:     float64(i + 1),
			Labels:    []string{Label("env", "prod"), Label("idx", fmt.Sprintf("%d", i))},
			Timestamp: baseTS + int64(i),
		}
	}
	for i := 0; i < numValues; i++ {
		payload.Values[i] = ValueEvent{
			Metric:    fmt.Sprintf("value_metric_%d", i),
			Value:     float64(i) * 0.5,
			Labels:    []string{Label("env", "prod")},
			Timestamp: baseTS + int64(i),
		}
	}

	// Add a few metrics that exercise large label sets.
	longLabels := []string{
		Label("env", strings.Repeat("prod", 25)),
		Label("region", strings.Repeat("us-east-1", 10)),
		Label("service", strings.Repeat("payments", 15)),
		Label("host", strings.Repeat("hostA", 20)),
		Label("owner", strings.Repeat("team-observability", 6)),
		Label("tier", strings.Repeat("critical", 12)),
	}
	payload.Counters = append(payload.Counters, CounterEvent{
		Metric:    "counter_many_labels",
		Value:     1234,
		Labels:    longLabels,
		Timestamp: baseTS + 9999,
	})
	payload.Values = append(payload.Values, ValueEvent{
		Metric:    "value_many_labels",
		Value:     3.1415,
		Labels:    longLabels,
		Timestamp: baseTS + 9999,
	})

	want := encodeLinePayload(payload)
	if want == nil {
		t.Fatalf("expected encoded payload, got nil")
	}
	uniqueSeries := make(map[string]struct{})
	for _, c := range payload.Counters {
		uniqueSeries[seriesKey(c.Metric, c.Labels)] = struct{}{}
	}
	for _, v := range payload.Values {
		uniqueSeries[seriesKey(v.Metric, v.Labels)] = struct{}{}
	}
	totalCounters := len(payload.Counters)
	totalValues := len(payload.Values)
	wantLines := 1 + len(uniqueSeries) + totalCounters + totalValues
	if gotLines := bytes.Count(want, []byte{'\n'}); gotLines != wantLines {
		t.Fatalf("encoded payload lines = %d, want %d", gotLines, wantLines)
	}
	if !bytes.HasPrefix(want, []byte("H|2|s\n")) {
		t.Fatalf("payload missing header: %q", string(want[:min(len(want), 6)]))
	}

	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}
		received = append(received[:0], body...)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	tr := &HTTPTransport{Endpoint: srv.URL}
	if err := tr.Send(context.Background(), payload); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if len(received) != len(want) {
		t.Fatalf("sent payload length = %d, want %d", len(received), len(want))
	}
	if !bytes.Equal(received, want) {
		t.Fatalf("sent payload mismatched contents")
	}
}

func TestHTTPTransportErrorIncludesEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer srv.Close()

	endpoint := srv.URL + "/api/i/batch"
	tr := &HTTPTransport{Endpoint: endpoint}
	payload := &Payload{
		Counters: []CounterEvent{{
			Metric:    "counter_metric_1",
			Value:     1,
			Labels:    []string{Label("env", "test")},
			Timestamp: 1730000000,
		}},
	}
	err := tr.Send(context.Background(), payload)
	if err == nil {
		t.Fatalf("Send() error = nil, want error")
	}
	if !strings.Contains(err.Error(), endpoint) {
		t.Fatalf("error %q does not mention endpoint %q", err, endpoint)
	}
	if !strings.Contains(err.Error(), "405") {
		t.Fatalf("error %q does not mention status code", err)
	}
}
