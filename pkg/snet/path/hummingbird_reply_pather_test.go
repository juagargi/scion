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

package path_test

import (
	"fmt"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/private/util"
	"github.com/scionproto/scion/pkg/slayers"
	dppath "github.com/scionproto/scion/pkg/slayers/path"
	"github.com/scionproto/scion/pkg/slayers/path/epic"
	dphumm "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	"github.com/scionproto/scion/pkg/slayers/path/onehop"
	dpscion "github.com/scionproto/scion/pkg/slayers/path/scion"
	"github.com/scionproto/scion/pkg/snet"
	"github.com/scionproto/scion/pkg/snet/path"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHummReplyPather checks that HummReplyPather only caches a reversed
// Hummingbird reservation after SetState sees valid reverse-path state on a
// Hummingbird carrier packet, and otherwise falls back to
// snet.DefaultReplyPather for reply-path construction.
func TestHummReplyPather(t *testing.T) {
	timestamp := util.SecsToTime(123456)
	srcId := snet.SourceIdentifier{
		IA:   addr.MustParseIA("1-ff00:0:111"),
		IP:   mustParseIp(t, "10.0.0.2"),
		Port: 12345,
	}
	carrierPath := mustRawHummingbirdPathForReplyPather(t, timestamp)
	validReverseState := mustSerializedReverseReservationState(t, timestamp)
	validReverseStateNoFlyovers := mustSerializedReverseReservationStateNoFlyovers(t, timestamp)

	// Exercise the fallback reply-pather behavior with the main path families
	// supported by DefaultReplyPather.
	replyInputs := map[string]snet.RawPath{
		"hummingbird": mustRawHummingbirdPathForReplyPather(t, timestamp),
		"scion":       mustRawSCIONPathForReplyPather(t, timestamp),
		"epic":        mustRawEPICPath(t, timestamp),
		"onehop":      mustRawOneHopPath(t, timestamp),
	}

	// Each case controls whether SetState is called and whether that call is
	// expected to install a cached reverse reservation.
	cases := map[string]struct {
		packet                *snet.Packet
		srcId                 snet.SourceIdentifier
		wantSetStateErr       bool
		wantCachedReservation bool
	}{
		"never_set_state": {},
		"set_state_invalid_packet/no_reverse_option": {
			packet:                packetWithoutReverseState(srcId, carrierPath),
			srcId:                 srcId,
			wantCachedReservation: false,
		},
		"set_state_invalid_packet/non_hummingbird_carrier": {
			packet: packetWithReverseState(
				srcId,
				mustRawSCIONPathForReplyPather(t, timestamp),
				validReverseState),
			srcId:                 srcId,
			wantSetStateErr:       true,
			wantCachedReservation: false,
		},
		"set_state_invalid_packet/bad_serialized_state": {
			packet: packetWithReverseState(srcId,
				carrierPath, []byte{0xde, 0xad, 0xbe, 0xef}),
			srcId:                 srcId,
			wantSetStateErr:       true,
			wantCachedReservation: false,
		},
		"set_state_valid_packet": {
			packet:                packetWithReverseState(srcId, carrierPath, validReverseState),
			srcId:                 srcId,
			wantCachedReservation: true,
		},
		"set_state_valid_packet/no_flyovers": {
			// A reverse reservation with no flyovers offers nothing beyond reversing the
			// transport path directly, so it must not be cached.
			packet:                packetWithReverseState(srcId, carrierPath, validReverseStateNoFlyovers),
			srcId:                 srcId,
			wantCachedReservation: false,
		},
	}

	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			rp := path.NewHummReplyPather(path.WithClock(func() time.Time { return timestamp }))
			if tc.packet != nil {
				err := rp.SetState(tc.srcId, *tc.packet)
				if tc.wantSetStateErr {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			}

			for replyName, input := range replyInputs {
				replyName, input := replyName, input
				t.Run(replyName, func(t *testing.T) {
					got, err := rp.ReplyPathTo(tc.srcId, cloneRawPath(input))
					if tc.wantCachedReservation {
						// Once a valid reverse reservation is cached, reply-path
						// selection should no longer depend on the incoming path type.
						require.NoError(t, err)
						gotReservation, ok := got.(*path.Reservation)
						require.True(t, ok, "expected cached reservation, got %T", got)

						wantReservation := mustReservationFromReverseState(
							t,
							carrierPath,
							validReverseState,
							tc.srcId.IA)
						require.Equal(t, wantReservation.DstIA, gotReservation.DstIA)
						require.Len(t, gotReservation.Hops, len(wantReservation.Hops))
						require.Equal(t, wantReservation.Dec.Type(), gotReservation.Dec.Type())
						require.Equal(t,
							mustSerializeReservation(t, wantReservation),
							mustSerializeReservation(t, gotReservation),
						)
						return
					}

					// Without cached reservation state, HummReplyPather should match
					// DefaultReplyPather exactly.
					want, wantErr := snet.DefaultReplyPather{}.ReplyPath(cloneRawPath(input))
					require.Equal(t, wantErr != nil, err != nil)
					if wantErr != nil {
						require.EqualError(t, err, wantErr.Error())
						return
					}
					require.NoError(t, err)
					require.IsType(t, want, got)
					require.Equal(t, reflect.TypeOf(want), reflect.TypeOf(got))

					wantRaw, ok := want.(snet.RawReplyPath)
					require.True(t, ok, "expected RawReplyPath, got %T", want)
					gotRaw, ok := got.(snet.RawReplyPath)
					require.True(t, ok, "expected RawReplyPath, got %T", got)
					require.Equal(t, wantRaw.Path.Type(), gotRaw.Path.Type())
					require.Equal(t, mustSerializeSlayersPath(t, wantRaw.Path), mustSerializeSlayersPath(t, gotRaw.Path))
				})
			}
		})
	}
}

