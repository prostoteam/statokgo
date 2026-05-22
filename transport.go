package statok

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
	BatchID  string
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
	Workload string
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

type workloadHTTPTransport struct {
	base     *HTTPTransport
	workload string
}

func (t *workloadHTTPTransport) Send(ctx context.Context, payload *Payload) error {
	if t == nil || t.base == nil {
		return errors.New("statok: HTTP transport is nil")
	}
	return t.base.send(ctx, payload, t.workload)
}

// StopIngestError marks a transport failure as non-retryable for the active client.
// The background worker should stop sending further batches after this error.
type StopIngestError struct {
	Code int
	Err  error
}

type HTTPTransportError struct {
	Method                       string
	Endpoint                     string
	BatchID                      string
	StatusCode                   int
	Status                       string
	ResponseCode                 string
	Detail                       string
	Accepted                     int
	Dropped                      int
	Rejected                     int
	RequestBytes                 int
	DictionarySession            string
	DictionaryRevision           uint64
	DictionarySeries             int
	SeriesDefinitions            int
	SeriesDefinitionBytes        int
	EventLines                   int
	EventBytes                   int
	EventSeriesMinID             int
	EventSeriesMaxID             int
	EventSeriesSamples           string
	LargestSeriesMetric          string
	LargestSeriesDefinitionBytes int
	LargestSeriesLabels          int
	LargestLabelKey              string
	LargestLabelBytes            int
	LargestLabelValuePrefix      string
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
	return t.send(ctx, payload, t.Workload)
}

