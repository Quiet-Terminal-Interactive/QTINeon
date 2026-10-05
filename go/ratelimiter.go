package qtineon

import (
	"sync"
	"time"
)

const violationThreshold = 10

// rateLimiter is a token-bucket rate limiter for a single source address.
//
// Tokens refill to maxTokens once per second.
// Packets that arrive when the bucket is empty are rejected and counted as violations.
// After more than violationThreshold violations the source is considered throttled.
type rateLimiter struct {
	mu         sync.Mutex
	tokens     int
	maxTokens  int
	lastRefill time.Time
	violations int
}

func newRateLimiter(maxTokensPerSecond int) *rateLimiter {
	return &rateLimiter{
		tokens:     maxTokensPerSecond,
		maxTokens:  maxTokensPerSecond,
		lastRefill: time.Now(),
	}
}

// allowPacket returns true and consumes a token if the packet is permitted.
func (r *rateLimiter) allowPacket() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refill()
	if r.tokens > 0 {
		r.tokens--
		return true
	}
	r.violations++
	return false
}

// isThrottled reports whether the source has exceeded the violation threshold.
func (r *rateLimiter) isThrottled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.violations > violationThreshold
}

func (r *rateLimiter) refill() {
	now := time.Now()
	if now.Sub(r.lastRefill) >= time.Second {
		r.tokens = r.maxTokens
		r.lastRefill = now
		if r.violations > 0 {
			r.violations--
		}
	}
}