// TestHummReplyPatherMultipleClients checks that HummReplyPather keeps one
// cached reservation per distinct source (identified by the IA, IP, port
// triplet), and that clients are never cross-contaminated even when they
// partially share IA or port with another client.
func TestHummReplyPatherMultipleClients(t *testing.T) {
	timestampA := util.SecsToTime(123456)
	timestampB := util.SecsToTime(654321)

	clientA := snet.SourceIdentifier{
		IA:   addr.MustParseIA("1-ff00:0:111"),
		IP:   mustParseIp(t, "10.0.0.2"),
		Port: 12345,
	}
	// clientB differs from clientA in every element of the triplet.
	clientB := snet.SourceIdentifier{
		IA:   addr.MustParseIA("1-ff00:0:112"),
		IP:   mustParseIp(t, "10.0.0.3"),
		Port: 54321,
	}
	// clientC shares clientA's IA and port but not its IP: the triplet as a whole must still
	// be treated as a distinct source.
	clientC := snet.SourceIdentifier{
		IA:   clientA.IA,
		IP:   mustParseIp(t, "10.0.0.9"),
		Port: clientA.Port,
	}

	carrierPathA := mustRawHummingbirdPathForReplyPather(t, timestampA)
	carrierPathB := mustRawHummingbirdPathForReplyPather(t, timestampB)
	stateA := mustSerializedReverseReservationState(t, timestampA)
	stateB := mustSerializedReverseReservationState(t, timestampB)

	rp := path.NewHummReplyPather(path.WithClock(func() time.Time { return timestampA }))

	// Only A and B ever call SetState; C never does.
	require.NoError(t, rp.SetState(clientA, *packetWithReverseState(clientA, carrierPathA, stateA)))
	require.NoError(t, rp.SetState(clientB, *packetWithReverseState(clientB, carrierPathB, stateB)))

	gotA, err := rp.ReplyPathTo(clientA, cloneRawPath(carrierPathA))
	require.NoError(t, err)
	rsvA, ok := gotA.(*path.Reservation)
	require.True(t, ok, "expected cached reservation for clientA, got %T", gotA)

	gotB, err := rp.ReplyPathTo(clientB, cloneRawPath(carrierPathB))
	require.NoError(t, err)
	rsvB, ok := gotB.(*path.Reservation)
	require.True(t, ok, "expected cached reservation for clientB, got %T", gotB)

	// Each client's cached reservation must reflect its own reverse state.
	wantA := mustReservationFromReverseState(t, carrierPathA, stateA, clientA.IA)
	wantB := mustReservationFromReverseState(t, carrierPathB, stateB, clientB.IA)
	require.Equal(t, mustSerializeReservation(t, wantA), mustSerializeReservation(t, rsvA))
	require.Equal(t, mustSerializeReservation(t, wantB), mustSerializeReservation(t, rsvB))
	require.NotEqual(t,
		mustSerializeReservation(t, rsvA),
		mustSerializeReservation(t, rsvB),
		"distinct clients must not share a cached reservation",
	)

	// clientC never called SetState, so it must fall back to the default reply pather rather
	// than picking up clientA's cached reservation, even though it shares A's IA and port.
	gotC, err := rp.ReplyPathTo(clientC, cloneRawPath(carrierPathA))
	require.NoError(t, err)
	_, isReservation := gotC.(*path.Reservation)
	require.False(t, isReservation, "clientC must not receive clientA's cached reservation")
}