func (t *HTTPTransport) send(ctx context.Context, payload *Payload, workload string) error {
	if t == nil {
		return errors.New("statok: HTTP transport is nil")
	}
	if t.Endpoint == "" {
		return errors.New("statok: HTTP endpoint is empty")
	}
	if err := validateWorkload(workload); err != nil {
		return err
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
	result, err := t.sendBody(ctx, body, workload, payload.BatchID)
	if err == nil {
		return nil
	}

	if result.statusCode == http.StatusRequestEntityTooLarge {
		t.resetDictionaryLocked()
		return err
	}
	if result.statusCode == http.StatusConflict && result.responseCode == "unknown_series_dictionary" {
		if t.Logger != nil {
			t.Logger.Printf("statok: ingester dictionary miss, resetting local dictionary and retrying; %s", summarizeTransportError(err))
		}
		t.resetDictionaryLocked()
		resyncBody := encodeLinePayloadV4(payload, t.dict, false)
		if len(resyncBody) == 0 {
			return err
		}
		retryResult, retryErr := t.sendBody(ctx, resyncBody, workload, payload.BatchID)
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
	accepted     int
	dropped      int
	rejected     int
}

func (t *HTTPTransport) sendBody(ctx context.Context, body []byte, workload string, batchID string) (sendResult, error) {
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
	if workload = strings.TrimSpace(workload); workload != "" {
		req.Header.Set(workloadHeaderName, workload)
	}
	if batchID = strings.TrimSpace(batchID); batchID != "" {
		req.Header.Set(batchIDHeaderName, batchID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("POST %s: %w", urlStr, err)
	}
	defer resp.Body.Close()

	result.statusCode = resp.StatusCode
	result.accepted = parseIngestCountHeader(resp.Header.Get("X-Statok-Accepted"))
	result.dropped = parseIngestCountHeader(resp.Header.Get("X-Statok-Dropped"))
	result.rejected = parseIngestCountHeader(resp.Header.Get("X-Statok-Rejected"))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		io.Copy(io.Discard, resp.Body)
		// The server may accept part of a batch and drop/reject the rest while
		// still returning 2xx. We do not retry here because replaying the whole
		// batch would duplicate the accepted events.
		if t.Logger != nil && (result.dropped > 0 || result.rejected > 0) {
			t.Logger.Printf(
				"statok: ingest accepted partial batch batchId=%s accepted=%d dropped=%d rejected=%d endpoint=%s",
				batchID,
				maxHeaderValue(result.accepted),
				maxHeaderValue(result.dropped),
				maxHeaderValue(result.rejected),
				urlStr,
			)
		}
		return result, nil
	}

	detail, responseCode := readErrorBody(resp.Body)
	result.responseCode = responseCode
	diag := analyzeLinePayloadV4(body, t.dict)
	sendErr := &HTTPTransportError{
		Method:                       http.MethodPost,
		Endpoint:                     urlStr,
		BatchID:                      batchID,
		StatusCode:                   resp.StatusCode,
		Status:                       resp.Status,
		ResponseCode:                 responseCode,
		Detail:                       detail,
		Accepted:                     result.accepted,
		Dropped:                      result.dropped,
		Rejected:                     result.rejected,
		RequestBytes:                 len(body),
		DictionarySession:            diag.dictionarySession,
		DictionaryRevision:           diag.dictionaryRevision,
		DictionarySeries:             diag.dictionarySeries,
		SeriesDefinitions:            diag.seriesDefinitions,
		SeriesDefinitionBytes:        diag.seriesDefinitionBytes,
		EventLines:                   diag.eventLines,
		EventBytes:                   diag.eventBytes,
		EventSeriesMinID:             diag.eventSeriesMinID,
		EventSeriesMaxID:             diag.eventSeriesMaxID,
		EventSeriesSamples:           diag.eventSeriesSamples,
		LargestSeriesMetric:          diag.largestSeriesMetric,
		LargestSeriesDefinitionBytes: diag.largestSeriesDefinitionBytes,
		LargestSeriesLabels:          diag.largestSeriesLabels,
		LargestLabelKey:              diag.largestLabelKey,
		LargestLabelBytes:            diag.largestLabelBytes,
		LargestLabelValuePrefix:      diag.largestLabelValuePrefix,
	}
	if t.shouldStopOnStatus(resp.StatusCode) || t.shouldStopOnResponseCode(responseCode) {
		return result, &StopIngestError{
			Code: resp.StatusCode,
			Err:  sendErr,
		}
	}
	return result, sendErr
}

func parseIngestCountHeader(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return -1
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return value
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

type linePayloadDiagnostics struct {
	dictionarySession            string
	dictionaryRevision           uint64
	dictionarySeries             int
	seriesDefinitions            int
	seriesDefinitionBytes        int
	eventLines                   int
	eventBytes                   int
	eventSeriesMinID             int
	eventSeriesMaxID             int
	eventSeriesSamples           string
	largestSeriesMetric          string
	largestSeriesDefinitionBytes int
	largestSeriesLabels          int
	largestLabelKey              string
	largestLabelBytes            int
	largestLabelValuePrefix      string
}

func analyzeLinePayloadV4(body []byte, state *dictionaryState) linePayloadDiagnostics {
	diag := linePayloadDiagnostics{
		eventSeriesMinID: -1,
		eventSeriesMaxID: -1,
	}
	if state != nil {
		diag.dictionarySeries = len(state.series)
	}
	lines := bytes.Split(body, []byte{'\n'})
	sampleIDs := make(map[int]struct{}, 4)
	sampleSeries := make([]string, 0, 4)
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		lineBytes := len(line) + 1
		if bytes.HasPrefix(line, []byte("H|")) {
			parts := bytes.SplitN(line, []byte{'|'}, 5)
			if len(parts) >= 5 {
				diag.dictionarySession = string(parts[3])
				diag.dictionaryRevision, _ = strconv.ParseUint(string(parts[4]), 10, 64)
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("S|")) {
			diag.seriesDefinitions++
			diag.seriesDefinitionBytes += lineBytes
			if lineBytes > diag.largestSeriesDefinitionBytes {
				diag.largestSeriesDefinitionBytes = lineBytes
				metric, labels := parseSeriesDefinitionLine(line)
				diag.largestSeriesMetric = truncateLogField(metric, 96)
				diag.largestSeriesLabels = len(labels)
				diag.largestLabelKey = ""
				diag.largestLabelBytes = 0
				diag.largestLabelValuePrefix = ""
				for _, lbl := range labels {
					if len(lbl) <= diag.largestLabelBytes {
						continue
					}
					diag.largestLabelBytes = len(lbl)
					diag.largestLabelKey, diag.largestLabelValuePrefix = summarizeLabelForLog(lbl)
				}
			}
			continue
		}
		diag.eventLines++
		diag.eventBytes += lineBytes
		if len(line) < 3 || line[1] != '|' {
			continue
		}
		parts := bytes.SplitN(line, []byte{'|'}, 4)
		if len(parts) < 4 {
			continue
		}
		seriesID, err := strconv.Atoi(string(parts[1]))
		if err != nil {
			continue
		}
		if diag.eventSeriesMinID == -1 || seriesID < diag.eventSeriesMinID {
			diag.eventSeriesMinID = seriesID
		}
		if diag.eventSeriesMaxID == -1 || seriesID > diag.eventSeriesMaxID {
			diag.eventSeriesMaxID = seriesID
		}
		if len(sampleSeries) >= 4 {
			continue
		}
		if _, ok := sampleIDs[seriesID]; ok {
			continue
		}
		sampleIDs[seriesID] = struct{}{}
		if state != nil && seriesID >= 0 && seriesID < len(state.series) {
			sampleSeries = append(sampleSeries, fmt.Sprintf("%d:%s", seriesID, truncateLogField(state.series[seriesID].metric, 48)))
			continue
		}
		sampleSeries = append(sampleSeries, strconv.Itoa(seriesID))
	}
	if len(sampleSeries) > 0 {
		diag.eventSeriesSamples = strings.Join(sampleSeries, ",")
	}
	return diag
}

func summarizeTransportError(err error) string {
	var transportErr *HTTPTransportError
	if !errors.As(err, &transportErr) || transportErr == nil {
		if err == nil {
			return ""
		}
		return err.Error()
	}
	fields := []string{
		"endpoint=" + transportErr.Endpoint,
		"status=" + strconv.Itoa(transportErr.StatusCode),
	}
	if transportErr.BatchID != "" {
		fields = append(fields, "batchId="+transportErr.BatchID)
	}
	if transportErr.DictionarySession != "" {
		fields = append(fields, "dictSession="+transportErr.DictionarySession)
	}
	if transportErr.DictionaryRevision > 0 || transportErr.DictionarySession != "" {
		fields = append(fields, "dictRevision="+strconv.FormatUint(transportErr.DictionaryRevision, 10))
	}
	if transportErr.DictionarySeries > 0 {
		fields = append(fields, "dictSeries="+strconv.Itoa(transportErr.DictionarySeries))
	}
	if transportErr.SeriesDefinitions > 0 {
		fields = append(fields, "seriesDefCount="+strconv.Itoa(transportErr.SeriesDefinitions))
	}
	if transportErr.Accepted >= 0 || transportErr.Dropped >= 0 || transportErr.Rejected >= 0 {
		fields = append(
			fields,
			"accepted="+strconv.Itoa(maxHeaderValue(transportErr.Accepted)),
			"dropped="+strconv.Itoa(maxHeaderValue(transportErr.Dropped)),
			"rejected="+strconv.Itoa(maxHeaderValue(transportErr.Rejected)),
		)
	}
	if transportErr.EventLines > 0 {
		fields = append(fields, "eventLines="+strconv.Itoa(transportErr.EventLines))
	}
	if transportErr.EventSeriesSamples != "" {
		fields = append(fields, "eventSeries="+transportErr.EventSeriesSamples)
	}
	if transportErr.ResponseCode != "" {
		fields = append(fields, "responseCode="+transportErr.ResponseCode)
	}
	return strings.Join(fields, " ")
}

func maxHeaderValue(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func parseSeriesDefinitionLine(line []byte) (metric string, labels []string) {
	parts := bytes.Split(line, []byte{'|'})
	if len(parts) < 3 {
		return "", nil
	}
	metric = string(parts[2])
	if len(parts) > 3 {
		labels = make([]string, 0, len(parts)-3)
		for _, part := range parts[3:] {
			if len(part) == 0 {
				continue
			}
			labels = append(labels, string(part))
		}
	}
	return metric, labels
}

func summarizeLabelForLog(label string) (key string, valuePrefix string) {
	key = "raw"
	value := label
	if idx := strings.IndexByte(label, '='); idx >= 0 {
		key = label[:idx]
		value = label[idx+1:]
	}
	return truncateLogField(key, 48), truncateLogField(value, 96)
}

func truncateLogField(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
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
