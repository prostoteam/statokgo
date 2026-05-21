package statok

import (
	"strconv"
	"testing"
)

func benchmarkPayload(seriesCount, eventsPerSeries int) *Payload {
	p := &Payload{
		Counters: make([]CounterEvent, 0, seriesCount*eventsPerSeries),
	}
	ts := int64(1700000000)
	for i := 0; i < seriesCount; i++ {
		metric := "bench.metric." + strconv.Itoa(i)
		labels := []string{"host=h" + strconv.Itoa(i)}
		for j := 0; j < eventsPerSeries; j++ {
			p.Counters = append(p.Counters, CounterEvent{
				Metric:    metric,
				Value:     1,
				Labels:    labels,
				Timestamp: ts + int64(j),
			})
		}
	}
	return p
}

func BenchmarkLineProtocolEncode_ReusedDictionary(b *testing.B) {
	payload := benchmarkPayload(200, 10)

	b.Run("v4_define_every_batch", func(b *testing.B) {
		b.ReportAllocs()
		totalBytes := 0
		state := newDictionaryState()
		for i := 0; i < b.N; i++ {
			data := encodeLinePayloadV4(payload, state, true)
			if len(data) == 0 {
				b.Fatal("empty payload")
			}
			totalBytes += len(data)
		}
		b.ReportMetric(float64(totalBytes)/float64(b.N), "payload_bytes/op")
	})

	b.Run("v4_first_send_with_definitions", func(b *testing.B) {
		b.ReportAllocs()
		totalBytes := 0
		for i := 0; i < b.N; i++ {
			state := newDictionaryState()
			data := encodeLinePayloadV4(payload, state, false)
			if len(data) == 0 {
				b.Fatal("empty payload")
			}
			totalBytes += len(data)
		}
		b.ReportMetric(float64(totalBytes)/float64(b.N), "payload_bytes/op")
	})

	b.Run("v4_reused_dictionary_events_only", func(b *testing.B) {
		state := newDictionaryState()
		if len(encodeLinePayloadV4(payload, state, false)) == 0 {
			b.Fatal("failed to seed dictionary")
		}

		b.ReportAllocs()
		totalBytes := 0
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			data := encodeLinePayloadV4(payload, state, false)
			if len(data) == 0 {
				b.Fatal("empty payload")
			}
			totalBytes += len(data)
		}
		b.ReportMetric(float64(totalBytes)/float64(b.N), "payload_bytes/op")
	})
}