// TestHummReplyPatherConcurrent exercises SetState and ReplyPathTo from many goroutines at once,
// each acting as a distinct client. Run with -race to confirm there is no data race on the
// shared reservation cache.
func TestHummReplyPatherConcurrent(t *testing.T) {
	const numClients = 50
	const itersPerClient = 20

	timestamp := util.SecsToTime(123456)
	rp := path.NewHummReplyPather(path.WithClock(func() time.Time { return timestamp }))

	type client struct {
		srcId       snet.SourceIdentifier
		carrierPath snet.RawPath
		state       []byte
	}
	clients := make([]client, numClients)
	for i := range clients {
		clients[i] = client{
			srcId: snet.SourceIdentifier{
				IA:   addr.MustParseIA("1-ff00:0:111"),
				IP:   mustParseIp(t, "10.0.0.2"),
				Port: uint16(20000 + i),
			},
			carrierPath: mustRawHummingbirdPathForReplyPather(t, timestamp),
			state:       mustSerializedReverseReservationState(t, timestamp),
		}
	}

	var wg sync.WaitGroup
	for _, c := range clients {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < itersPerClient; i++ {
				pkt := packetWithReverseState(c.srcId, c.carrierPath, c.state)
				if !assert.NoError(t, rp.SetState(c.srcId, *pkt)) {
					return
				}
				got, err := rp.ReplyPathTo(c.srcId, cloneRawPath(c.carrierPath))
				if !assert.NoError(t, err) {
					return
				}
				assert.IsType(t, (*path.Reservation)(nil), got)
			}
		}()
	}
	wg.Wait()

	// After every goroutine settles, each client must still have its own valid cached
	// reservation, independent of how many other clients were being served concurrently.
	for _, c := range clients {
		got, err := rp.ReplyPathTo(c.srcId, cloneRawPath(c.carrierPath))
		require.NoError(t, err)
		require.IsType(t, (*path.Reservation)(nil), got)
	}
}

