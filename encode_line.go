package statok

import (
	"bytes"
	"math"
	"strconv"
)

type seriesDef struct {
	metric string
	labels []string
}

// encodeLinePayload encodes the payload using the dictionary-based line protocol:
//
//	H|2|s
//	S|id|metric|label1|label2|...
//	c|id|value|timestamp
//	v|id|value|timestamp
func encodeLinePayload(p *Payload) []byte {
	if p == nil || (len(p.Counters) == 0 && len(p.Values) == 0) {
		return nil
	}

	totalEvents := len(p.Counters) + len(p.Values)
	seriesIndex := make(map[string]int, totalEvents)
	seriesList := make([]seriesDef, 0, totalEvents)
	nextID := 0

	getSeriesID := func(metric string, labels []string) int {
		key := seriesKey(metric, labels)
		if id, ok := seriesIndex[key]; ok {
			return id
		}
		id := nextID
		nextID++
		seriesIndex[key] = id
		seriesList = append(seriesList, seriesDef{
			metric: metric,
			labels: labels,
		})
		return id
	}

	// First pass: populate series dictionary so IDs are stable.
	for _, c := range p.Counters {
		_ = getSeriesID(c.Metric, c.Labels)
	}
	for _, v := range p.Values {
		_ = getSeriesID(v.Metric, v.Labels)
	}

	var buf bytes.Buffer
	buf.Grow(totalEvents * 32) // heuristic

	// Header defines protocol version and timestamp unit.
	buf.WriteString("H|2|s\n")

	// Series dictionary lines.
	for id, s := range seriesList {
		buf.WriteString("S|")
		buf.WriteString(strconv.FormatInt(int64(id), 10))
		buf.WriteByte('|')
		buf.WriteString(s.metric)
		for _, lbl := range s.labels {
			if lbl == "" {
				continue
			}
			buf.WriteByte('|')
			buf.WriteString(lbl)
		}
		buf.WriteByte('\n')
	}

	writeCounter := func(seriesID int, value float64, ts int64) {
		buf.WriteByte('c')
		buf.WriteByte('|')
		buf.WriteString(strconv.FormatInt(int64(seriesID), 10))
		buf.WriteByte('|')
		buf.WriteString(strconv.FormatInt(int64(math.Round(value)), 10))
		buf.WriteByte('|')
		buf.WriteString(strconv.FormatInt(ts, 10))
		buf.WriteByte('\n')
	}

	writeValue := func(seriesID int, value float64, ts int64) {
		buf.WriteByte('v')
		buf.WriteByte('|')
		buf.WriteString(strconv.FormatInt(int64(seriesID), 10))
		buf.WriteByte('|')
		buf.WriteString(strconv.FormatFloat(value, 'f', 2, 64))
		buf.WriteByte('|')
		buf.WriteString(strconv.FormatInt(ts, 10))
		buf.WriteByte('\n')
	}

	for _, c := range p.Counters {
		id := getSeriesID(c.Metric, c.Labels)
		writeCounter(id, c.Value, c.Timestamp)
	}
	for _, v := range p.Values {
		id := getSeriesID(v.Metric, v.Labels)
		writeValue(id, v.Value, v.Timestamp)
	}

	return buf.Bytes()
}
