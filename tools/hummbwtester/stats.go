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

package main

import (
	"sync"
	"time"
)

// JitterEstimator computes the RFC 3550 section 6.4.1 interarrival jitter estimate
// incrementally, in O(1) time and memory per sample.
//
//	D(i-1,i)  = (Ri - Ri-1) - (Si - Si-1)
//	J = J + (|D(i-1,i)| - J) / 16
//
// where R is the receiver's relative arrival time and S is the sender's relative send time
// for consecutive samples.
//
// Note that monotonic clocks are expected and required.
type JitterEstimator struct {
	have     bool
	prevSend time.Duration
	prevRecv time.Duration
	Jitter   time.Duration
}

// Sample folds one (send time, receive time) pair into the estimator. sendTime and recvTime
// need not be synchronized with each other (they come from different clocks) since only their
// successive differences are used.
func (j *JitterEstimator) Sample(sendTime, recvTime time.Duration) {
	if !j.have {
		j.prevSend, j.prevRecv, j.have = sendTime, recvTime, true
		return
	}
	d := (recvTime - j.prevRecv) - (sendTime - j.prevSend)
	if d < 0 {
		d = -d
	}
	j.Jitter += (d - j.Jitter) / 16
	j.prevSend, j.prevRecv = sendTime, recvTime
}

// seqWindowBits is the size of the reorder-tolerance bitmap used by SeqLossTracker: an arriving
// sequence number can fill a gap up to this many sequence numbers behind the highest seen one
// before the gap is finalized as lost.
const seqWindowBits = 128

// SeqLossTracker implements RFC 3550-style loss accounting for a single, independent sequence
// number stream (e.g. Payload packets from one client), tolerant of a bounded amount of
// reordering: a gap is only finalized as "lost" once it falls out of the trailing
// seqWindowBits window behind the highest sequence number seen so far.
type SeqLossTracker struct {
	started       bool
	baseSeq       uint64
	highestSeq    uint64
	received      uint64
	lost          uint64
	outOfOrder    uint64
	window        [seqWindowBits]bool // window[seq % seqWindowBits] == true iff seq was received.
	windowHighest uint64              // Highest sequence number currently represented in window.
}

// Received records the arrival of seq. It returns whether it arrived out of order (i.e. it is
// not exactly highestSeq+1), and the number of sequence numbers newly finalized as lost by this
// call (sequence numbers that just fell out of the trailing reorder window without ever being
// marked received).
func (t *SeqLossTracker) Received(seq uint64) (outOfOrder bool, newlyLost uint64) {
	if !t.started {
		t.started = true
		t.baseSeq = seq
		t.highestSeq = seq
		t.windowHighest = seq
		t.received++
		t.markReceived(seq)
		return false, 0
	}

	if seq > t.highestSeq {
		outOfOrder = seq != t.highestSeq+1
		before := t.lost
		t.advanceWindow(seq)
		newlyLost = t.lost - before
		t.highestSeq = seq
	} else {
		outOfOrder = true
	}
	t.received++
	t.markReceived(seq)
	if outOfOrder {
		t.outOfOrder++
	}
	return outOfOrder, newlyLost
}

// markReceived marks seq as received in the sliding bitmap, if it is still within the window.
func (t *SeqLossTracker) markReceived(seq uint64) {
	if t.windowHighest > seq && t.windowHighest-seq >= seqWindowBits {
		return // Too far in the past; already finalized as lost.
	}
	t.window[seq%seqWindowBits] = true
}

// advanceWindow slides the bitmap forward to newHighest. Every sequence number pushed out of
// the trailing seqWindowBits window in the process is finalized: if it was never marked
// received, it is counted as lost. Slots newly entering the window are cleared to "not yet
// received" so a later out-of-order arrival can still mark them.
func (t *SeqLossTracker) advanceWindow(newHighest uint64) {
	for s := t.windowHighest + 1; s <= newHighest; s++ {
		if evicted := int64(s) - seqWindowBits; evicted >= 0 && !t.window[uint64(evicted)%seqWindowBits] {
			t.lost++
		}
		t.window[s%seqWindowBits] = false
	}
	t.windowHighest = newHighest
}

// Stats returns the current expected/received/lost/out-of-order counters. Packets still inside
// the trailing reorder window are not yet finalized as lost, so `lost` is a lower bound until
// the run ends; callers wanting a final tally should account for the last seqWindowBits
// sequence numbers separately.
func (t *SeqLossTracker) Stats() (expected, received, lost, outOfOrder uint64) {
	if !t.started {
		return 0, 0, 0, 0
	}
	expected = t.highestSeq - t.baseSeq + 1
	return expected, t.received, t.lost, t.outOfOrder
}