// TestHummReplyPatherCleanup checks that cleanup is tied to SetState's insertion of a new
// entry rather than to ReplyPathTo lookups: a reservation past its own expiry+slack keeps being
// served by ReplyPathTo until some later SetState call (for any source) sweeps it away.
func TestHummReplyPatherCleanup(t *testing.T) {
	start := util.SecsToTime(1_000_000)
	now := start
	clock := func() time.Time { return now }

	const slack = 5 * time.Second
	rp := path.NewHummReplyPather(path.WithClock(clock), path.WithCleanupSlack(slack))

	srcId := snet.SourceIdentifier{
		IA:   addr.MustParseIA("1-ff00:0:111"),
		IP:   mustParseIp(t, "10.0.0.2"),
		Port: 12345,
	}
	carrierPath := mustRawHummingbirdPathForReplyPather(t, start)
	state := mustSerializedReverseReservationState(t, start)
	require.NoError(t, rp.SetState(srcId, *packetWithReverseState(srcId, carrierPath, state)))

	// The fixture's flyovers have a 10s duration (see createFlyoverForReplyPather), so the
	// reservation's own expiry is start+10s; with slack it becomes eligible for cleanup at
	// start+10s+slack.
	got, err := rp.ReplyPathTo(srcId, cloneRawPath(carrierPath))
	require.NoError(t, err)
	require.IsType(t, (*path.Reservation)(nil), got, "reservation should still be cached")

	// Advance time past expiry + slack. ReplyPathTo performs no cleanup on its own, so the
	// now-stale entry must still be returned: eviction only happens on the next SetState call.
	now = start.Add(10*time.Second + slack).Add(time.Millisecond)
	got, err = rp.ReplyPathTo(srcId, cloneRawPath(carrierPath))
	require.NoError(t, err)
	require.IsType(t, (*path.Reservation)(nil), got,
		"ReplyPathTo alone must not evict a stale entry; cleanup is tied to SetState")

	// A SetState call for an unrelated source triggers the global cleanup sweep, which
	// evicts srcId's now-stale entry as a side effect.
	otherSrcId := snet.SourceIdentifier{
		IA:   addr.MustParseIA("1-ff00:0:112"),
		IP:   mustParseIp(t, "10.0.0.3"),
		Port: 54321,
	}
	otherCarrierPath := mustRawHummingbirdPathForReplyPather(t, now)
	otherState := mustSerializedReverseReservationState(t, now)
	require.NoError(t, rp.SetState(
		otherSrcId, *packetWithReverseState(otherSrcId, otherCarrierPath, otherState)))

	got, err = rp.ReplyPathTo(srcId, cloneRawPath(carrierPath))
	require.NoError(t, err)
	_, isReservation := got.(*path.Reservation)
	require.False(t, isReservation,
		"srcId's reservation should have been evicted by the SetState-triggered sweep")

	want, wantErr := snet.DefaultReplyPather{}.ReplyPath(cloneRawPath(carrierPath))
	require.NoError(t, wantErr)
	require.Equal(t, want, got)
}

// TestHummReplyPatherSetStateNoFlyoversEvictsCachedReservation checks that a SetState call
// carrying a valid but flyover-less reverse reservation for a source that already has a cached
// reservation immediately evicts that cached entry, rather than leaving it to be served (or to
// be reaped incidentally by some later, unrelated SetState call).
func TestHummReplyPatherSetStateNoFlyoversEvictsCachedReservation(t *testing.T) {
	timestamp := util.SecsToTime(123456)
	rp := path.NewHummReplyPather(path.WithClock(func() time.Time { return timestamp }))

	srcId := snet.SourceIdentifier{
		IA:   addr.MustParseIA("1-ff00:0:111"),
		IP:   mustParseIp(t, "10.0.0.2"),
		Port: 12345,
	}
	carrierPath := mustRawHummingbirdPathForReplyPather(t, timestamp)

	// First, install a cached reservation via a valid reverse state with flyovers.
	state := mustSerializedReverseReservationState(t, timestamp)
	require.NoError(t, rp.SetState(srcId, *packetWithReverseState(srcId, carrierPath, state)))

	got, err := rp.ReplyPathTo(srcId, cloneRawPath(carrierPath))
	require.NoError(t, err)
	require.IsType(t, (*path.Reservation)(nil), got, "reservation should be cached")

	// The same source now sends a valid reverse state, but with no flyovers. SetState must
	// not cache it (as checked elsewhere), and must also evict the stale entry it supersedes.
	stateNoFlyovers := mustSerializedReverseReservationStateNoFlyovers(t, timestamp)
	require.NoError(t, rp.SetState(
		srcId, *packetWithReverseState(srcId, carrierPath, stateNoFlyovers)))

	got, err = rp.ReplyPathTo(srcId, cloneRawPath(carrierPath))
	require.NoError(t, err)
	_, isReservation := got.(*path.Reservation)
	require.False(t, isReservation,
		"the previously cached reservation should have been evicted")

	want, wantErr := snet.DefaultReplyPather{}.ReplyPath(cloneRawPath(carrierPath))
	require.NoError(t, wantErr)
	require.Equal(t, want, got)
}

