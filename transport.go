package statok

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Transport sends ready-to-ship payloads to the ingester endpoint. It must be
// safe for concurrent use by multiple goroutines.
type Transport interface {
	Send(ctx context.Context, payload *Payload) error
}

// Payload is the serialized form of a flushed batch.
type Payload struct {
	Counters []CounterEvent
	Values   []ValueEvent
}

func (p *Payload) empty() bool {
	return len(p.Counters) == 0 && len(p.Values) == 0
}

// CounterEvent represents an aggregated counter metric.
type CounterEvent struct {
	Metric    string
	Value     float64
	Labels    []string
	Timestamp int64
}

// ValueEvent represents a single value metric sample forwarded as-is.
type ValueEvent struct {
	Metric    string
	Value     float64
	Labels    []string
	Timestamp int64
}

// HTTPTransport is a minimal HTTP implementation of Transport for
// local development and agents that talk to the ingester's HTTP endpoint.
// Events are encoded using the dictionary-based line protocol (v2, seconds).
type HTTPTransport struct {
	Endpoint string
	Client   *http.Client
	Header   http.Header
	Logger   Logger
}

var defaultHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
}

// Send implements Transport.
func (t *HTTPTransport) Send(ctx context.Context, payload *Payload) error {
	if t == nil {
		return errors.New("statok: HTTP transport is nil")
	}
	if t.Endpoint == "" {
		return errors.New("statok: HTTP endpoint is empty")
	}
	if payload == nil || payload.empty() {
		return nil
	}
	body := encodeLinePayload(payload)
	if len(body) == 0 {
		return nil
	}
	bodyLen := len(body)
	client := t.Client
	if client == nil {
		client = defaultHTTPClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request %s %s: %w", http.MethodPost, t.Endpoint, err)
	}
	urlStr := req.URL.String()
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	for k, vs := range t.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", urlStr, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // ensure the connection can be reused
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if t.Logger != nil {
			_ = bodyLen
			//t.Logger.Printf("statok: sent %d bytes (%d counters, %d values)", bodyLen, len(payload.Counters), len(payload.Values))
		}
		return nil
	}
	return fmt.Errorf("POST %s: %s", urlStr, resp.Status)
}
