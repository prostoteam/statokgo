package statok

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"
)

type scriptedTransport struct {
	errs     []error
	batchIDs []string
	calls    int
}

func (t *scriptedTransport) Send(_ context.Context, payload *Payload) error {
	t.calls++
	t.batchIDs = append(t.batchIDs, payload.BatchID)
	if len(t.errs) == 0 {
		return nil
	}
	err := t.errs[0]
	t.errs = t.errs[1:]
	return err
}

func TestNewClientBindsWorkloadWithoutMutatingSharedHTTPTransport(t *testing.T) {
	var gotWorkloads []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotWorkloads = append(gotWorkloads, r.Header.Get(workloadHeaderName))
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	shared := &HTTPTransport{
		Endpoint: srv.URL,
		APIKey:   "1_secret",
	}

	clientA, err := NewClient("api-a", Config{Transport: shared})
	if err != nil {
		t.Fatalf("NewClient(api-a) error = %v", err)
	}
	defer clientA.Close(context.Background())

	clientB, err := NewClient("api-b", Config{Transport: shared})
	if err != nil {
		t.Fatalf("NewClient(api-b) error = %v", err)
	}
	defer clientB.Close(context.Background())

	payload := &Payload{
		Counters: []CounterEvent{{
			Metric:    "requests",
			Value:     1,
			Labels:    []string{Label("host", "h1")},
			Timestamp: 1730000000,
		}},
	}

	if err := clientA.cfg.Transport.Send(context.Background(), payload); err != nil {
		t.Fatalf("clientA Send() error = %v", err)
	}
	if err := clientB.cfg.Transport.Send(context.Background(), payload); err != nil {
		t.Fatalf("clientB Send() error = %v", err)
	}

	if len(gotWorkloads) != 2 {
		t.Fatalf("request count = %d, want 2", len(gotWorkloads))
	}
	if gotWorkloads[0] != "api-a" || gotWorkloads[1] != "api-b" {
		t.Fatalf("workload headers = %v, want [api-a api-b]", gotWorkloads)
	}
	if shared.Workload != "" {
		t.Fatalf("shared transport workload mutated to %q, want empty", shared.Workload)
	}
}

