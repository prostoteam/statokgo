package statok

import "strings"

type batchBuilder struct {
	payload     Payload
	counterAggs map[string]*CounterEvent
	uniqueSeen  map[string]struct{}
}

func newBatchBuilder(capacity int) *batchBuilder {
	b := &batchBuilder{}
	if capacity > 0 {
		b.payload.Counters = make([]CounterEvent, 0, capacity)
		b.payload.Values = make([]ValueEvent, 0, capacity)
		b.payload.Uniques = make([]UniqueEvent, 0, capacity)
	}
	b.counterAggs = make(map[string]*CounterEvent, min(capacity, defaultMaxSeriesPerBatch))
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
	key := seriesKey(e.name, e.labels)
	if agg, ok := b.counterAggs[key]; ok {
		agg.Value += e.value
		if ts := e.ts.Unix(); ts > agg.Timestamp {
			agg.Timestamp = ts
		}
		return
	}
	if len(b.counterAggs) >= defaultMaxSeriesPerBatch {
		b.payload.Counters = append(b.payload.Counters, CounterEvent{
			Metric:    e.name,
			Value:     e.value,
			Labels:    cloneLabels(e.labels),
			Timestamp: e.ts.Unix(),
		})
		return
	}
	agg := &CounterEvent{
		Metric:    e.name,
		Value:     e.value,
		Labels:    cloneLabels(e.labels),
		Timestamp: e.ts.Unix(),
	}
	b.counterAggs[key] = agg
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
	if len(b.counterAggs) > 0 {
		for _, agg := range b.counterAggs {
			b.payload.Counters = append(b.payload.Counters, *agg)
		}
	}
	if b.payload.empty() {
		return nil
	}
	return &b.payload
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
