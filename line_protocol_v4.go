package statok

import (
	"bytes"
	"math"
	"strconv"
	"time"
)

type seriesDef struct {
	metric string
	labels []string
}

type dictionaryState struct {
	sessionID string
	revision  uint64
	seriesMap map[string]int
	series    []seriesDef
}

func newDictionaryState() *dictionaryState {
	return &dictionaryState{
		sessionID: strconv.FormatInt(time.Now().UnixNano(), 10),
		revision:  0,
		seriesMap: make(map[string]int),
		series:    make([]seriesDef, 0, 64),
	}
}

// encodeLinePayloadV4 encodes payload using reusable dictionary protocol:
//
//	H|4|s|<session>|<revision>
//	S|id|metric|label1|label2|...  (optional, sent when dictionary changes)
//	c|id|value|timestamp
//	v|id|value|timestamp
//	u|id|uniqueId|timestamp
func encodeLinePayloadV4(p *Payload, state *dictionaryState, forceDefinitions bool) []byte {
	if p == nil || p.empty() || state == nil {
		return nil
	}

	totalEvents := len(p.Counters) + len(p.Values) + len(p.Uniques)
	type encodedEvent struct {
		metricType byte
		seriesID   int
		intValue   int64
		floatValue float64
		uniqueID   string
		timestamp  int64
	}
	events := make([]encodedEvent, 0, totalEvents)
	newSeriesIDs := make([]int, 0, totalEvents)
	seriesChanged := false

	getSeriesID := func(metric string, labels []string) int {
		key := seriesKey(metric, labels)
		if id, ok := state.seriesMap[key]; ok {
			return id
		}
		id := len(state.series)
		state.seriesMap[key] = id
		state.series = append(state.series, seriesDef{
			metric: metric,
			labels: cloneLabels(labels),
		})
		newSeriesIDs = append(newSeriesIDs, id)
		seriesChanged = true
		return id
	}

	for _, c := range p.Counters {
		events = append(events, encodedEvent{
			metricType: 'c',
			seriesID:   getSeriesID(c.Metric, c.Labels),
			intValue:   int64(math.Round(c.Value)),
			timestamp:  c.Timestamp,
		})
	}
	for _, v := range p.Values {
		events = append(events, encodedEvent{
			metricType: 'v',
			seriesID:   getSeriesID(v.Metric, v.Labels),
			floatValue: v.Value,
			timestamp:  v.Timestamp,
		})
	}
	for _, u := range p.Uniques {
		events = append(events, encodedEvent{
			metricType: 'u',
			seriesID:   getSeriesID(u.Metric, u.Labels),
			uniqueID:   u.UniqueID,
			timestamp:  u.Timestamp,
		})
	}

	if seriesChanged {
		state.revision++
	}
	sendDefinitions := forceDefinitions || seriesChanged

	var buf bytes.Buffer
	buf.Grow(totalEvents * 32)
	buf.WriteString("H|4|s|")
	buf.WriteString(state.sessionID)
	buf.WriteByte('|')
	buf.WriteString(strconv.FormatUint(state.revision, 10))
	buf.WriteByte('\n')

	if sendDefinitions {
		if forceDefinitions {
			for id, s := range state.series {
				writeSeriesDefinition(&buf, id, s)
			}
		} else {
			for _, id := range newSeriesIDs {
				writeSeriesDefinition(&buf, id, state.series[id])
			}
		}
	}

	for _, ev := range events {
		buf.WriteByte(ev.metricType)
		buf.WriteByte('|')
		buf.WriteString(strconv.Itoa(ev.seriesID))
		buf.WriteByte('|')
		switch ev.metricType {
		case 'c':
			buf.WriteString(strconv.FormatInt(ev.intValue, 10))
		case 'v':
			buf.WriteString(strconv.FormatFloat(ev.floatValue, 'f', 2, 64))
		case 'u':
			buf.WriteString(ev.uniqueID)
		}
		buf.WriteByte('|')
		buf.WriteString(strconv.FormatInt(ev.timestamp, 10))
		buf.WriteByte('\n')
	}

	return buf.Bytes()
}

func writeSeriesDefinition(buf *bytes.Buffer, id int, s seriesDef) {
	buf.WriteString("S|")
	buf.WriteString(strconv.Itoa(id))
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
