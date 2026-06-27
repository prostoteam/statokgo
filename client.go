package statok

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	ErrNoTransport                 = errors.New("statok: no transport configured")
	ErrClientClosed                = errors.New("statok: client closed")
	ErrInvalidWorkload             = errors.New("statok: invalid workload")
	ErrMissingAPIKey               = errors.New("statok: API key is required for HTTP transport")
	ErrInvalidAPIKey               = errors.New("statok: invalid API key")
	ErrAPIKeyAuthorizationConflict = errors.New("statok: API key conflicts with custom Authorization header")
	errClientBackoffActive         = errors.New("statok: transient ingest backoff active")
)

const (
	metaLabelFillMode = "__statok_fill"
	metaFillModeCarry = "carry"
)

// Client implements the non-blocking Statok metrics API.
type Client struct {
	cfg          Config
	queue        chan *event
	cancel       context.CancelFunc
	done         chan struct{}
	batchSeq     atomic.Uint64
	dropped      atomic.Uint64
	stopSending  atomic.Bool
	logger       Logger
	once         sync.Once
	workload     string
	batchSession string

	transientBackoffAttempt int
	nextSendAttempt         time.Time
}

// NewClient builds a client with the provided workload and configuration and starts
// the background flushing goroutine.
func NewClient(workload string, cfg Config) (*Client, error) {
	workload = strings.TrimSpace(workload)
	if err := validateWorkload(workload); err != nil {
		return nil, err
	}
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	if cfg.Transport == nil {
		return nil, ErrNoTransport
	}
	if ht, ok := cfg.Transport.(*HTTPTransport); ok {
		cfg.Transport = &workloadHTTPTransport{
			base:     ht,
			workload: workload,
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		cfg:          cfg,
		queue:        make(chan *event, defaultQueueSize),
		cancel:       cancel,
		done:         make(chan struct{}),
		logger:       cfg.Logger,
		workload:     workload,
		batchSession: newBatchSessionID(),
	}
	if cfg.Verbose && c.logger != nil {
		c.logger.Printf("statok: client version %s", Version())
	}
	go c.run(ctx)
	return c, nil
}

type retryBatch struct {
	payload     *Payload
	attempts    int
	nextAttempt time.Time
}

func newBatchSessionID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

// Default client handling -----------------------------------------------------

var (
	defaultMu     sync.RWMutex
	defaultClient *Client
)

// Init replaces the package-level Client returned by Count/Value helpers.
func Init(workload string, cfg Config) (*Client, error) {
	client, err := NewClient(workload, cfg)
	if err != nil {
		return nil, err
	}
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultClient != nil {
		_ = defaultClient.Close(context.Background())
	}
	defaultClient = client
	return client, nil
}

// Default returns the package-level Client, or nil if Init was never called.
func Default() *Client {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultClient
}

// Count records a counter delta using the default client.
func Count(metric string, delta float64, labels ...string) {
	if c := Default(); c != nil {
		c.Count(metric, delta, labels...)
	}
}

// CountUnique records one unique occurrence using the default client.
func CountUnique(uniqueID any, metric string, labels ...string) {
	if c := Default(); c != nil {
		c.CountUnique(uniqueID, metric, labels...)
	}
}

// Total records a monotonic counter total using the default client.
func Total(metric string, total float64, labels ...string) {
	if c := Default(); c != nil {
		c.Total(metric, total, labels...)
	}
}

// Value records a value sample using the default client.
func Value(metric string, value float64, labels ...string) {
	if c := Default(); c != nil {
		c.Value(metric, value, labels...)
	}
}

// ValueSparse records a value sample that should be treated as sparse gauge data.
func ValueSparse(metric string, value float64, labels ...string) {
	if c := Default(); c != nil {
		c.ValueSparse(metric, value, labels...)
	}
}

// Client methods --------------------------------------------------------------

// Count records a counter delta. Calls never block; on overflow the event is dropped.
func (c *Client) Count(metric string, delta float64, labels ...string) {
	c.enqueue(metricTypeCounter, metric, delta, labels)
}

// CountUnique records one unique occurrence. Calls never block; unsupported
// unique ID values or queue overflow drop the event.
func (c *Client) CountUnique(uniqueID any, metric string, labels ...string) {
	encodedID, ok := canonicalUniqueID(uniqueID)
	if !ok {
		return
	}
	c.enqueueUnique(metric, encodedID, labels)
}

// Total records a monotonic counter total. Calls never block; on overflow the event is dropped.
// The first Total sample for a series is used as a baseline and does not emit a counter event.
func (c *Client) Total(metric string, total float64, labels ...string) {
	c.enqueue(metricTypeTotal, metric, total, labels)
}

// Value records a measurement sample.
func (c *Client) Value(metric string, value float64, labels ...string) {
	c.enqueue(metricTypeValue, metric, value, labels)
}

// ValueSparse records a sparse gauge sample (last value should carry forward).
func (c *Client) ValueSparse(metric string, value float64, labels ...string) {
	if metric == "" {
		return
	}
	metaLabels := make([]string, 0, len(labels)+2)
	metaLabels = append(metaLabels, labels...)
	metaLabels = append(metaLabels, Label(metaLabelFillMode, metaFillModeCarry))
	c.enqueue(metricTypeValue, metric, value, metaLabels)
}

func (c *Client) enqueue(typ metricType, metric string, value float64, labels []string) {
	if metric == "" {
		return
	}
	if c.workload == "" {
		return
	}
	if c.stopSending.Load() {
		return
	}
	if hasWorkloadLabel(labels) {
		if c.logger != nil {
			c.logger.Printf("statok: workload label is reserved; drop metric %q", metric)
		}
		return
	}
	select {
	case <-c.done:
		return
	default:
	}
	ev := borrowEvent(typ, metric, value, labels)
	select {
	case c.queue <- ev:
	default:
		c.dropped.Add(1)
		releaseEvent(ev)
	}
}

func (c *Client) enqueueUnique(metric string, uniqueID string, labels []string) {
	if metric == "" || uniqueID == "" {
		return
	}
	if c.workload == "" {
		return
	}
	if c.stopSending.Load() {
		return
	}
	if hasWorkloadLabel(labels) {
		if c.logger != nil {
			c.logger.Printf("statok: workload label is reserved; drop metric %q", metric)
		}
		return
	}
	select {
	case <-c.done:
		return
	default:
	}
	ev := borrowUniqueEvent(metric, uniqueID, labels)
	select {
	case c.queue <- ev:
	default:
		c.dropped.Add(1)
		releaseEvent(ev)
	}
}

func hasWorkloadLabel(labels []string) bool {
	for _, raw := range labels {
		label := strings.TrimSpace(raw)
		if label == "" {
			continue
		}
		if label == "workload" {
			return true
		}
		if idx := strings.IndexByte(label, '='); idx >= 0 {
			if label[:idx] == "workload" {
				return true
			}
		}
	}
	return false
}

// Dropped returns how many events were rejected because the queue was full.
func (c *Client) Dropped() uint64 {
	return c.dropped.Load()
}

// Close flushes pending events and stops the background worker.
func (c *Client) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var err error
	c.once.Do(func() {
		c.cancel()
		select {
		case <-c.done:
		case <-ctx.Done():
			err = ctx.Err()
			return
		}
	})
	return err
}

