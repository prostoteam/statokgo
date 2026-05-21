package statok

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
