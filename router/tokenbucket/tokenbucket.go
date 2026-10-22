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
	"sync"
	"time"

	"github.com/scionproto/scion/pkg/hummingbird/bwencoding"
)

// bytesPerSecondPerKbps converts the bandwidth of a reservation, which is
// expressed in kbps, into the unit of the token bucket.
const bytesPerSecondPerKbps = 1000 / 8

// ConvertBW converts a Hummingbird bandwidth codepoint into bytes per second,
// which is what the token bucket is configured with.
func ConvertBW(bw uint16) int64 {
	return int64(bwencoding.DecodeEncodedBandwidth(bw)) * bytesPerSecondPerKbps
}

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
	// Increase available tokens according to time passed since last call;
	// Calls to apply() are serialized by the mutex, but callers may capture `now` before
	// acquiring it. Concurrent calls can therefore arrive with `now` older than `LastTimeApplied`.
	if !now.Before(t.LastTimeApplied) {
		t.CurrentTokens = min(t.CurrentTokens, t.CBS)
		// Carefully (avoid int64 overflows) refill the bucket, if any available tokens.
		if t.CIR > 0 && t.CurrentTokens < t.CBS {
			// There are new tokens (CIR>0) and the current tokens do not saturate CBS (curr<CBS).
			elapsed := now.Sub(t.LastTimeApplied)
			available := t.CBS - t.CurrentTokens    // Always >0; max possible refill.
			seconds := int64(elapsed / time.Second) // Always >0
			// Saturate before multiplying whole seconds by the rate.
			if seconds > (available-1)/t.CIR {
				// Long time since last applied: saturate.
				t.CurrentTokens = t.CBS
			} else {
				tokens := seconds * t.CIR
				t.CurrentTokens += tokens
				available -= tokens
				nanoseconds := int64(elapsed % time.Second)
				// Split the rate so neither sub-second product can overflow int64.
				tokens = nanoseconds * (t.CIR / int64(time.Second))
				tokens += nanoseconds * (t.CIR % int64(time.Second)) / int64(time.Second)
				if tokens >= available {
					t.CurrentTokens = t.CBS
				} else {
					t.CurrentTokens += tokens
				}
			}
		}
		t.LastTimeApplied = now
	}
	if t.CurrentTokens >= int64(size) {
		t.CurrentTokens -= int64(size)
		return true
	}
	return false
}
