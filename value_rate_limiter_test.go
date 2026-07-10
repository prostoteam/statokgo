package statok

import "testing"

func TestValueRateLimiterLimitsOneSecondWindow(t *testing.T) {
	var limiter valueRateLimiter
	const second = int64(1_700_000_000)

	for i := 0; i < maxValueEventsPerSecond; i++ {
		if !limiter.allowAt(second) {
			t.Fatalf("allowAt() rejected event %d before limit", i+1)
		}
	}
	if limiter.allowAt(second) {
		t.Fatal("allowAt() accepted event above limit")
	}
	if !limiter.allowAt(second + 1) {
		t.Fatal("allowAt() did not reset for next second")
	}
}
