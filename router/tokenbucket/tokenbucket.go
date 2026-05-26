package tokenbucket

import (
	"sync"
	"time"
)

type TokenBucket struct {
	CurrentTokens   int64
	LastTimeApplied time.Time

	// Committed Burst Size (burst). In bytes per second.
	CBS int64

	// Committed Information Rate (rate). In bytes per second.
	CIR int64

	// Lock
	lock sync.Mutex
}

// Initializes a new tockenbucket for the given burstSize and rate
func NewTokenBucket(initialTime time.Time, burstSize int64, rate int64) *TokenBucket {
	return &TokenBucket{
		CurrentTokens:   rate,
		CIR:             rate,
		CBS:             burstSize,
		LastTimeApplied: initialTime,
	}
}

// Sets a new rate for the token bucket
func (t *TokenBucket) SetRate(rate int64) {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.CIR = rate
}

// Sets a new burst size for the token bucket
func (t *TokenBucket) SetBurstSize(burstSize int64) {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.CBS = burstSize
}

// Apply calculates the current available tokens and checks whether there
// are enough tokens available. The success is indicated by a bool.
func (t *TokenBucket) Apply(size int, now time.Time) bool {
	t.lock.Lock()
	defer t.lock.Unlock()
	// Increase available tokens according to time passed since last call
	// Apply() is expected to be called from different threads
	// As a consequence, it is possible for now to be older than LastTimeApplied
	if !now.Before(t.LastTimeApplied) {
		t.CurrentTokens += now.Sub(t.LastTimeApplied).Nanoseconds() * t.CIR / (1e9)
		t.CurrentTokens = min(t.CurrentTokens, t.CBS)
		t.LastTimeApplied = now
	}
	if t.CurrentTokens >= int64(size) {
		t.CurrentTokens -= int64(size)
		return true
	}
	return false
}
