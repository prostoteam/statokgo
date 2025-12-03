package statok

import "strings"

type batchBuilder struct {
	cfg           *Config
	payload       Payload
	counterAggs   map[string]*CounterEvent
	valueAggs     map[string]*valueAggregate
	valueAutoAggs map[string]*valueAutoAggregate
}

func newBatchBuilder(cfg *Config, capacity int) *batchBuilder {
	b := &batchBuilder{cfg: cfg}
	if capacity > 0 {
		b.payload.Counters = make([]CounterEvent, 0, capacity)
		b.payload.Values = make([]ValueEvent, 0, capacity)
	}
	if cfg.LocalAggCounters {
		b.counterAggs = make(map[string]*CounterEvent, min(capacity, cfg.MaxSeriesPerBatch))
	}
	if cfg.ValueMode == ValueAggregationBatch {
		b.valueAggs = make(map[string]*valueAggregate, min(capacity, cfg.MaxSeriesPerBatch))
	}
	if cfg.ValueMode == ValueAggregationAuto {
		b.valueAutoAggs = make(map[string]*valueAutoAggregate, min(capacity, cfg.MaxSeriesPerBatch))
	}
	return b
}

func (b *batchBuilder) add(e *event) {
	switch e.typ {
	case metricTypeCounter:
		b.addCounter(e)
	case metricTypeValue:
		switch b.cfg.ValueMode {
		case ValueAggregationBatch:
			b.addValueAggregate(e)
		case ValueAggregationAuto:
			b.addValueAuto(e)
		default:
			b.payload.Values = append(b.payload.Values, ValueEvent{
				Metric:    e.name,
				Value:     e.value,
				Labels:    cloneLabels(e.labels),
				Timestamp: e.ts.Unix(),
			})
		}
	}
}

func (b *batchBuilder) addCounter(e *event) {
	if !b.cfg.LocalAggCounters {
		b.payload.Counters = append(b.payload.Counters, CounterEvent{
			Metric:    e.name,
			Value:     e.value,
			Labels:    cloneLabels(e.labels),
			Timestamp: e.ts.Unix(),
		})
		return
	}
	if len(b.counterAggs) >= b.cfg.MaxSeriesPerBatch {
		b.payload.Counters = append(b.payload.Counters, CounterEvent{
			Metric:    e.name,
			Value:     e.value,
			Labels:    cloneLabels(e.labels),
			Timestamp: e.ts.Unix(),
		})
		return
	}
	key := seriesKey(e.name, e.labels)
	if agg, ok := b.counterAggs[key]; ok {
		agg.Value += e.value
		if ts := e.ts.Unix(); ts > agg.Timestamp {
			agg.Timestamp = ts
		}
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

func (b *batchBuilder) addValueAggregate(e *event) {
	key := seriesKey(e.name, e.labels)
	if agg, ok := b.valueAggs[key]; ok {
		agg.add(e)
		return
	}
	if len(b.valueAggs) >= b.cfg.MaxSeriesPerBatch {
		b.payload.Values = append(b.payload.Values, ValueEvent{
			Metric:    e.name,
			Value:     e.value,
			Labels:    cloneLabels(e.labels),
			Timestamp: e.ts.Unix(),
		})
		return
	}
	b.valueAggs[key] = newValueAggregate(e)
}

func (b *batchBuilder) addValueAuto(e *event) {
	key := seriesKey(e.name, e.labels)
	if agg, ok := b.valueAutoAggs[key]; ok {
		agg.add(e, b.cfg.ValueAggAutoThreshold)
		return
	}
	if len(b.valueAutoAggs) >= b.cfg.MaxSeriesPerBatch {
		b.payload.Values = append(b.payload.Values, ValueEvent{
			Metric:    e.name,
			Value:     e.value,
			Labels:    cloneLabels(e.labels),
			Timestamp: e.ts.Unix(),
		})
		return
	}
	b.valueAutoAggs[key] = newValueAutoAggregate(e, b.cfg.ValueAggAutoThreshold)
}

func (b *batchBuilder) build() *Payload {
	if len(b.counterAggs) > 0 {
		for _, agg := range b.counterAggs {
			b.payload.Counters = append(b.payload.Counters, *agg)
		}
	}
	if len(b.valueAggs) > 0 {
		for _, agg := range b.valueAggs {
			b.payload.Values = append(b.payload.Values, agg.event())
		}
	}
	if len(b.valueAutoAggs) > 0 {
		for _, agg := range b.valueAutoAggs {
			b.payload.Values = append(b.payload.Values, agg.events(b.cfg.ValueAggAutoThreshold)...)
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

type valueAggregate struct {
	metric string
	labels []string
	sum    float64
	count  uint64
	ts     int64
}

func newValueAggregate(e *event) *valueAggregate {
	return &valueAggregate{
		metric: e.name,
		labels: cloneLabels(e.labels),
		sum:    e.value,
		count:  1,
		ts:     e.ts.Unix(),
	}
}

func (a *valueAggregate) add(e *event) {
	a.sum += e.value
	a.count++
	if ts := e.ts.Unix(); ts > a.ts {
		a.ts = ts
	}
}

func (a *valueAggregate) event() ValueEvent {
	val := a.sum
	if a.count > 0 {
		val = a.sum / float64(a.count)
	}
	return ValueEvent{
		Metric:    a.metric,
		Value:     val,
		Labels:    cloneLabels(a.labels),
		Timestamp: a.ts,
	}
}

type valueAutoAggregate struct {
	metric string
	labels []string
	sum    float64
	count  uint64
	ts     int64
	raw    []ValueEvent
}

func newValueAutoAggregate(e *event, rawLimit int) *valueAutoAggregate {
	labels := cloneLabels(e.labels)
	agg := &valueAutoAggregate{
		metric: e.name,
		labels: labels,
		sum:    e.value,
		count:  1,
		ts:     e.ts.Unix(),
	}
	if rawLimit > 0 {
		agg.raw = append(agg.raw, ValueEvent{
			Metric:    e.name,
			Value:     e.value,
			Labels:    labels,
			Timestamp: e.ts.Unix(),
		})
	}
	return agg
}

func (a *valueAutoAggregate) add(e *event, rawLimit int) {
	a.sum += e.value
	a.count++
	if ts := e.ts.Unix(); ts > a.ts {
		a.ts = ts
	}
	if rawLimit > 0 && len(a.raw) < rawLimit {
		a.raw = append(a.raw, ValueEvent{
			Metric:    e.name,
			Value:     e.value,
			Labels:    a.labels,
			Timestamp: e.ts.Unix(),
		})
	}
}

func (a *valueAutoAggregate) events(rawLimit int) []ValueEvent {
	if rawLimit > 0 && int(a.count) <= rawLimit {
		return a.raw
	}
	val := a.sum / float64(a.count)
	return []ValueEvent{{
		Metric:    a.metric,
		Value:     val,
		Labels:    cloneLabels(a.labels),
		Timestamp: a.ts,
	}}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
