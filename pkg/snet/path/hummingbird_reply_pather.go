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

package path

import (
	"container/heap"
	"sync"
	"time"

	"github.com/scionproto/scion/pkg/slayers"
	"github.com/scionproto/scion/pkg/snet"
)

// DefaultCleanupSlack is the slack duration added to a cached reservation's expiry time before
// it becomes eligible for cleanup, used unless a different value is given via WithCleanupSlack.
const DefaultCleanupSlack = 10 * time.Second

// HummReplyPather is a singleton, per-Conn, StatefulReplyPather that caches a bidirectional
// Hummingbird Reservation for each distinct remote source (identified by the IA, IP, port
// triplet in snet.SourceIdentifier) that has advertised reverse-reservation state. It is safe
// for concurrent use by multiple goroutines, e.g. serving several clients over the same Conn.
type HummReplyPather struct {
	BackupRepyPather snet.ReplyPather // Used if no bidirectional reservation is available.

	// Now returns the current time. Overridable for testing.
	Now func() time.Time

	// CleanupSlack is added to a reservation's own expiry time before that entry becomes
	// eligible for removal from the cache, to tolerate clock skew and in-flight packets.
	CleanupSlack time.Duration

	mu sync.Mutex
	// reservations maintains a Hummingbird Reservation to each source who has sent a reverse
	// reservation to reach them. It is populated by SetState and consumed by ReplyPathTo.
	// SetState only ever stores entries with a non-zero expiry (see reservationEntry.expiry);
	// a reverse reservation with no flyovers is rejected before it would be cached, since it
	// offers nothing beyond letting ReplyPathTo fall back to reversing the transport path.
	reservations map[snet.SourceIdentifier]reservationEntry
	// expirations is a min-heap of (source, expiry) pairs ordered by expiry time, used to
	// evict entries from reservations once they are no longer valid. It may contain stale
	// entries for a source whose cached reservation has since been replaced; those are
	// recognized and discarded (not evicted) at cleanup time, see cleanupLocked.
	expirations expiryHeap
}

// reservationEntry is the value type stored in HummReplyPather.reservations. expiry is always
// non-zero: SetState never caches a reservation whose own Expiry() is zero.
type reservationEntry struct {
	rsv    *Reservation
	expiry time.Time // expiration + slack.
}

var _ snet.StatefulReplyPather = (*HummReplyPather)(nil)