func (c *Client) run(ctx context.Context) {
	defer close(c.done)
	ticker := time.NewTicker(defaultFlushInterval)
	defer ticker.Stop()
	batch := make([]*event, 0, defaultMaxBatchSize)
	// Retries stay off the hot queue so transient transport failures cannot
	// feed back into caller-facing backpressure or unbounded memory growth.
	retryQueue := make([]retryBatch, 0, defaultRetryQueueSize)
	totals := make(map[string]float64, min(defaultMaxTotalSeries, defaultMaxBatchSize))
	flush := func() {
		if len(batch) == 0 {
			return
		}
		c.flush(batch, &retryQueue)
		for i := range batch {
			releaseEvent(batch[i])
			batch[i] = nil
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ticker.C:
			flush()
			c.flushRetryQueue(&retryQueue, false)
		case ev := <-c.queue:
			if ev == nil {
				continue
			}
			if ev.typ == metricTypeTotal {
				if !c.applyTotal(ev, totals, defaultMaxTotalSeries) {
					releaseEvent(ev)
					continue
				}
			}
			batch = append(batch, ev)
			if len(batch) >= defaultMaxBatchSize {
				flush()
			}
		case <-ctx.Done():
			// drain queue without blocking
			for {
				select {
				case ev := <-c.queue:
					if ev != nil {
						if ev.typ == metricTypeTotal {
							if !c.applyTotal(ev, totals, defaultMaxTotalSeries) {
								releaseEvent(ev)
								continue
							}
						}
						batch = append(batch, ev)
						if len(batch) >= defaultMaxBatchSize {
							flush()
						}
					}
				default:
					flush()
					c.flushRetryQueue(&retryQueue, true)
					return
				}
			}
		}
	}
}