// BenchmarkHummReplyPatherCleanup measures the cost of evicting n already-expired reservations
// from the cache in a single SetState call, for growing values of n. Cleanup is tied to
// insertion: every SetState call that adds a new entry also sweeps every expired entry at
// once (not just the calling source's), so this is the operation whose cost scales with how
// many distinct clients had entries pending eviction.
//
// The n entries must be recreated before every measured call, since cleanup empties the cache;
// that setup is deliberately left inside the benchmark's own timer (rather than excluded via
// b.StopTimer/b.StartTimer) so Go's duration-based auto-scaling picks a b.N inversely
// proportional to the true per-iteration cost, keeping the total run time bounded regardless of
// n. The cost of the triggering SetState call alone is measured separately and reported as the
// custom "ns/entry" metric.
func BenchmarkHummReplyPatherCleanup(b *testing.B) {
	for _, n := range []int{1, 10, 100, 1_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			start := util.SecsToTime(1_000_000)
			carrierPath := mustRawHummingbirdPathForReplyPather(b, start)
			state := mustSerializedReverseReservationState(b, start)
			ia := addr.MustParseIA("1-ff00:0:111")
			ip := mustParseIp(b, "10.0.0.2")

			srcIds := make([]snet.SourceIdentifier, n)
			for i := range srcIds {
				// Varying only the port is enough to make each entry a distinct map key.
				srcIds[i] = snet.SourceIdentifier{IA: ia, IP: ip, Port: uint16(i)}
			}
			// A source distinct from all of srcIds, used only to trigger the measured
			// SetState call that performs the cleanup sweep.
			triggerSrcId := snet.SourceIdentifier{IA: ia, IP: ip, Port: uint16(n)}

			var cleanupNanos int64
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				now := start
				rp := path.NewHummReplyPather(path.WithClock(func() time.Time { return now }))
				for _, srcId := range srcIds {
					pkt := packetWithReverseState(srcId, carrierPath, state)
					if err := rp.SetState(srcId, *pkt); err != nil {
						b.Fatal(err)
					}
				}
				// Advance time well past every entry's expiry+slack so the next SetState
				// call must evict all n of them in a single sweep.
				now = start.Add(time.Hour)
				triggerPkt := packetWithReverseState(triggerSrcId, carrierPath, state)

				cleanupStart := time.Now()
				if err := rp.SetState(triggerSrcId, *triggerPkt); err != nil {
					b.Fatal(err)
				}
				cleanupNanos += time.Since(cleanupStart).Nanoseconds()
			}
			entries := n
			if entries == 0 {
				entries = 1
			}
			b.ReportMetric(float64(cleanupNanos)/float64(b.N*entries), "ns/entry")
		})
	}
}

// packetWithoutReverseState builds a packet whose SetState call should behave
// like a no-op for reverse-reservation caching.
func packetWithoutReverseState(
	srcId snet.SourceIdentifier,
	carrierPath snet.RawPath,
) *snet.Packet {
	return &snet.Packet{
		PacketInfo: snet.PacketInfo{
			Source: snet.SCIONAddress{
				IA:   srcId.IA,
				Host: addr.HostIP(srcId.IP),
			},
			Path: carrierPath,
			Payload: snet.UDPPayload{
				SrcPort: srcId.Port,
				DstPort: 42,
				Payload: ([]byte)("mock payload"),
			},
		},
	}
}

