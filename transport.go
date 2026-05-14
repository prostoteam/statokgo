package statok

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
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
	Uniques  []UniqueEvent
}

func (p *Payload) empty() bool {
	return len(p.Counters) == 0 && len(p.Values) == 0 && len(p.Uniques) == 0
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

// UniqueEvent represents one unique metric occurrence.
type UniqueEvent struct {
	Metric    string
	UniqueID  string
	Labels    []string
	Timestamp int64
}

// HTTPTransport is a minimal HTTP implementation of Transport for
// local development and agents that talk to the ingester's HTTP endpoint.
// Events are encoded using reusable dictionary line protocol v4 (seconds).
type HTTPTransport struct {
	Endpoint string
	APIKey   string
	Client   *http.Client
	Header   http.Header
	Logger   Logger
	mu       sync.Mutex
	dict     *dictionaryState
	// StopStatusCodes controls which HTTP statuses are treated as non-retryable.
	// When matched, Send returns StopIngestError and client worker disables
	// further transport sends (default: [401]).
	StopStatusCodes []int
	// StopResponseCodes controls which API error body `code` values are treated
	// as non-retryable (default: ["unauthorized"]).
	StopResponseCodes []string
}

// StopIngestError marks a transport failure as non-retryable for the active client.
// The background worker should stop sending further batches after this error.
type StopIngestError struct {
	Code int
	Err  error
}

type HTTPTransportError struct {
	Method       string
	Endpoint     string
	StatusCode   int
	Status       string
	ResponseCode string
	Detail       string
	RequestBytes int
}

func (e *HTTPTransportError) Error() string {
	if e == nil {
		return ""
	}
	method := e.Method
	if method == "" {
		method = http.MethodPost
	}
	if e.Status != "" {
		if e.Detail != "" {
			if e.RequestBytes > 0 {
				return fmt.Sprintf("%s %s: %s: %s (request_bytes=%d)", method, e.Endpoint, e.Status, e.Detail, e.RequestBytes)
			}
			return fmt.Sprintf("%s %s: %s: %s", method, e.Endpoint, e.Status, e.Detail)
		}
		if e.RequestBytes > 0 {
			return fmt.Sprintf("%s %s: %s (request_bytes=%d)", method, e.Endpoint, e.Status, e.RequestBytes)
		}
		return fmt.Sprintf("%s %s: %s", method, e.Endpoint, e.Status)
	}
	if e.Detail != "" {
		if e.RequestBytes > 0 {
			return fmt.Sprintf("%s %s: %s (request_bytes=%d)", method, e.Endpoint, e.Detail, e.RequestBytes)
		}
		return fmt.Sprintf("%s %s: %s", method, e.Endpoint, e.Detail)
	}
	if e.RequestBytes > 0 {
		return fmt.Sprintf("%s %s failed (request_bytes=%d)", method, e.Endpoint, e.RequestBytes)
	}
	return fmt.Sprintf("%s %s failed", method, e.Endpoint)
}

func (e *StopIngestError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Code > 0 {
		return fmt.Sprintf("statok: stop ingesting after HTTP %d", e.Code)
	}
	return "statok: stop ingesting"
}

func (e *StopIngestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StopIngest reports that the error should disable future sends.
func (e *StopIngestError) StopIngest() bool { return true }

// IsStopIngestError reports whether err is marked as non-retryable.
func IsStopIngestError(err error) bool {
	var marker interface{ StopIngest() bool }
	return errors.As(err, &marker) && marker.StopIngest()
}

var defaultHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
}

const maxErrorBodyBytes = 4096

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
	t.mu.Lock()
	defer t.mu.Unlock()

	t.ensureDictionaryLocked()

	body := encodeLinePayloadV4(payload, t.dict, false)
	if len(body) == 0 {
		return nil
	}
	result, err := t.sendBody(ctx, body)
	if err == nil {
		return nil
	}

	if result.statusCode == http.StatusRequestEntityTooLarge {
		t.resetDictionaryLocked()
		return err
	}
	if result.statusCode == http.StatusConflict && result.responseCode == "unknown_series_dictionary" {
		t.resetDictionaryLocked()
		resyncBody := encodeLinePayloadV4(payload, t.dict, false)
		if len(resyncBody) == 0 {
			return err
		}
		retryResult, retryErr := t.sendBody(ctx, resyncBody)
		if retryResult.statusCode == http.StatusRequestEntityTooLarge {
			t.resetDictionaryLocked()
		}
		return retryErr
	}
	return err
}

func (t *HTTPTransport) ensureDictionaryLocked() {
	if t.dict == nil || len(t.dict.series) >= defaultMaxDictionarySeries {
		t.resetDictionaryLocked()
	}
}

func (t *HTTPTransport) resetDictionaryLocked() {
	t.dict = newDictionaryState()
}

type sendResult struct {
	statusCode   int
	responseCode string
}

func (t *HTTPTransport) sendBody(ctx context.Context, body []byte) (sendResult, error) {
	result := sendResult{}

	client := t.Client
	if client == nil {
		client = defaultHTTPClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, bytes.NewReader(body))
	if err != nil {
		return result, fmt.Errorf("build request %s %s: %w", http.MethodPost, t.Endpoint, err)
	}
	urlStr := req.URL.String()
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	for k, vs := range t.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if t.APIKey != "" {
		req.Header.Set("Authorization", t.APIKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("POST %s: %w", urlStr, err)
	}
	defer resp.Body.Close()

	result.statusCode = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		io.Copy(io.Discard, resp.Body)
		return result, nil
	}

	detail, responseCode := readErrorBody(resp.Body)
	result.responseCode = responseCode
	sendErr := &HTTPTransportError{
		Method:       http.MethodPost,
		Endpoint:     urlStr,
		StatusCode:   resp.StatusCode,
		Status:       resp.Status,
		ResponseCode: responseCode,
		Detail:       detail,
		RequestBytes: len(body),
	}
	if t.shouldStopOnStatus(resp.StatusCode) || t.shouldStopOnResponseCode(responseCode) {
		return result, &StopIngestError{
			Code: resp.StatusCode,
			Err:  sendErr,
		}
	}
	return result, sendErr
}

func readErrorBody(r io.Reader) (detail string, responseCode string) {
	bodyBytes, _ := io.ReadAll(io.LimitReader(r, maxErrorBodyBytes))
	if len(bodyBytes) == 0 {
		return "", ""
	}
	io.Copy(io.Discard, r) // drain the rest for keep-alive
	return compactErrorBody(string(bodyBytes)), extractResponseCode(bodyBytes)
}

func compactErrorBody(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return strings.Join(strings.Fields(raw), " ")
}

func extractResponseCode(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(payload.Code))
}

func (t *HTTPTransport) shouldStopOnStatus(status int) bool {
	codes := t.StopStatusCodes
	if len(codes) == 0 {
		return status == defaultStopStatusCode
	}
	for _, code := range codes {
		if code == status {
			return true
		}
	}
	return false
}

func (t *HTTPTransport) shouldStopOnResponseCode(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return false
	}
	configured := t.StopResponseCodes
	if len(configured) == 0 {
		for _, candidate := range defaultStopResponseCodes {
			if strings.EqualFold(strings.TrimSpace(candidate), code) {
				return true
			}
		}
		return false
	}
	for _, candidate := range configured {
		if strings.EqualFold(strings.TrimSpace(candidate), code) {
			return true
		}
	}
	return false
}