// NewHummReplyPather returns a HummReplyPather with its backup reply pather set to a regular
// DefaultReplyPather.
func NewHummReplyPather(opts ...HummReplyPatherOption) *HummReplyPather {
	p := &HummReplyPather{
		BackupRepyPather: snet.DefaultReplyPather{},
		Now:              time.Now,
		CleanupSlack:     DefaultCleanupSlack,
		reservations:     make(map[snet.SourceIdentifier]reservationEntry),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// HummReplyPatherOption customizes a HummReplyPather returned by NewHummReplyPather.
type HummReplyPatherOption func(*HummReplyPather)

// WithCleanupSlack overrides the default slack duration added to a cached reservation's expiry
// time before it becomes eligible for cleanup.
func WithCleanupSlack(slack time.Duration) HummReplyPatherOption {
	return func(p *HummReplyPather) {
		p.CleanupSlack = slack
	}
}

// WithClock overrides the function HummReplyPather uses to obtain the current time. It is
// intended for testing the cleanup logic deterministically.
func WithClock(now func() time.Time) HummReplyPatherOption {
	return func(p *HummReplyPather) {
		p.Now = now
	}
}

// SetState stores the necessary information for the Hummingbird reply pather to create a
// reservation. Being this reply pather run at AS A, the state is set when a packet is received
// by A from B, i.e. B->A. This packet contains some end2end extension options with the necessary
// serialized reservation state to reconstruct a valid reverse Reservation.
func (p *HummReplyPather) SetState(sourceId snet.SourceIdentifier, pkt snet.Packet) error {
	// Check if there is any bidirectional reservation information in this packet.
	serializedReservation := containedReversePathState(pkt.E2eExtnContents)
	if serializedReservation == nil {
		// No bidirectional reservation information. Bail.
		return nil
	}

	// Build the reverse reservation.
	originalPath := pkt.Path.(snet.RawPath) // Can't fail, it was checked by the caller.
	rsv, err := NewReservation(
		WithReverseFromBidirectional(serializedReservation, originalPath, pkt.Source.IA))
	if err != nil {
		return err
	}

	expiry := rsv.Expiry()
	if expiry.IsZero() {
		// No flyovers: a reversed Reservation with no flyovers offers nothing beyond
		// reversing the transport path directly, so don't cache it. ReplyPathTo will fall
		// back to the default reply pather, yielding a similar result.
		// If a previously cached reservation exists for this source, remove it: this
		// SetState call reflects the source's current reservation state, which supersedes
		// whatever was cached before, so keeping the old entry around would only serve a
		// now-outdated reservation until it happens to expire on its own.
		p.mu.Lock()
		delete(p.reservations, sourceId)
		p.mu.Unlock()
		return nil
	}

	entry := reservationEntry{
		rsv:    rsv,
		expiry: expiry.Add(p.CleanupSlack),
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.reservations[sourceId] = entry
	heap.Push(&p.expirations, expiryItem{srcId: sourceId, expiry: entry.expiry})
	// Cleanup is tied to insertion rather than lookup: this is the only place the map can
	// grow, so sweeping every globally-expired entry here (irrespective of the current
	// source ID) bounds its size relative to how often reservations are set up, regardless
	// of how often ReplyPathTo is called. A consequence is that a stale entry keeps being
	// returned by ReplyPathTo until some future SetState call reaps it.
	p.cleanupLocked()
	return nil
}

func (p *HummReplyPather) ReplyPathTo(
	sourceId snet.SourceIdentifier,
	rpath snet.RawPath,
) (snet.DataplanePath, error) {
	p.mu.Lock()
	entry, ok := p.reservations[sourceId] // Find a reverse reservation (if any) for this source.
	p.mu.Unlock()

	if ok {
		// If we have a valid reversed reservation for this source,
		// return it already without reversing the current passed path.
		// This reversed reservation might have been constructed many packets ago.
		return entry.rsv, nil
	}

	// Otherwise, just reverse the hummingbird path.
	return p.ReplyPath(rpath)
}

// cleanupLocked removes every reservations entry whose expiry time has passed. p.mu must be
// held by the caller.
func (p *HummReplyPather) cleanupLocked() {
	now := p.Now()
	for p.expirations.Len() > 0 && !p.expirations[0].expiry.After(now) {
		stale := heap.Pop(&p.expirations).(expiryItem)
		// The reservations entry for this source may have been overwritten by a later
		// SetState call after this heap entry was pushed; only delete it if it is still the
		// same entry this heap entry refers to.
		if entry, ok := p.reservations[stale.srcId]; ok && entry.expiry.Equal(stale.expiry) {
			delete(p.reservations, stale.srcId)
		}
	}
}

// ReplyPath uses the embedded backup DefaultReplyPather to return the reply path.
func (p *HummReplyPather) ReplyPath(rpath snet.RawPath) (snet.DataplanePath, error) {
	return p.BackupRepyPather.ReplyPath(rpath)
}

// containedReversePathState extracts the reverse path information reservation option from the
// end to end extension and returns it, or nil if none is present.
func containedReversePathState(opts []*slayers.EndToEndOption) []byte {
	for _, opt := range opts {
		if opt.OptType == slayers.OptTypeReversePath {
			return opt.OptData
		}
	}
	return nil
}

// expiryHeap is a container/heap.Interface min-heap of expiryItem ordered by expiry time.
type expiryHeap []expiryItem

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].expiry.Before(h[j].expiry) }
func (h expiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *expiryHeap) Push(x any) {
	*h = append(*h, x.(expiryItem))
}
func (h *expiryHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// expiryItem is an entry in the expiryHeap.
type expiryItem struct {
	srcId  snet.SourceIdentifier
	expiry time.Time
}