func (c *Client) applyTotal(ev *event, totals map[string]float64, limit int) bool {
	if ev == nil || limit <= 0 {
		return false
	}
	key := seriesKey(ev.name, ev.labels)
	prev, ok := totals[key]
	if !ok {
		if len(totals) >= limit {
			return false
		}
		totals[key] = ev.value
		return false
	}
	if ev.value < prev {
		totals[key] = ev.value
		return false
	}
	delta := ev.value - prev
	if delta <= 0 {
		totals[key] = ev.value
		return false
	}
	totals[key] = ev.value
	ev.typ = metricTypeCounter
	ev.value = delta
	return true
}

func (c *Client) flush(events []*event, retryQueue *[]retryBatch) {
	if c.stopSending.Load() {
		return
	}
	payload := c.buildPayload(events)
	if payload == nil {
		return
	}
	c.sendPayload(payload, 1, retryQueue, false)
}

func (c *Client) buildPayload(events []*event) *Payload {
	builder := newBatchBuilder(len(events))
	for _, ev := range events {
		builder.add(ev)
	}
	payload := builder.build()
	if payload == nil {
		return nil
	}
	if payload.BatchID == "" {
		// Retries must reuse the original batch identity so a supporting ingester
		// can safely deduplicate ambiguous replays.
		payload.BatchID = c.nextBatchID()
	}
	return payload
}

func (c *Client) nextBatchID() string {
	seq := c.batchSeq.Add(1)
	return c.batchSession + "-" + strconv.FormatUint(seq, 36)
}

func (c *Client) sendPayload(payload *Payload, attempt int, retryQueue *[]retryBatch, fromRetry bool) {
	if payload == nil || payload.empty() || c.stopSending.Load() {
		return
	}
	if payload.BatchID == "" {
		payload.BatchID = c.nextBatchID()
	}
	if c.cfg.Verbose && !fromRetry {
		c.logFlushSummary(payload)
	}
	if c.deferForClientBackoff(payload, attempt, retryQueue) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultFlushTimeout)
	defer cancel()

	if err := c.cfg.Transport.Send(ctx, payload); err != nil {
		if IsStopIngestError(err) {
			if c.stopSending.CompareAndSwap(false, true) && c.logger != nil {
				c.logger.Printf("statok: ingest disabled after non-retryable transport response: %v", err)
			}
			return
		}
		if c.shouldRetryTransport(err) {
			c.noteTransientFailure()
			if c.enqueueRetry(retryQueue, payload, attempt, err) {
				return
			}
		}
		if c.logger != nil {
			c.logger.Printf("statok: flush failed: %v; %s", err, c.flushFailureDetails(payload, err))
		}
		return
	}

	c.resetTransientBackoff()
}