func TestClientQueuesRetryForTransientFailureWithStableBatchID(t *testing.T) {
	tr := &scriptedTransport{
		errs: []error{
			&HTTPTransportError{Endpoint: "http://statok.test/api/i/batch", StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable"},
			nil,
		},
	}
	client := &Client{
		cfg:          Config{Transport: tr, Logger: noopLogger{}},
		logger:       noopLogger{},
		batchSession: "session",
	}
	payload := &Payload{
		Counters: []CounterEvent{{
			Metric:    "requests",
			Value:     1,
			Labels:    []string{Label("host", "h1")},
			Timestamp: 1730000000,
		}},
	}
	retryQueue := make([]retryBatch, 0, 1)

	client.sendPayload(payload, 1, &retryQueue, false)
	if len(retryQueue) != 1 {
		t.Fatalf("retryQueue len = %d, want 1", len(retryQueue))
	}
	if payload.BatchID == "" {
		t.Fatal("expected payload batch id to be assigned")
	}
	client.nextSendAttempt = time.Now().Add(-time.Millisecond)
	retryQueue[0].nextAttempt = time.Now().Add(-time.Millisecond)
	client.flushRetryQueue(&retryQueue, false)

	if len(retryQueue) != 0 {
		t.Fatalf("retryQueue len after retry = %d, want 0", len(retryQueue))
	}
	if tr.calls != 2 {
		t.Fatalf("transport calls = %d, want 2", tr.calls)
	}
	if len(tr.batchIDs) != 2 || tr.batchIDs[0] != payload.BatchID || tr.batchIDs[1] != payload.BatchID {
		t.Fatalf("batch IDs = %v, want stable id %q", tr.batchIDs, payload.BatchID)
	}
}

func TestClientDoesNotRetryNonRetryableFailure(t *testing.T) {
	tr := &scriptedTransport{
		errs: []error{
			&HTTPTransportError{Endpoint: "http://statok.test/api/i/batch", StatusCode: http.StatusBadRequest, Status: "400 Bad Request"},
		},
	}
	client := &Client{
		cfg:          Config{Transport: tr, Logger: noopLogger{}},
		logger:       noopLogger{},
		batchSession: "session",
	}
	payload := &Payload{
		Values: []ValueEvent{{
			Metric:    "latency_ms",
			Value:     5,
			Labels:    []string{Label("host", "h1")},
			Timestamp: 1730000000,
		}},
	}
	retryQueue := make([]retryBatch, 0, 1)

	client.sendPayload(payload, 1, &retryQueue, false)
	if len(retryQueue) != 0 {
		t.Fatalf("retryQueue len = %d, want 0", len(retryQueue))
	}
	if tr.calls != 1 {
		t.Fatalf("transport calls = %d, want 1", tr.calls)
	}
}

func TestClientRetriesNetworkErrors(t *testing.T) {
	client := &Client{}
	if !client.shouldRetryTransport(context.DeadlineExceeded) {
		t.Fatal("expected context deadline exceeded to be retryable")
	}
	if !client.shouldRetryTransport(&net.DNSError{IsTimeout: true}) {
		t.Fatal("expected timeout network error to be retryable")
	}
}

func TestFlushRetryQueueLeavesFutureRetryScheduled(t *testing.T) {
	tr := &scriptedTransport{}
	client := &Client{
		cfg:    Config{Transport: tr, Logger: noopLogger{}},
		logger: noopLogger{},
	}
	retryQueue := []retryBatch{{
		payload: &Payload{
			BatchID: "batch-1",
			Counters: []CounterEvent{{
				Metric:    "requests",
				Value:     1,
				Timestamp: 1730000000,
			}},
		},
		attempts:    1,
		nextAttempt: time.Now().Add(time.Second),
	}}

	client.flushRetryQueue(&retryQueue, false)

	if tr.calls != 0 {
		t.Fatalf("transport calls = %d, want 0", tr.calls)
	}
	if len(retryQueue) != 1 {
		t.Fatalf("retryQueue len = %d, want 1", len(retryQueue))
	}
}

func TestFlushRetryQueueCanIgnoreBackoffDuringClose(t *testing.T) {
	tr := &scriptedTransport{}
	client := &Client{
		cfg:    Config{Transport: tr, Logger: noopLogger{}},
		logger: noopLogger{},
	}
	retryQueue := []retryBatch{{
		payload: &Payload{
			BatchID: "batch-1",
			Counters: []CounterEvent{{
				Metric:    "requests",
				Value:     1,
				Timestamp: 1730000000,
			}},
		},
		attempts:    1,
		nextAttempt: time.Now().Add(time.Hour),
	}}

	client.flushRetryQueue(&retryQueue, true)

	if tr.calls != 1 {
		t.Fatalf("transport calls = %d, want 1", tr.calls)
	}
	if len(retryQueue) != 0 {
		t.Fatalf("retryQueue len = %d, want 0", len(retryQueue))
	}
}

func TestFlushRetryQueueLimitsSuccessfulRetrySendsPerPass(t *testing.T) {
	tr := &scriptedTransport{}
	client := &Client{
		cfg:    Config{Transport: tr, Logger: noopLogger{}},
		logger: noopLogger{},
	}
	retryQueue := []retryBatch{
		{
			payload: &Payload{
				BatchID: "batch-1",
				Counters: []CounterEvent{{
					Metric:    "requests",
					Value:     1,
					Timestamp: 1730000000,
				}},
			},
			attempts:    1,
			nextAttempt: time.Now().Add(-time.Millisecond),
		},
		{
			payload: &Payload{
				BatchID: "batch-2",
				Counters: []CounterEvent{{
					Metric:    "requests",
					Value:     2,
					Timestamp: 1730000001,
				}},
			},
			attempts:    1,
			nextAttempt: time.Now().Add(-time.Millisecond),
		},
	}

	client.flushRetryQueue(&retryQueue, false)

	if tr.calls != 1 {
		t.Fatalf("transport calls = %d, want 1", tr.calls)
	}
	if len(retryQueue) != 1 {
		t.Fatalf("retryQueue len = %d, want 1", len(retryQueue))
	}
	if retryQueue[0].payload.BatchID != "batch-2" {
		t.Fatalf("remaining batch id = %q, want batch-2", retryQueue[0].payload.BatchID)
	}
}

func TestFlushRetryQueueClosePassDoesNotExhaustRetryBudget(t *testing.T) {
	tr := &scriptedTransport{
		errs: []error{
			&HTTPTransportError{Endpoint: "http://statok.test/api/i/batch", StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable"},
		},
	}
	client := &Client{
		cfg:    Config{Transport: tr, Logger: noopLogger{}},
		logger: noopLogger{},
	}
	retryQueue := []retryBatch{{
		payload: &Payload{
			BatchID: "batch-1",
			Counters: []CounterEvent{{
				Metric:    "requests",
				Value:     1,
				Timestamp: 1730000000,
			}},
		},
		attempts:    1,
		nextAttempt: time.Now().Add(time.Hour),
	}}

	client.flushRetryQueue(&retryQueue, true)

	if tr.calls != 1 {
		t.Fatalf("transport calls = %d, want 1", tr.calls)
	}
	if len(retryQueue) != 1 {
		t.Fatalf("retryQueue len = %d, want failed retry requeued", len(retryQueue))
	}
	if retryQueue[0].attempts != 2 {
		t.Fatalf("retry attempts = %d, want 2", retryQueue[0].attempts)
	}
}

func TestClientRetriesConnectionClosedErrors(t *testing.T) {
	client := &Client{}
	for _, err := range []error{
		io.EOF,
		io.ErrUnexpectedEOF,
		syscall.ECONNRESET,
		syscall.ECONNREFUSED,
		syscall.ETIMEDOUT,
	} {
		if !client.shouldRetryTransport(err) {
			t.Fatalf("expected %v to be retryable", err)
		}
	}
}

func TestClientBackoffGatesFreshBatchesAfterTransientFailure(t *testing.T) {
	tr := &scriptedTransport{
		errs: []error{
			&HTTPTransportError{Endpoint: "http://statok.test/api/i/batch", StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable"},
		},
	}
	client := &Client{
		cfg:          Config{Transport: tr, Logger: noopLogger{}},
		logger:       noopLogger{},
		batchSession: "session",
	}
	retryQueue := make([]retryBatch, 0, 2)
	first := &Payload{Counters: []CounterEvent{{Metric: "requests", Value: 1, Timestamp: 1730000000}}}
	second := &Payload{Counters: []CounterEvent{{Metric: "requests", Value: 2, Timestamp: 1730000001}}}

	client.sendPayload(first, 1, &retryQueue, false)
	client.sendPayload(second, 1, &retryQueue, false)

	if tr.calls != 1 {
		t.Fatalf("transport calls = %d, want only first send to hit network", tr.calls)
	}
	if len(retryQueue) != 2 {
		t.Fatalf("retryQueue len = %d, want 2", len(retryQueue))
	}
	if retryQueue[1].attempts != 0 {
		t.Fatalf("deferred fresh batch attempts = %d, want 0", retryQueue[1].attempts)
	}
	if retryQueue[1].nextAttempt.Before(client.nextSendAttempt) {
		t.Fatalf("deferred retry scheduled before client backoff: retry=%s backoff=%s", retryQueue[1].nextAttempt, client.nextSendAttempt)
	}
}

func TestClientBackoffResetsAfterSuccessfulSend(t *testing.T) {
	tr := &scriptedTransport{}
	client := &Client{
		cfg:                     Config{Transport: tr, Logger: noopLogger{}},
		logger:                  noopLogger{},
		batchSession:            "session",
		transientBackoffAttempt: 3,
		nextSendAttempt:         time.Now().Add(-time.Millisecond),
	}
	retryQueue := make([]retryBatch, 0, 1)
	payload := &Payload{Counters: []CounterEvent{{Metric: "requests", Value: 1, Timestamp: 1730000000}}}

	client.sendPayload(payload, 1, &retryQueue, false)

	if tr.calls != 1 {
		t.Fatalf("transport calls = %d, want 1", tr.calls)
	}
	if client.transientBackoffAttempt != 0 {
		t.Fatalf("transientBackoffAttempt = %d, want 0", client.transientBackoffAttempt)
	}
	if !client.nextSendAttempt.IsZero() {
		t.Fatalf("nextSendAttempt = %s, want zero", client.nextSendAttempt)
	}
}

func TestCloseRetryPassRespectsClientBackoff(t *testing.T) {
	tr := &scriptedTransport{}
	client := &Client{
		cfg:             Config{Transport: tr, Logger: noopLogger{}},
		logger:          noopLogger{},
		nextSendAttempt: time.Now().Add(time.Hour),
	}
	retryQueue := []retryBatch{{
		payload: &Payload{
			BatchID: "batch-1",
			Counters: []CounterEvent{{
				Metric:    "requests",
				Value:     1,
				Timestamp: 1730000000,
			}},
		},
		attempts:    1,
		nextAttempt: time.Now().Add(time.Hour),
	}}

	client.flushRetryQueue(&retryQueue, true)

	if tr.calls != 0 {
		t.Fatalf("transport calls = %d, want 0 while client backoff is active", tr.calls)
	}
	if len(retryQueue) != 1 {
		t.Fatalf("retryQueue len = %d, want 1", len(retryQueue))
	}
}

func TestBuildPayloadAssignsBatchIDOnce(t *testing.T) {
	client := &Client{batchSession: "session"}
	events := []*event{{
		typ:   metricTypeCounter,
		name:  "requests",
		value: 1,
		labels: []string{
			Label("host", "h1"),
		},
		ts: time.Unix(1730000000, 0),
	}}

	payload := client.buildPayload(events)
	if payload == nil {
		t.Fatal("buildPayload() returned nil")
	}
	if payload.BatchID == "" {
		t.Fatal("expected buildPayload to assign batch id")
	}

	got := payload.BatchID
	payload = client.buildPayload(events)
	if payload == nil {
		t.Fatal("second buildPayload() returned nil")
	}
	if payload.BatchID == got {
		t.Fatalf("expected new payload batch id to differ, got %q twice", got)
	}
}

func TestSendPayloadDoesNotRetryAcceptedPartialBatch(t *testing.T) {
	tr := &scriptedTransport{errs: []error{nil}}
	client := &Client{
		cfg:          Config{Transport: tr, Logger: noopLogger{}},
		logger:       noopLogger{},
		batchSession: "session",
	}
	payload := &Payload{
		BatchID: "batch-1",
		Counters: []CounterEvent{{
			Metric:    "requests",
			Value:     1,
			Timestamp: 1730000000,
		}},
	}
	retryQueue := make([]retryBatch, 0, 1)

	client.sendPayload(payload, 1, &retryQueue, false)

	if len(retryQueue) != 0 {
		t.Fatalf("retryQueue len = %d, want 0", len(retryQueue))
	}
	if tr.calls != 1 {
		t.Fatalf("transport calls = %d, want 1", tr.calls)
	}
}

func TestEnqueueRetryExhaustsBudget(t *testing.T) {
	client := &Client{logger: noopLogger{}}
	payload := &Payload{BatchID: "batch-1", Counters: []CounterEvent{{Metric: "requests", Value: 1, Timestamp: 1730000000}}}
	retryQueue := make([]retryBatch, 0, 1)

	ok := client.enqueueRetry(&retryQueue, payload, defaultRetryMaxAttempts, context.DeadlineExceeded)
	if ok {
		t.Fatal("enqueueRetry() unexpectedly accepted exhausted retry budget")
	}
	if len(retryQueue) != 0 {
		t.Fatalf("retryQueue len = %d, want 0", len(retryQueue))
	}
}
