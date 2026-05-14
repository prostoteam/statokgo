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

	state := newDictionaryState()
	want := encodeLinePayloadV4(payload, state, false)
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
	if !bytes.HasPrefix(want, []byte("H|4|s|")) {
		t.Fatalf("payload missing v4 header: %q", string(want[:min(len(want), 12)]))
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

	if !bytes.HasPrefix(received, []byte("H|4|s|")) {
		t.Fatalf("sent payload missing v4 header")
	}
	if gotLines := bytes.Count(received, []byte{'\n'}); gotLines != wantLines {
		t.Fatalf("sent payload lines = %d, want %d", gotLines, wantLines)
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

func TestHTTPTransportSendSetsAuthorizationHeader(t *testing.T) {
	const apiKey = "123_secret-token"
	var gotAuthorization string
	var gotCustomHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotCustomHeader = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	tr := &HTTPTransport{
		Endpoint: srv.URL,
		APIKey:   apiKey,
		Header: http.Header{
			"X-Custom": []string{"1"},
		},
	}
	payload := &Payload{
		Counters: []CounterEvent{{
			Metric:    "counter_metric_1",
			Value:     1,
			Labels:    []string{Label("env", "test")},
			Timestamp: 1730000000,
		}},
	}
	if err := tr.Send(context.Background(), payload); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotAuthorization != apiKey {
		t.Fatalf("Authorization = %q, want %q", gotAuthorization, apiKey)
	}
	if gotCustomHeader != "1" {
		t.Fatalf("X-Custom = %q, want %q", gotCustomHeader, "1")
	}
}

func TestHTTPTransportAuthorizationIntegration(t *testing.T) {
	const apiKey = "123_secret-token"
	payload := &Payload{
		Counters: []CounterEvent{{
			Metric:    "counter_metric_1",
			Value:     1,
			Labels:    []string{Label("env", "test")},
			Timestamp: 1730000000,
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	missingAuthTransport := &HTTPTransport{Endpoint: srv.URL}
	if err := missingAuthTransport.Send(context.Background(), payload); err == nil {
		t.Fatalf("Send() error = nil, want unauthorized error")
	} else if !strings.Contains(err.Error(), "401") {
		t.Fatalf("Send() error = %v, want 401 status", err)
	}

	withAuthTransport := &HTTPTransport{
		Endpoint: srv.URL,
		APIKey:   apiKey,
	}
	if err := withAuthTransport.Send(context.Background(), payload); err != nil {
		t.Fatalf("Send() error = %v, want nil", err)
	}
}

func TestHTTPTransportResyncsDictionaryOnUnknownDictionary(t *testing.T) {
	payload := &Payload{
		Counters: []CounterEvent{{
			Metric:    "counter_metric_1",
			Value:     1,
			Labels:    []string{Label("env", "test")},
			Timestamp: 1730000000,
		}},
	}

	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		body, _ := io.ReadAll(r.Body)
		lines := strings.Split(strings.TrimSpace(string(body)), "\n")
		hasSeriesDef := false
		for _, line := range lines {
			if strings.HasPrefix(line, "S|") {
				hasSeriesDef = true
				break
			}
		}
		if requestCount == 1 {
			if !hasSeriesDef {
				t.Fatalf("first request must define series dictionary")
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if requestCount == 2 {
			if hasSeriesDef {
				t.Fatalf("second request must reuse cached dictionary")
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"code":"unknown_series_dictionary","message":"unknown series dictionary"}`)
			return
		}
		if requestCount == 3 {
			if !hasSeriesDef {
				t.Fatalf("third request must resync dictionary after conflict")
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		t.Fatalf("unexpected extra request %d", requestCount)
	}))
	defer srv.Close()

	tr := &HTTPTransport{Endpoint: srv.URL}

	if err := tr.Send(context.Background(), payload); err != nil {
		t.Fatalf("first Send() error = %v", err)
	}
	if err := tr.Send(context.Background(), payload); err != nil {
		t.Fatalf("second Send() error = %v", err)
	}
	if requestCount != 3 {
		t.Fatalf("request count = %d, want 3", requestCount)
	}
}

func TestHTTPTransportResetsDictionaryWhenCapExceeded(t *testing.T) {
	payload := &Payload{
		Counters: []CounterEvent{{
			Metric:    "fresh_metric",
			Value:     1,
			Labels:    []string{Label("env", "test")},
			Timestamp: 1730000000,
		}},
	}

	dict := newDictionaryState()
	originalSessionID := dict.sessionID
	for i := 0; i < defaultMaxDictionarySeries; i++ {
		metric := fmt.Sprintf("seed_metric_%d", i)
		key := seriesKey(metric, []string{Label("env", "seed")})
		dict.seriesMap[key] = i
		dict.series = append(dict.series, seriesDef{
			metric: metric,
			labels: []string{Label("env", "seed")},
		})
	}

	var received string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	tr := &HTTPTransport{
		Endpoint: srv.URL,
		dict:     dict,
	}
	if err := tr.Send(context.Background(), payload); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if tr.dict == nil {
		t.Fatal("transport dictionary = nil, want reset dictionary")
	}
	if tr.dict.sessionID == originalSessionID {
		t.Fatalf("dictionary sessionID = %q, want reset from original", tr.dict.sessionID)
	}
	if len(tr.dict.series) != 1 {
		t.Fatalf("dictionary series count = %d, want 1 after reset", len(tr.dict.series))
	}
	if strings.Contains(received, "seed_metric_0") {
		t.Fatalf("request body unexpectedly included stale dictionary entries: %q", received)
	}
	if !strings.Contains(received, "S|0|fresh_metric|") {
		t.Fatalf("request body missing fresh dictionary entry: %q", received)
	}
}

func TestHTTPTransportResetsDictionaryOnRequestEntityTooLarge(t *testing.T) {
	payload := &Payload{
		Counters: []CounterEvent{{
			Metric:    "requests",
			Value:     1,
			Labels:    []string{Label("env", "test")},
			Timestamp: 1730000000,
		}},
	}

	dict := newDictionaryState()
	dict.seriesMap[seriesKey("requests", []string{Label("env", "test")})] = 0
	dict.series = append(dict.series, seriesDef{
		metric: "requests",
		labels: []string{Label("env", "test")},
	})
	originalSessionID := dict.sessionID

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
	}))
	defer srv.Close()

	tr := &HTTPTransport{
		Endpoint: srv.URL,
		dict:     dict,
	}
	err := tr.Send(context.Background(), payload)
	if err == nil {
		t.Fatal("Send() error = nil, want 413 error")
	}
	if !strings.Contains(err.Error(), "request_bytes=") {
		t.Fatalf("error %q missing request_bytes detail", err)
	}
	if tr.dict == nil {
		t.Fatal("transport dictionary = nil, want reset dictionary")
	}
	if tr.dict.sessionID == originalSessionID {
		t.Fatalf("dictionary sessionID = %q, want reset from original", tr.dict.sessionID)
	}
	if len(tr.dict.series) != 0 {
		t.Fatalf("dictionary series count = %d, want 0 after 413 reset", len(tr.dict.series))
	}
}