func (c *Client) deferForClientBackoff(payload *Payload, attempt int, retryQueue *[]retryBatch) bool {
	if c == nil || c.nextSendAttempt.IsZero() || !time.Now().Before(c.nextSendAttempt) {
		return false
	}
	lastAttempt := attempt - 1
	if lastAttempt < 0 {
		lastAttempt = 0
	}
	c.enqueueRetryAt(retryQueue, payload, lastAttempt, c.nextSendAttempt, errClientBackoffActive)
	return true
}

func (c *Client) noteTransientFailure() {
	if c == nil {
		return
	}
	c.transientBackoffAttempt++
	c.nextSendAttempt = time.Now().Add(clientBackoffDelay(c.transientBackoffAttempt))
}

func (c *Client) resetTransientBackoff() {
	if c == nil {
		return
	}
	c.transientBackoffAttempt = 0
	c.nextSendAttempt = time.Time{}
}

func (c *Client) enqueueRetry(retryQueue *[]retryBatch, payload *Payload, attempt int, err error) bool {
	return c.enqueueRetryAt(retryQueue, payload, attempt, time.Now().Add(retryDelay(attempt)), err)
}

func (c *Client) enqueueRetryAt(retryQueue *[]retryBatch, payload *Payload, attempts int, nextAttempt time.Time, err error) bool {
	if retryQueue == nil || payload == nil {
		return false
	}
	if attempts >= defaultRetryMaxAttempts {
		if c.logger != nil {
			c.logger.Printf(
				"statok: retry budget exhausted for batchId=%s attempts=%d; %s",
				payload.BatchID,
				attempts,
				c.flushFailureDetails(payload, err),
			)
		}
		return false
	}
	if len(*retryQueue) >= defaultRetryQueueSize {
		if c.logger != nil {
			c.logger.Printf(
				"statok: retry queue full, dropping batchId=%s pending=%d; %s",
				payload.BatchID,
				len(*retryQueue),
				c.flushFailureDetails(payload, err),
			)
		}
		return false
	}
	// Retain the already-built payload rather than rebuilding from pooled events;
	// the payload is immutable from this point onward.
	*retryQueue = append(*retryQueue, retryBatch{
		payload:     payload,
		attempts:    attempts,
		nextAttempt: nextAttempt,
	})
	if c.logger != nil {
		c.logger.Printf(
			"statok: queued retry for batchId=%s nextAttempt=%d/%d delay=%s; %s",
			payload.BatchID,
			attempts+1,
			defaultRetryMaxAttempts,
			time.Until(nextAttempt).Round(time.Millisecond),
			c.flushFailureDetails(payload, err),
		)
	}
	return true
}

func (c *Client) flushRetryQueue(retryQueue *[]retryBatch, ignoreBackoff bool) {
	if retryQueue == nil || len(*retryQueue) == 0 || c.stopSending.Load() {
		return
	}
	now := time.Now()
	processed := 0
	processLimit := len(*retryQueue)
	if defaultRetryFlushMaxSends > 0 && processLimit > defaultRetryFlushMaxSends {
		processLimit = defaultRetryFlushMaxSends
	}
	for i := 0; i < len(*retryQueue) && processed < processLimit; {
		item := (*retryQueue)[i]
		if !ignoreBackoff && item.nextAttempt.After(now) {
			i++
			continue
		}
		// Remove before sending so a failed resend can append a fresh retry entry
		// at the tail without corrupting iteration.
		copy((*retryQueue)[i:], (*retryQueue)[i+1:])
		*retryQueue = (*retryQueue)[:len(*retryQueue)-1]
		c.sendPayload(item.payload, item.attempts+1, retryQueue, true)
		processed++
		now = time.Now()
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := defaultRetryBaseDelay
	for step := 1; step < attempt && delay < defaultRetryMaxDelay; step++ {
		delay *= 2
		if delay >= defaultRetryMaxDelay {
			delay = defaultRetryMaxDelay
			break
		}
	}
	if defaultRetryJitterWindow > 0 {
		delay += time.Duration(time.Now().UnixNano() % int64(defaultRetryJitterWindow))
	}
	return delay
}

func clientBackoffDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := defaultRetryBaseDelay
	for step := 1; step < attempt && delay < defaultClientBackoffMaxDelay; step++ {
		delay *= 2
		if delay >= defaultClientBackoffMaxDelay {
			delay = defaultClientBackoffMaxDelay
			break
		}
	}
	if defaultClientBackoffJitterWindow > 0 {
		delay += time.Duration(time.Now().UnixNano() % int64(defaultClientBackoffJitterWindow))
	}
	return delay
}

