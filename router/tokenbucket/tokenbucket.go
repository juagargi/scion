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

// ConvertBW converts 10-bit data-plane encoded bandwidth into bytes per second.
// This conversion follows the proposal from the Hummingbird paper.
func ConvertBW(bw uint16) int64 {
	// e=0:   0..31
	// e=1:  32..63
	// e=2:  64,66,68,..126
	// e=3:  128,132,..252
	// e=31: ~ 2^35..2^36

	exponent := bw >> 5
	mantissa := bw & 0x1f

	var bytesPerSecond int64
	if exponent == 0 {
		// For exponent=0, the value is represented directly by mantissa.
		bytesPerSecond = int64(mantissa)
	} else {
		// For exponent>0, restore the implicit +32 and scale by 2^(exponent-1):
		// result = (mantissa + 32) * 2^(exponent - 1)
		bytesPerSecond = int64(mantissa+32) << (exponent - 1)
	}

	return bytesPerSecond
}
