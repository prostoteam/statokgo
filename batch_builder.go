package statok

import (
	"hash/maphash"
	"strings"
)

type batchBuilder struct {
	payload        Payload
	counterAggs    map[uint64]int
	counterEntries []counterAggEntry
	uniqueSeen     map[string]struct{}
}

type counterAggEntry struct {
	metric       string
	labels       []string
	payloadIndex int
	next         int
}

var seriesHashSeed = maphash.MakeSeed()

func newBatchBuilder(capacity int) *batchBuilder {
	b := &batchBuilder{}
	if capacity > 0 {
		b.payload.Counters = make([]CounterEvent, 0, capacity)
		b.payload.Values = make([]ValueEvent, 0, capacity)
		b.payload.Uniques = make([]UniqueEvent, 0, capacity)
	}
	aggCapacity := min(capacity, defaultMaxSeriesPerBatch)
	b.counterAggs = make(map[uint64]int, aggCapacity)
	b.counterEntries = make([]counterAggEntry, 0, aggCapacity)
	b.uniqueSeen = make(map[string]struct{}, min(capacity, defaultMaxSeriesPerBatch))
	return b
}

func (b *batchBuilder) add(e *event) {
	switch e.typ {
	case metricTypeCounter:
		b.addCounter(e)
	case metricTypeValue:
		b.payload.Values = append(b.payload.Values, ValueEvent{
			Metric:    e.name,
			Value:     e.value,
			Labels:    cloneLabels(e.labels),
			Timestamp: e.ts.Unix(),
		})
	case metricTypeUnique:
		b.addUnique(e)
	}
}

func (b *batchBuilder) addCounter(e *event) {
	hash := seriesHash(e.name, e.labels)
	if head := b.counterAggs[hash]; head != 0 {
		for idx := head - 1; idx >= 0; idx = b.counterEntries[idx].next - 1 {
			entry := b.counterEntries[idx]
			if entry.metric != e.name || !labelsEqual(entry.labels, e.labels) {
				continue
			}
			agg := &b.payload.Counters[entry.payloadIndex]
			agg.Value += e.value
			if ts := e.ts.Unix(); ts > agg.Timestamp {
				agg.Timestamp = ts
			}
			return
		}
	}
	if len(b.counterEntries) >= defaultMaxSeriesPerBatch {
		b.payload.Counters = append(b.payload.Counters, CounterEvent{
			Metric:    e.name,
			Value:     e.value,
			Labels:    cloneLabels(e.labels),
			Timestamp: e.ts.Unix(),
		})
		return
	}
	payloadIndex := len(b.payload.Counters)
	b.payload.Counters = append(b.payload.Counters, CounterEvent{
		Metric:    e.name,
		Value:     e.value,
		Labels:    cloneLabels(e.labels),
		Timestamp: e.ts.Unix(),
	})
	b.counterEntries = append(b.counterEntries, counterAggEntry{
		metric:       e.name,
		labels:       b.payload.Counters[payloadIndex].Labels,
		payloadIndex: payloadIndex,
		next:         b.counterAggs[hash],
	})
	b.counterAggs[hash] = len(b.counterEntries)
}

func (b *batchBuilder) addUnique(e *event) {
	if e.uniqueID == "" {
		return
	}
	key := seriesKey(e.name, e.labels) + "\x01" + e.uniqueID
	if _, ok := b.uniqueSeen[key]; ok {
		return
	}
	b.uniqueSeen[key] = struct{}{}
	b.payload.Uniques = append(b.payload.Uniques, UniqueEvent{
		Metric:    e.name,
		UniqueID:  e.uniqueID,
		Labels:    cloneLabels(e.labels),
		Timestamp: e.ts.Unix(),
	})
}

func (b *batchBuilder) build() *Payload {
	if b.payload.empty() {
		return nil
	}
	return &b.payload
}

func seriesHash(metric string, labels []string) uint64 {
	var h maphash.Hash
	h.SetSeed(seriesHashSeed)
	_, _ = h.WriteString(metric)
	_ = h.WriteByte(0)
	for _, l := range labels {
		_, _ = h.WriteString(l)
		_ = h.WriteByte(0)
	}
	return h.Sum64()
}

func labelsEqual(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func seriesKey(metric string, labels []string) string {
	var sb strings.Builder
	sb.Grow(len(metric) + len(labels)*8 + 8)
	sb.WriteString(metric)
	sb.WriteByte('\x00')
	for _, l := range labels {
		sb.WriteString(l)
		sb.WriteByte('\x00')
	}
	return sb.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