// cloneRawPath prevents reply-path reversal from mutating shared test input.
func cloneRawPath(rpath snet.RawPath) snet.RawPath {
	return snet.RawPath{
		PathType: rpath.PathType,
		Raw:      append([]byte(nil), rpath.Raw...),
	}
}

// packetWithReverseState builds a packet that carries reverse reservation state
// in an end-to-end option.
func packetWithReverseState(
	srcId snet.SourceIdentifier,
	carrierPath snet.RawPath,
	state []byte,
) *snet.Packet {
	pkt := packetWithoutReverseState(srcId, carrierPath)
	pkt.E2eExtnContents = []*slayers.EndToEndOption{
		{
			OptType: slayers.OptTypeReversePath,
			OptData: append([]byte(nil), state...),
		},
	}
	return pkt
}

// mustRawSCIONPathForReplyPather serializes the synthetic SCION path fixture
// used by the reply-pather tests into an snet.RawPath.
func mustRawSCIONPathForReplyPather(t testing.TB, when time.Time) snet.RawPath {
	t.Helper()

	dec := createScionPathForReplyPather(when)
	raw, err := path.NewSCIONFromDecoded(*dec)
	require.NoError(t, err)
	return snet.RawPath{
		PathType: dpscion.PathType,
		Raw:      append([]byte(nil), raw.Raw...),
	}
}

// mustRawHummingbirdPathForReplyPather serializes the synthetic Hummingbird path
// fixture used by the reply-pather tests into an snet.RawPath.
func mustRawHummingbirdPathForReplyPather(t testing.TB, when time.Time) snet.RawPath {
	t.Helper()

	dec := createHummingbirdPathForReplyPather(when)
	raw := make([]byte, dec.Len())
	require.NoError(t, dec.SerializeTo(raw))
	return snet.RawPath{
		PathType: dphumm.PathType,
		Raw:      raw,
	}
}

// mustRawEPICPath wraps the synthetic SCION fixture in an EPIC path so the
// fallback reply-path logic can be exercised on EPIC inputs.
func mustRawEPICPath(t testing.TB, when time.Time) snet.RawPath {
	t.Helper()

	scionDecoded := createScionPathForReplyPather(when)
	scionRaw, err := scionDecoded.ToRaw()
	require.NoError(t, err)
	epicPath := epic.Path{
		PktID: epic.PktID{
			Timestamp: 1,
			Counter:   0x02000003,
		},
		PHVF:      []byte{1, 2, 3, 4},
		LHVF:      []byte{5, 6, 7, 8},
		ScionPath: scionRaw,
	}
	buff := make([]byte, epicPath.Len())
	require.NoError(t, epicPath.SerializeTo(buff))
	return snet.RawPath{
		PathType: epic.PathType,
		Raw:      buff,
	}
}

// mustRawOneHopPath builds a supported non-SCION, non-Hummingbird fallback
// input for reply-path tests.
func mustRawOneHopPath(t testing.TB, when time.Time) snet.RawPath {
	t.Helper()

	p := onehop.Path{
		Info: dppath.InfoField{
			ConsDir:   true,
			Timestamp: util.TimeToSecs(when),
		},
		FirstHop: dppath.HopField{
			ConsIngress: 0,
			ConsEgress:  41,
			ExpTime:     8,
		},
		SecondHop: dppath.HopField{
			ConsIngress: 1,
			ConsEgress:  0,
			ExpTime:     8,
		},
	}
	raw := make([]byte, p.Len())
	require.NoError(t, p.SerializeTo(raw))
	return snet.RawPath{
		PathType: onehop.PathType,
		Raw:      raw,
	}
}