func (c *Client) shouldRetryTransport(err error) bool {
	if err == nil || IsStopIngestError(err) {
		return false
	}
	var transportErr *HTTPTransportError
	if errors.As(err, &transportErr) && transportErr != nil {
		switch transportErr.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ETIMEDOUT)
}

func (c *Client) flushFailureDetails(payload *Payload, err error) string {
	stats := summarizePayload(payload)
	fields := []string{
		"batchId=" + payload.BatchID,
		"events=" + strconv.Itoa(stats.totalEvents),
		"counters=" + strconv.Itoa(stats.counters),
		"values=" + strconv.Itoa(stats.values),
		"uniques=" + strconv.Itoa(stats.uniques),
		"counterSeries=" + strconv.Itoa(stats.counterSeries),
		"valueSeries=" + strconv.Itoa(stats.valueSeries),
		"uniqueSeries=" + strconv.Itoa(stats.uniqueSeries),
		"queueDepth=" + strconv.Itoa(len(c.queue)),
		"dropped=" + strconv.FormatUint(c.dropped.Load(), 10),
	}
	fields = append(fields, summarizePayloadValueLimits(payload)...)
	var transportErr *HTTPTransportError
	if errors.As(err, &transportErr) {
		fields = append(fields,
			"endpoint="+transportErr.Endpoint,
			"status="+strconv.Itoa(transportErr.StatusCode),
			"requestBytes="+strconv.Itoa(transportErr.RequestBytes),
		)
		if transportErr.DictionarySession != "" {
			fields = append(fields, "dictSession="+transportErr.DictionarySession)
		}
		if transportErr.DictionaryRevision > 0 || transportErr.DictionarySession != "" {
			fields = append(fields, "dictRevision="+strconv.FormatUint(transportErr.DictionaryRevision, 10))
		}
		if transportErr.DictionarySeries > 0 {
			fields = append(fields, "dictSeries="+strconv.Itoa(transportErr.DictionarySeries))
		}
		if transportErr.EventLines > 0 {
			fields = append(fields, "eventLines="+strconv.Itoa(transportErr.EventLines))
		}
		if transportErr.EventSeriesMinID >= 0 {
			fields = append(fields, "eventSeriesMinID="+strconv.Itoa(transportErr.EventSeriesMinID))
		}
		if transportErr.EventSeriesMaxID >= 0 {
			fields = append(fields, "eventSeriesMaxID="+strconv.Itoa(transportErr.EventSeriesMaxID))
		}
		if transportErr.EventSeriesSamples != "" {
			fields = append(fields, "eventSeries="+transportErr.EventSeriesSamples)
		}
		if transportErr.SeriesDefinitions > 0 {
			fields = append(fields,
				"seriesDefCount="+strconv.Itoa(transportErr.SeriesDefinitions),
				"seriesDefBytes="+strconv.Itoa(transportErr.SeriesDefinitionBytes),
				"eventBytes="+strconv.Itoa(transportErr.EventBytes),
				"largestSeriesDefBytes="+strconv.Itoa(transportErr.LargestSeriesDefinitionBytes),
			)
			if transportErr.LargestSeriesMetric != "" {
				fields = append(fields, "largestSeriesMetric="+transportErr.LargestSeriesMetric)
			}
			if transportErr.LargestSeriesLabels > 0 {
				fields = append(fields, "largestSeriesLabels="+strconv.Itoa(transportErr.LargestSeriesLabels))
			}
			if transportErr.LargestLabelBytes > 0 {
				fields = append(fields, "largestLabelBytes="+strconv.Itoa(transportErr.LargestLabelBytes))
			}
			if transportErr.LargestLabelKey != "" {
				fields = append(fields, "largestLabelKey="+transportErr.LargestLabelKey)
			}
			if transportErr.LargestLabelValuePrefix != "" {
				fields = append(fields, "largestLabelValuePrefix="+transportErr.LargestLabelValuePrefix)
			}
		}
		if transportErr.ResponseCode != "" {
			fields = append(fields, "responseCode="+transportErr.ResponseCode)
		}
		if transportErr.Accepted >= 0 || transportErr.Dropped >= 0 || transportErr.Rejected >= 0 {
			fields = append(
				fields,
				"responseAccepted="+strconv.Itoa(nonNegativeHeaderValue(transportErr.Accepted)),
				"responseDropped="+strconv.Itoa(nonNegativeHeaderValue(transportErr.Dropped)),
				"responseRejected="+strconv.Itoa(nonNegativeHeaderValue(transportErr.Rejected)),
			)
		}
	}
	return strings.Join(fields, " ")
}

