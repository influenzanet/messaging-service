package main

import (
	"math/rand"
	"time"
)

const (
	// whatsAppThrottleBase is the wait after the first throttle of a series; every further
	// throttle doubles it.
	whatsAppThrottleBase = 60 * time.Second
	// whatsAppThrottleCap is the longest wait the backoff alone reaches.
	whatsAppThrottleCap = time.Hour
	// whatsAppThrottleJitter is the share of the wait that jitter can add on top of it. The
	// stored time is the longest across replicas, so jitter does not spread replicas of one
	// instance; it spreads instances and series, and keeps retries from lining up with other
	// Meta clients.
	whatsAppThrottleJitter = 0.25
)

// whatsAppThrottleDelay is how long the sender stays paused after a throttle. level counts the
// throttles of the series so far, the one being recorded not included; jitter is a number in
// [0, 1). The wait grows with the level up to the cap, jitter only lengthens it and never
// carries it past the cap. retryAfter, the wait Meta asked for, wins when it is longer, and
// floor, the claim lock of the queue, is the least that is ever returned: the messages this
// tick claimed and did not send are held by that lock, so a shorter pause would end before
// they can be fetched again.
func whatsAppThrottleDelay(level int, retryAfter, floor time.Duration, jitter float64) time.Duration {
	delay := whatsAppThrottleBase
	for i := 0; i < level && delay < whatsAppThrottleCap; i++ {
		delay *= 2
	}
	if delay > whatsAppThrottleCap {
		delay = whatsAppThrottleCap
	}
	delay += time.Duration(float64(delay) * whatsAppThrottleJitter * jitter)
	if delay > whatsAppThrottleCap {
		delay = whatsAppThrottleCap
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay < floor {
		delay = floor
	}
	return delay
}

// whatsAppThrottleNotBefore turns the delay into the unix second before which nobody sends,
// rounded up so that the pause is never shorter than the delay.
func whatsAppThrottleNotBefore(now time.Time, delay time.Duration) int64 {
	return now.Add(delay + time.Second - 1).Unix()
}

// randomJitter is the jitter of a real throttle; tests pass fixed values to the functions above.
func randomJitter() float64 { return rand.Float64() }