// mustSerializedReverseReservationState creates the serialized reverse
// reservation state consumed by HummReplyPather.SetState.
func mustSerializedReverseReservationState(t testing.TB, when time.Time) []byte {
	t.Helper()

	srcIA := addr.MustParseIA("1-ff00:0:111")
	reverseSCION := mustReversedSCIONPathForReplyPather(t, when)
	reverseHops := path.FlyoverSequence{
		{
			BaseHop: path.BaseHop{
				IA:      addr.MustParseIA("1-ff00:0:112"),
				Ingress: 0,
				Egress:  1,
			},
			Flyover: createFlyoverForReplyPather(uint32(when.Unix())),
		},
		{
			BaseHop: path.BaseHop{
				IA:      addr.MustParseIA("1-ff00:0:110"),
				Ingress: 2,
				Egress:  1,
			},
			Flyover: createFlyoverForReplyPather(uint32(when.Unix())),
		},
		{
			BaseHop: path.BaseHop{
				IA:      addr.MustParseIA("1-ff00:0:111"),
				Ingress: 41,
				Egress:  0,
			},
			Flyover: createFlyoverForReplyPather(uint32(when.Unix())),
		},
	}

	reservation, err := path.NewReservation(
		path.WithDataplanePath(reverseSCION, srcIA, reverseHops),
		path.WithNow(func() time.Time { return when }),
	)
	require.NoError(t, err)
	return mustSerializeReservation(t, reservation)
}

// mustSerializedReverseReservationStateNoFlyovers is identical to
// mustSerializedReverseReservationState except none of its hops carry a flyover, so the
// resulting Reservation's Expiry is the zero Time. It is used to check that HummReplyPather
// rejects caching such a reservation, since it offers nothing beyond reversing the transport
// path directly.
func mustSerializedReverseReservationStateNoFlyovers(t testing.TB, when time.Time) []byte {
	t.Helper()

	srcIA := addr.MustParseIA("1-ff00:0:111")
	reverseSCION := mustReversedSCIONPathForReplyPather(t, when)
	reverseHops := path.FlyoverSequence{
		{
			BaseHop: path.BaseHop{
				IA:      addr.MustParseIA("1-ff00:0:112"),
				Ingress: 0,
				Egress:  1,
			},
		},
		{
			BaseHop: path.BaseHop{
				IA:      addr.MustParseIA("1-ff00:0:110"),
				Ingress: 2,
				Egress:  1,
			},
		},
		{
			BaseHop: path.BaseHop{
				IA:      addr.MustParseIA("1-ff00:0:111"),
				Ingress: 41,
				Egress:  0,
			},
		},
	}

	reservation, err := path.NewReservation(
		path.WithDataplanePath(reverseSCION, srcIA, reverseHops),
		path.WithNow(func() time.Time { return when }),
	)
	require.NoError(t, err)
	return mustSerializeReservation(t, reservation)
}

// mustReservationFromReverseState reconstructs the reservation that SetState is
// expected to cache for successful bidirectional-reply setup.
func mustReservationFromReverseState(
	t testing.TB,
	carrierPath snet.RawPath,
	state []byte,
	dstIA addr.IA,
) *path.Reservation {
	t.Helper()

	reservation, err := path.NewReservation(
		path.WithReverseFromBidirectional(state, carrierPath, dstIA),
	)
	require.NoError(t, err)
	return reservation
}

// mustReversedSCIONPathForReplyPather creates the reversed SCION dataplane path
// used to synthesize reverse reservation state.
func mustReversedSCIONPathForReplyPather(t testing.TB, when time.Time) path.SCION {
	t.Helper()

	dec := createScionPathForReplyPather(when)
	reversed, err := dec.Reverse()
	require.NoError(t, err)
	reversedDecoded, ok := reversed.(*dpscion.Decoded)
	require.True(t, ok, "unexpected reversed type %T", reversed)
	raw, err := path.NewSCIONFromDecoded(*reversedDecoded)
	require.NoError(t, err)
	return raw
}