func nonNegativeHeaderValue(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

const (
	maxIngestCounterValue = float64(^uint32(0))
	maxIngestValueValue   = float64(^uint32(0) / 10)
)

type numericSeriesDiag struct {
	metric string
	value  float64
	labels string
}

type numericLimitDiag struct {
	negative  int
	nonFinite int
	overMax   int
	maxSeen   numericSeriesDiag
}

func summarizePayloadValueLimits(payload *Payload) []string {
	if payload == nil {
		return nil
	}

	counterDiag := numericLimitDiag{}
	for _, ev := range payload.Counters {
		updateNumericLimitDiag(&counterDiag, ev.Metric, ev.Value, ev.Labels, maxIngestCounterValue)
	}

	valueDiag := numericLimitDiag{}
	for _, ev := range payload.Values {
		updateNumericLimitDiag(&valueDiag, ev.Metric, ev.Value, ev.Labels, maxIngestValueValue)
	}

	var fields []string
	if counterDiag.negative > 0 || counterDiag.nonFinite > 0 || counterDiag.overMax > 0 {
		fields = append(fields,
			"counterNegative="+strconv.Itoa(counterDiag.negative),
			"counterNonFinite="+strconv.Itoa(counterDiag.nonFinite),
			"counterOverMax="+strconv.Itoa(counterDiag.overMax),
		)
		if counterDiag.maxSeen.metric != "" {
			fields = append(fields,
				"maxCounterMetric="+counterDiag.maxSeen.metric,
				"maxCounterValue="+strconv.FormatFloat(counterDiag.maxSeen.value, 'f', 2, 64),
			)
			if counterDiag.maxSeen.labels != "" {
				fields = append(fields, "maxCounterLabels="+counterDiag.maxSeen.labels)
			}
		}
	}
	if valueDiag.negative > 0 || valueDiag.nonFinite > 0 || valueDiag.overMax > 0 {
		fields = append(fields,
			"valueNegative="+strconv.Itoa(valueDiag.negative),
			"valueNonFinite="+strconv.Itoa(valueDiag.nonFinite),
			"valueOverMax="+strconv.Itoa(valueDiag.overMax),
		)
		if valueDiag.maxSeen.metric != "" {
			fields = append(fields,
				"maxValueMetric="+valueDiag.maxSeen.metric,
				"maxValueValue="+strconv.FormatFloat(valueDiag.maxSeen.value, 'f', 2, 64),
			)
			if valueDiag.maxSeen.labels != "" {
				fields = append(fields, "maxValueLabels="+valueDiag.maxSeen.labels)
			}
		}
	}
	return fields
}

func updateNumericLimitDiag(diag *numericLimitDiag, metric string, value float64, labels []string, maxAllowed float64) {
	if diag == nil {
		return
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		diag.nonFinite++
		return
	}
	if value < 0 {
		diag.negative++
	}
	if value > maxAllowed {
		diag.overMax++
	}
	if metric == "" {
		return
	}
	if diag.maxSeen.metric == "" || value > diag.maxSeen.value {
		diag.maxSeen = numericSeriesDiag{
			metric: truncateLogField(metric, 48),
			value:  value,
			labels: summarizeLabelsForLog(labels, 3, 96),
		}
	}
}

func summarizeLabelsForLog(labels []string, maxLabels int, maxBytes int) string {
	if len(labels) == 0 || maxLabels <= 0 || maxBytes <= 0 {
		return ""
	}
	var parts []string
	total := 0
	for _, label := range labels {
		if label == "" {
			continue
		}
		part := truncateLogField(label, 48)
		nextTotal := total + len(part)
		if len(parts) > 0 {
			nextTotal++
		}
		if len(parts) >= maxLabels || nextTotal > maxBytes {
			break
		}
		parts = append(parts, part)
		total = nextTotal
	}
	return strings.Join(parts, ",")
}

func (c *Client) logFlushSummary(payload *Payload) {
	if c.logger == nil || payload == nil {
		return
	}
	cCounter := len(payload.Counters)
	cValues := len(payload.Values)
	cUniques := len(payload.Uniques)
	metricCounters := countMetricsCounters(payload.Counters)
	metricValues := countMetricsValues(payload.Values)
	metricUniques := countMetricsUniques(payload.Uniques)
	const maxMetricsToShow = 10
	c.logger.Printf(
		"statok: flushing %d events (counters=%d, values=%d, uniques=%d); counter metrics: %s; value metrics: %s; unique metrics: %s",
		cCounter+cValues+cUniques,
		cCounter,
		cValues,
		cUniques,
		summarizeMetricCounts(metricCounters, maxMetricsToShow),
		summarizeMetricCounts(metricValues, maxMetricsToShow),
		summarizeMetricCounts(metricUniques, maxMetricsToShow),
	)
}

func countMetricsCounters(events []CounterEvent) map[string]int {
	if len(events) == 0 {
		return nil
	}
	counts := make(map[string]int, min(len(events), defaultMaxSeriesPerBatch))
	for _, ev := range events {
		counts[ev.Metric]++
	}
	return counts
}

func countMetricsValues(events []ValueEvent) map[string]int {
	if len(events) == 0 {
		return nil
	}
	counts := make(map[string]int, min(len(events), defaultMaxSeriesPerBatch))
	for _, ev := range events {
		counts[ev.Metric]++
	}
	return counts
}

func countMetricsUniques(events []UniqueEvent) map[string]int {
	if len(events) == 0 {
		return nil
	}
	counts := make(map[string]int, min(len(events), defaultMaxSeriesPerBatch))
	for _, ev := range events {
		counts[ev.Metric]++
	}
	return counts
}

func summarizeMetricCounts(counts map[string]int, limit int) string {
	if len(counts) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(strconv.Itoa(counts[k]))
	}
	if limit > 0 && len(counts) > limit {
		remaining := len(counts) - limit
		sb.WriteString(", +")
		sb.WriteString(strconv.Itoa(remaining))
		sb.WriteString(" more")
	}
	return sb.String()
}

type payloadSummary struct {
	totalEvents   int
	counters      int
	values        int
	uniques       int
	counterSeries int
	valueSeries   int
	uniqueSeries  int
}

func summarizePayload(payload *Payload) payloadSummary {
	if payload == nil {
		return payloadSummary{}
	}
	return payloadSummary{
		totalEvents:   len(payload.Counters) + len(payload.Values) + len(payload.Uniques),
		counters:      len(payload.Counters),
		values:        len(payload.Values),
		uniques:       len(payload.Uniques),
		counterSeries: len(countMetricsCounters(payload.Counters)),
		valueSeries:   len(countMetricsValues(payload.Values)),
		uniqueSeries:  len(countMetricsUniques(payload.Uniques)),
	}
}
