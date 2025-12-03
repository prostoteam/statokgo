package statok

import (
	"strings"
	"sync"
	"time"
)

type metricType uint8

const (
	metricTypeCounter metricType = iota + 1
	metricTypeValue
)

type event struct {
	typ    metricType
	name   string
	value  float64
	labels []string
	ts     time.Time
}

var eventPool = sync.Pool{
	New: func() any {
		return &event{}
	},
}

func borrowEvent(typ metricType, name string, value float64, labels []string) *event {
	e := eventPool.Get().(*event)
	e.typ = typ
	e.name = name
	e.value = value
	e.labels = e.labels[:0]
	for _, l := range labels {
		if trimmed := strings.TrimSpace(l); trimmed != "" {
			e.labels = append(e.labels, trimmed)
		}
	}
	e.ts = time.Now()
	return e
}

func releaseEvent(e *event) {
	if e == nil {
		return
	}
	e.typ = 0
	e.name = ""
	e.value = 0
	e.labels = e.labels[:0]
	eventPool.Put(e)
}