// mustSerializeReservation encodes a reservation into its wire-format state for
// stable equality assertions.
func mustSerializeReservation(t testing.TB, reservation *path.Reservation) []byte {
	t.Helper()

	raw := make([]byte, reservation.SerializedLen())
	require.NoError(t, reservation.Serialize(raw))
	return raw
}

// mustSerializeSlayersPath encodes a decoded slayers path into bytes so tests
// can compare reply-path results independently of concrete Go values.
func mustSerializeSlayersPath(t testing.TB, p dppath.Path) []byte {
	t.Helper()

	if p == nil {
		return nil
	}
	raw := make([]byte, p.Len())
	require.NoError(t, p.SerializeTo(raw))
	return raw
}

// createHummingbirdPathForReplyPather returns the synthetic two-segment
// Hummingbird path used throughout the reply-pather tests.
func createHummingbirdPathForReplyPather(iniTime time.Time) *dphumm.Decoded {
	const hfValidity = 8

	return &dphumm.Decoded{
		Base: dphumm.Base{
			PathMeta: dphumm.MetaHdr{
				SegLen: [3]uint8{6, 6, 0},
			},
			NumINF:   2,
			NumLines: 12,
		},
		InfoFields: []dppath.InfoField{
			{
				ConsDir:   false,
				Timestamp: util.TimeToSecs(iniTime),
			},
			{
				ConsDir:   true,
				Timestamp: util.TimeToSecs(iniTime),
			},
		},
		FirstHopPerSeg: [2]uint8{2, 4},
		HopFields: []dphumm.FlyoverHopField{
			{
				HopField: dppath.HopField{
					ConsIngress: 41,
					ConsEgress:  0,
					ExpTime:     hfValidity,
				},
			},
			{
				HopField: dppath.HopField{
					ConsIngress: 0,
					ConsEgress:  1,
					ExpTime:     hfValidity,
				},
			},
			{
				HopField: dppath.HopField{
					ConsIngress: 0,
					ConsEgress:  2,
					ExpTime:     hfValidity,
				},
			},
			{
				HopField: dppath.HopField{
					ConsIngress: 1,
					ConsEgress:  0,
					ExpTime:     hfValidity,
				},
			},
		},
	}
}

// createScionPathForReplyPather returns the SCION counterpart of the synthetic
// Hummingbird fixture used in reply-pather tests.
func createScionPathForReplyPather(iniTime time.Time) *dpscion.Decoded {
	const hfValidity = 8

	return &dpscion.Decoded{
		Base: dpscion.Base{
			PathMeta: dpscion.MetaHdr{
				SegLen: [3]uint8{2, 2, 0},
			},
			NumINF:  2,
			NumHops: 4,
		},
		InfoFields: []dppath.InfoField{
			{
				ConsDir:   false,
				Timestamp: util.TimeToSecs(iniTime),
			},
			{
				ConsDir:   true,
				Timestamp: util.TimeToSecs(iniTime),
			},
		},
		HopFields: []dppath.HopField{
			{
				ConsIngress: 41,
				ConsEgress:  0,
				ExpTime:     hfValidity,
			},
			{
				ConsIngress: 0,
				ConsEgress:  1,
				ExpTime:     hfValidity,
			},
			{
				ConsIngress: 0,
				ConsEgress:  2,
				ExpTime:     hfValidity,
			},
			{
				ConsIngress: 1,
				ConsEgress:  0,
				ExpTime:     hfValidity,
			},
		},
	}
}

// createFlyoverForReplyPather builds deterministic flyover data for synthetic
// reverse reservation state.
func createFlyoverForReplyPather(startTime uint32) *path.FlyoverData {
	return &path.FlyoverData{
		ResID:     1,
		StartTime: startTime,
		Duration:  10,
		Bw:        64,
		Ak:        [16]byte{1, 2, 3, 4},
	}
}

func mustParseIp(t testing.TB, ip string) netip.Addr {
	a, err := netip.ParseAddr(ip)
	require.NoError(t, err)
	return a
}
