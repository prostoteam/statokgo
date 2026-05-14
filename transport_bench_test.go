package statok

import (
	"context"
	"io"
	"net/http"
	"testing"
)

type benchmarkRoundTripper struct{}

func (benchmarkRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	return &http.Response{
		StatusCode: http.StatusAccepted,
		Status:     "202 Accepted",
		Body:       http.NoBody,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func BenchmarkHTTPTransportSend(b *testing.B) {
	payload := benchmarkPayload(200, 10)

	newTransport := func() *HTTPTransport {
		return &HTTPTransport{
			Endpoint: "http://statok.test/api/i/batch",
			Client: &http.Client{
				Transport: benchmarkRoundTripper{},
			},
			Logger: noopLogger{},
		}
	}

	b.Run("first_send_with_definitions", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			tr := newTransport()
			if err := tr.Send(context.Background(), payload); err != nil {
				b.Fatalf("Send() error = %v", err)
			}
		}
	})

	b.Run("reused_dictionary", func(b *testing.B) {
		tr := newTransport()
		if err := tr.Send(context.Background(), payload); err != nil {
			b.Fatalf("warmup Send() error = %v", err)
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := tr.Send(context.Background(), payload); err != nil {
				b.Fatalf("Send() error = %v", err)
			}
		}
	})
}
