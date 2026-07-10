package statok

import (
	"sync/atomic"
	"time"
)

const (
	maxValueEventsPerSecond = 20_000
	rateLimitCounterBits    = 16
	rateLimitCounterMask    = (1 << rateLimitCounterBits) - 1
)

// valueRateLimiter bounds raw value samples before they consume client resources.
type valueRateLimiter struct {
	state atomic.Uint64
}

func (l *valueRateLimiter) allow() bool {
	return l.allowAt(time.Now().Unix())
}

func (l *valueRateLimiter) allowAt(second int64) bool {
	window := uint64(second)
	for {
		state := l.state.Load()
		currentWindow := state >> rateLimitCounterBits
		if currentWindow > window {
			return false
		}
		if currentWindow < window {
			if l.state.CompareAndSwap(state, window<<rateLimitCounterBits|1) {
				return true
			}
			continue
		}

		count := state & rateLimitCounterMask
		if count >= maxValueEventsPerSecond {
			return false
		}
		if l.state.CompareAndSwap(state, state+1) {
			return true
		}
	}
}