// RateTracker accumulates bytes and packets over successive reporting intervals and computes
// the achieved rate for the most recently completed interval.
type RateTracker struct {
	lastTime    time.Time
	bytes       uint64
	packets     uint64
	totalBytes  uint64
	totalPacket uint64
}

// NewRateTracker returns a RateTracker starting its first interval at now.
func NewRateTracker(now time.Time) *RateTracker {
	return &RateTracker{lastTime: now}
}

// Add records the arrival/departure of one packet of the given size in the current interval.
func (r *RateTracker) Add(bytes int) {
	r.bytes += uint64(bytes)
	r.packets++
	r.totalBytes += uint64(bytes)
	r.totalPacket++
}

// IntervalResult is a snapshot of one completed reporting interval.
type IntervalResult struct {
	Duration    time.Duration
	Bytes       uint64
	Packets     uint64
	BitsPerSec  float64
	TotalBytes  uint64
	TotalPacket uint64
}

// Snapshot closes out the current interval as of now and starts a new one.
func (r *RateTracker) Snapshot(now time.Time) IntervalResult {
	d := now.Sub(r.lastTime)
	res := IntervalResult{
		Duration:    d,
		Bytes:       r.bytes,
		Packets:     r.packets,
		TotalBytes:  r.totalBytes,
		TotalPacket: r.totalPacket,
	}
	if d > 0 {
		res.BitsPerSec = float64(r.bytes) * 8 / d.Seconds()
	}
	r.bytes, r.packets = 0, 0
	r.lastTime = now
	return res
}

// remoteStatsDelta contains the newly reported remote observations in an accepted PongReply.
// The first accepted snapshot returns its full cumulative values so the exported Prometheus
// counters reflect everything the server observed before that reply.
type remoteStatsDelta struct {
	payloadPacketsReceived uint64
	payloadBytesReceived   uint64
	payloadLost            uint64
	payloadOutOfOrder      uint64
	pongRequestsReceived   uint64
	pongRepliesSent        uint64
	receiveRateBps         float64
	hasReceiveRate         bool
}

// remoteStatsTracker converts cumulative server snapshots into deltas and a receive-rate
// estimate. All elapsed time comes from locally captured time.Time values, preserving Go's
// monotonic clock and requiring no synchronization with the server clock.
type remoteStatsTracker struct {
	mu sync.Mutex

	haveSnapshot bool
	lastSequence uint64
	lastReceived time.Time
	last         PongReply
}

func (t *remoteStatsTracker) record(reply PongReply, receivedAt time.Time) (remoteStatsDelta, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.haveSnapshot && (reply.SequenceNumber <= t.lastSequence ||
		reply.SendTimestampNanos <= t.last.SendTimestampNanos) {
		return remoteStatsDelta{}, false
	}
	if t.haveSnapshot && cumulativeSnapshotRegressed(reply, t.last) {
		return remoteStatsDelta{}, false
	}

	previous := PongReply{}
	if t.haveSnapshot {
		previous = t.last
	}
	delta := remoteStatsDelta{
		payloadPacketsReceived: reply.PayloadPacketsReceived - previous.PayloadPacketsReceived,
		payloadBytesReceived:   reply.PayloadBytesReceived - previous.PayloadBytesReceived,
		payloadLost:            reply.PayloadLost - previous.PayloadLost,
		payloadOutOfOrder:      reply.PayloadOutOfOrder - previous.PayloadOutOfOrder,
		pongRequestsReceived:   reply.PongRequestsReceived - previous.PongRequestsReceived,
		pongRepliesSent:        reply.PongRepliesSent - previous.PongRepliesSent,
	}
	if t.haveSnapshot {
		elapsed := receivedAt.Sub(t.lastReceived)
		if elapsed > 0 {
			delta.receiveRateBps = float64(delta.payloadBytesReceived) * 8 / elapsed.Seconds()
			delta.hasReceiveRate = true
		}
	}

	t.haveSnapshot = true
	t.lastSequence = reply.SequenceNumber
	t.lastReceived = receivedAt
	t.last = reply
	return delta, true
}

func cumulativeSnapshotRegressed(current, previous PongReply) bool {
	return current.PayloadPacketsReceived < previous.PayloadPacketsReceived ||
		current.PayloadBytesReceived < previous.PayloadBytesReceived ||
		current.PayloadLost < previous.PayloadLost ||
		current.PayloadOutOfOrder < previous.PayloadOutOfOrder ||
		current.PongRequestsReceived < previous.PongRequestsReceived ||
		current.PongRepliesSent < previous.PongRepliesSent
}

func (t *remoteStatsTracker) age(now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.haveSnapshot {
		return -1
	}
	return now.Sub(t.lastReceived)
}
