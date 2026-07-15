// Copyright 2026 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tokenbucket

import (
	"fmt"
	"math/bits"
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
	return t.apply(size, now)
}

// ReconfigureAndApply atomically updates the rate and burst size before applying
// a packet to the bucket. This is used when a reservation identifier is reused
// with a different bandwidth while packets may be processed concurrently.
func (t *TokenBucket) ReconfigureAndApply(size int, now time.Time, rate, burstSize int64) bool {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.CIR = rate
	t.CBS = burstSize
	return t.apply(size, now)
}

func (t *TokenBucket) apply(size int, now time.Time) bool {
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

// ConvertBW converts a 10-bit Hummingbird bandwidth code into bytes per second.
//
// The 10 bits are interpreted as:
// - bits [9:5]: exponent e (5 bits)
// - bits [4:0]: mantissa m (5 bits)
//
// The decoded real bandwidth is:
// - m, if e == 0
// - (m + 32) * 2^(e-1), if e > 0
//
// ConvertBW is monotonic over the valid encoded range [0, 1023].
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

const maxEncodedBW = (1 << 10) - 1

var maxRealBw = ConvertBW(maxEncodedBW)

// RealBwToEncoded converts real bandwidth in bytes per second to a 10-bit
// Hummingbird bandwidth code.
//
// Quantization policy is ceil: when an exact representation does not exist,
// the returned code is the smallest encodable value whose decoded bandwidth is
// greater than or equal to bw.
//
// Errors:
// - bw < 0
// - bw exceeds the maximum representable value ConvertBW(1023)
func RealBwToEncoded(bw int64) (uint16, error) {
	switch {
	case bw < 0:
		return 0, fmt.Errorf("bandwidth must be non-negative: %d", bw)
	case bw == 0:
		return 0, nil
	case bw > maxRealBw:
		return 0, fmt.Errorf("bandwidth %d exceeds max representable %d", bw, maxRealBw)
	case bw <= 31:
		// exponent=0 directly represents 0..31 via mantissa.
		return uint16(bw), nil
	default:
	}

	// For exponent>0, decoded bandwidth is:
	// (mantissa + 32) * 2^(exponent - 1), with mantissa in [0, 31].
	//
	// First choose the minimum exponent e such that bw <= 63*2^(e-1).
	// Let x = ceil(bw/63). Then e-1 = ceil(log2(x)).
	x := uint64((bw + 62) / 63)
	exponentMinus1 := bits.Len64(x - 1)
	exponent := uint16(exponentMinus1 + 1)

	// With the chosen exponent, compute the minimum q=(mantissa+32) satisfying:
	// q * 2^(e-1) >= bw  => q = ceil(bw / 2^(e-1)).
	scale := int64(1) << exponentMinus1
	q := (bw + scale - 1) / scale
	mantissa := uint16(q - 32)

	return (exponent << 5) | mantissa, nil
}
