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
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/scionproto/scion/pkg/addr"
	dppath "github.com/scionproto/scion/pkg/slayers/path"
	dpscion "github.com/scionproto/scion/pkg/slayers/path/scion"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// BenchmarkSerializeWriteTo measures the no-sleep client send path over a real loopback UDP
// socket. Each operation includes snet packet construction, SCION serialization, UDP checksum
// generation, path-specific work (including flyover MACs for Hummingbird), and WriteTo.
func BenchmarkSerializeWriteTo(b *testing.B) {
	paths := benchmarkDataplanePaths(b)
	for _, tc := range paths {
		b.Run(tc.name, func(b *testing.B) {
			benchmarkSerializeWriteTo(b, tc.path)
		})
	}
}

func benchmarkSerializeWriteTo(b *testing.B, dataplanePath snet.DataplanePath) {
	b.Helper()

	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(b, err)
	require.NoError(b, receiver.SetReadBuffer(4<<20))

	var receiverWG sync.WaitGroup
	receiverWG.Add(1)
	go func() {
		defer receiverWG.Done()
		buf := make([]byte, 64<<10)
		for {
			if _, _, err := receiver.ReadFromUDP(buf); err != nil {
				return
			}
		}
	}()

	srcIA := addr.MustParseIA("1-ff00:0:111")
	dstIA := addr.MustParseIA("1-ff00:0:112")
	network := &snet.SCIONNetwork{
		Topology: snet.Topology{
			LocalIA:   srcIA,
			PortRange: snet.TopologyPortRange{Start: 31000, End: 32767},
		},
	}
	remote := &snet.UDPAddr{
		IA:      dstIA,
		Host:    &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 12345},
		Path:    dataplanePath,
		NextHop: receiver.LocalAddr().(*net.UDPAddr),
	}
	conn, err := network.Dial(
		context.Background(), "udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, remote)
	require.NoError(b, err)
	b.Cleanup(func() {
		_ = conn.Close()
		_ = receiver.Close()
		receiverWG.Wait()
	})

	payload := make([]byte, defaultPayloadSize)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := conn.WriteTo(payload, remote); err != nil {
			b.Fatal(err)
		}
	}
	packetsPerSecond := float64(b.N) / b.Elapsed().Seconds()
	b.ReportMetric(packetsPerSecond, "packets/s")
	b.ReportMetric(packetsPerSecond*float64(len(payload)*8)/1e6, "payload-Mbps")
}

func benchmarkDataplanePaths(b *testing.B) []struct {
	name string
	path snet.DataplanePath
} {
	b.Helper()

	decoded := dpscion.Decoded{
		Base: dpscion.Base{
			PathMeta: dpscion.MetaHdr{SegLen: [3]uint8{2, 2, 0}},
			NumINF:   2,
			NumHops:  4,
		},
		InfoFields: []dppath.InfoField{
			{ConsDir: false},
			{ConsDir: true},
		},
		HopFields: []dppath.HopField{
			{ConsIngress: 41, ConsEgress: 0},
			{ConsIngress: 0, ConsEgress: 1},
			{ConsIngress: 0, ConsEgress: 2},
			{ConsIngress: 1, ConsEgress: 0},
		},
	}
	raw := make([]byte, decoded.Len())
	require.NoError(b, decoded.SerializeTo(raw))
	bestEffort := snetpath.SCION{Raw: raw}

	startTime := uint32(time.Now().Add(-time.Minute).Unix())
	flyover := func(ia string, ingress, egress uint16) *snetpath.Hop {
		return &snetpath.Hop{
			BaseHop: snetpath.BaseHop{
				IA:      addr.MustParseIA(ia),
				Ingress: ingress,
				Egress:  egress,
			},
			Flyover: &snetpath.FlyoverData{
				ResID: 1, Ak: [16]byte{1, 2, 3, 4}, Bw: 1000,
				StartTime: startTime, Duration: 600,
			},
		}
	}
	hummingbird, err := snetpath.NewReservation(snetpath.WithDataplanePath(
		bestEffort,
		addr.MustParseIA("1-ff00:0:112"),
		snetpath.FlyoverSequence{
			flyover("1-ff00:0:111", 0, 41),
			flyover("1-ff00:0:110", 1, 2),
			flyover("1-ff00:0:112", 1, 0),
		},
	))
	require.NoError(b, err)

	return []struct {
		name string
		path snet.DataplanePath
	}{
		{name: "best_effort", path: bestEffort},
		{name: "hummingbird", path: hummingbird},
	}
}

func TestRandomHummReservationID(t *testing.T) {
	// Replace the rand function with our own.
	oldRead := cryptoRandRead
	t.Cleanup(func() { cryptoRandRead = oldRead })

	calls := 0
	cryptoRandRead = func(buf []byte) (int, error) {
		calls++
		if calls == 1 {
			copy(buf, []byte{0, 0, 0, 0})
			return len(buf), nil
		}
		copy(buf, []byte{0xff, 0xff, 0xff, 0})
		return len(buf), nil
	}
	id, err := randomHummReservationID()
	require.NoError(t, err)
	assert.Equal(t, uint32(maxHummReservationID), id)
	assert.Equal(t, 2, calls) // First call returned 0, necessary to call again.

	cryptoRandRead = func([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
	_, err = randomHummReservationID()
	require.Error(t, err)
}

func TestRenewalSchedule(t *testing.T) {
	expiry := time.Unix(1_000_000, 0)
	requestAt, handoverAt, startAt := renewalSchedule(
		expiry, defaultRenewalAhead, defaultReservationOverlap, defaultHummStartOffset)

	assert.Equal(t, expiry.Add(-20*time.Second), requestAt)
	assert.Equal(t, expiry.Add(-15*time.Second), handoverAt)
	assert.Equal(t, expiry.Add(-16*time.Second), startAt)

	_, handoverAt, startAt = renewalSchedule(
		expiry, 20*time.Second, 15*time.Second, -3*time.Second)
	assert.Equal(t, expiry.Add(-15*time.Second), handoverAt)
	assert.Equal(t, expiry.Add(-18*time.Second), startAt)

	_, _, startAt = renewalSchedule(expiry, 20*time.Second, 15*time.Second, 3*time.Second)
	assert.Equal(t, expiry.Add(-12*time.Second), startAt)
}

func TestValidateRenewalTiming(t *testing.T) {
	tests := map[string]struct {
		ahead   time.Duration
		overlap time.Duration
		wantErr string
	}{
		"defaults":         {ahead: 20 * time.Second, overlap: 15 * time.Second},
		"equal":            {ahead: 20 * time.Second, overlap: 20 * time.Second},
		"zero":             {},
		"negative ahead":   {ahead: -time.Second, wantErr: "renewal-ahead"},
		"negative overlap": {overlap: -time.Second, wantErr: "reservation-overlap"},
		"handover too soon": {
			ahead: 20 * time.Second, overlap: 21 * time.Second, wantErr: "must not exceed",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateRenewalTiming(tc.ahead, tc.overlap)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestHeaderRoundTrip checks that a packet header can be encoded and decoded
// without losing any fields.
func TestHeaderRoundTrip(t *testing.T) {
	// Build a header-sized buffer and encode representative header values.
	buf := make([]byte, HeaderLen)
	EncodeHeader(buf, Header{
		Type:               PacketTypePongRequest,
		SequenceNumber:     42,
		SendTimestampNanos: 1234567890,
	})

	// Decode the same buffer to verify the wire representation is readable.
	got, err := DecodeHeader(buf)
	require.NoError(t, err)

	// Check each field explicitly so regressions point to the broken encoding.
	assert.Equal(t, PacketTypePongRequest, got.Type)
	assert.Equal(t, uint64(42), got.SequenceNumber)
	assert.Equal(t, int64(1234567890), got.SendTimestampNanos)
}

// TestDecodeHeaderVersionMismatch checks that headers with an unsupported
// protocol version are rejected. This protects peers from silently accepting
// packets that may have an incompatible layout or semantics.
func TestDecodeHeaderVersionMismatch(t *testing.T) {
	// Encode a valid header first so the only invalid byte is the version.
	buf := make([]byte, HeaderLen)
	EncodeHeader(buf, Header{Type: PacketTypePayload})
	buf[0] = Version + 1

	// Decode should fail instead of interpreting a different protocol version.
	_, err := DecodeHeader(buf)
	assert.Error(t, err)
}

// TestPongReplyRoundTrip checks that a pong reply preserves both its embedded
// header and server-side timestamps through encoding and decoding.
func TestPongReplyRoundTrip(t *testing.T) {
	// Encode a complete pong reply with client and server timing fields.
	buf := make([]byte, PongReplyLen)
	EncodePongReply(buf, PongReply{
		Header: Header{
			Type:               PacketTypePongReply,
			SequenceNumber:     7,
			SendTimestampNanos: 100,
		},
		ServerRecvTimestampNanos: 200,
		ServerSendTimestampNanos: 300,
		PayloadPacketsReceived:   11,
		PayloadBytesReceived:     8800,
		PayloadLost:              2,
		PayloadOutOfOrder:        3,
		PongRequestsReceived:     7,
		PongRepliesSent:          6,
	})

	// Decode the reply to exercise the same path a receiving client uses.
	got, err := DecodePongReply(buf)
	require.NoError(t, err)

	// Verify all timing and sequence fields survive the round trip unchanged.
	assert.Equal(t, uint64(7), got.SequenceNumber)
	assert.Equal(t, int64(100), got.SendTimestampNanos)
	assert.Equal(t, int64(200), got.ServerRecvTimestampNanos)
	assert.Equal(t, int64(300), got.ServerSendTimestampNanos)
	assert.Equal(t, uint64(11), got.PayloadPacketsReceived)
	assert.Equal(t, uint64(8800), got.PayloadBytesReceived)
	assert.Equal(t, uint64(2), got.PayloadLost)
	assert.Equal(t, uint64(3), got.PayloadOutOfOrder)
	assert.Equal(t, uint64(7), got.PongRequestsReceived)
	assert.Equal(t, uint64(6), got.PongRepliesSent)
}

func TestRemoteStatsTrackerUsesClientReceiveTime(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	var tracker remoteStatsTracker

	first, accepted := tracker.record(PongReply{
		Header:                 Header{SequenceNumber: 10, SendTimestampNanos: 100},
		PayloadPacketsReceived: 10,
		PayloadBytesReceived:   1000,
		PongRequestsReceived:   1,
		PongRepliesSent:        1,
		// Deliberately unrelated server clock values must not affect the rate.
		ServerRecvTimestampNanos: -9_000_000_000,
		ServerSendTimestampNanos: 99_000_000_000,
	}, start)
	require.True(t, accepted)
	assert.False(t, first.hasReceiveRate)
	assert.Equal(t, uint64(1000), first.payloadBytesReceived)

	second, accepted := tracker.record(PongReply{
		Header:                   Header{SequenceNumber: 11, SendTimestampNanos: 200},
		PayloadPacketsReceived:   30,
		PayloadBytesReceived:     5000,
		PayloadLost:              2,
		PayloadOutOfOrder:        1,
		PongRequestsReceived:     2,
		PongRepliesSent:          2,
		ServerRecvTimestampNanos: 1,
		ServerSendTimestampNanos: 2,
	}, start.Add(2*time.Second))
	require.True(t, accepted)
	require.True(t, second.hasReceiveRate)
	assert.InDelta(t, 16000, second.receiveRateBps, 0.001)
	assert.Equal(t, uint64(4000), second.payloadBytesReceived)
}

func TestRemoteStatsTrackerCatchesUpAndRejectsOldSnapshots(t *testing.T) {
	start := time.Unix(0, 0)
	var tracker remoteStatsTracker
	_, accepted := tracker.record(PongReply{
		Header:                 Header{SequenceNumber: 1, SendTimestampNanos: 100},
		PayloadPacketsReceived: 1,
		PayloadBytesReceived:   100,
		PongRequestsReceived:   1,
		PongRepliesSent:        1,
	}, start)
	require.True(t, accepted)

	catchUp, accepted := tracker.record(PongReply{
		Header:                 Header{SequenceNumber: 3, SendTimestampNanos: 300},
		PayloadPacketsReceived: 5,
		PayloadBytesReceived:   500,
		PongRequestsReceived:   3,
		PongRepliesSent:        3,
	}, start.Add(2*time.Second))
	require.True(t, accepted)
	assert.Equal(t, uint64(400), catchUp.payloadBytesReceived)
	assert.InDelta(t, 1600, catchUp.receiveRateBps, 0.001)

	_, accepted = tracker.record(PongReply{
		Header:                 Header{SequenceNumber: 2, SendTimestampNanos: 200},
		PayloadPacketsReceived: 3,
		PayloadBytesReceived:   300,
		PongRequestsReceived:   2,
		PongRepliesSent:        2,
	}, start.Add(3*time.Second))
	assert.False(t, accepted)
	assert.Equal(t, time.Second, tracker.age(start.Add(3*time.Second)))

	_, accepted = tracker.record(PongReply{
		Header:                 Header{SequenceNumber: 4, SendTimestampNanos: 250},
		PayloadPacketsReceived: 6,
		PayloadBytesReceived:   600,
		PongRequestsReceived:   4,
		PongRepliesSent:        4,
	}, start.Add(4*time.Second))
	assert.False(t, accepted)
	assert.Equal(t, 2*time.Second, tracker.age(start.Add(4*time.Second)))
}

func TestRemoteStatsTrackerIgnoresNonPositiveRateInterval(t *testing.T) {
	now := time.Unix(0, 0)
	var tracker remoteStatsTracker
	_, accepted := tracker.record(PongReply{
		Header:                 Header{SequenceNumber: 1, SendTimestampNanos: 100},
		PayloadPacketsReceived: 1,
		PayloadBytesReceived:   100,
		PongRequestsReceived:   1,
		PongRepliesSent:        1,
	}, now)
	require.True(t, accepted)

	delta, accepted := tracker.record(PongReply{
		Header:                 Header{SequenceNumber: 2, SendTimestampNanos: 200},
		PayloadPacketsReceived: 2,
		PayloadBytesReceived:   200,
		PongRequestsReceived:   2,
		PongRepliesSent:        2,
	}, now)
	require.True(t, accepted)
	assert.False(t, delta.hasReceiveRate)
}

func TestPongTrackerUsesReplyAfterTimeout(t *testing.T) {
	tracker := newPongTracker()
	sentAt := tracker.startedAt.Add(time.Second)
	tracker.recordSent(7, sentAt)

	receivedAt := sentAt.Add(3 * time.Second)
	assert.Equal(t, 1, tracker.evictTimedOut(receivedAt))
	rtt, late := tracker.recordReplied(PongReply{
		Header: Header{SequenceNumber: 7, SendTimestampNanos: sentAt.UnixNano()},
	}, receivedAt)

	assert.True(t, late)
	assert.Equal(t, 3*time.Second, rtt)
	assert.Equal(t, 3*time.Second, tracker.lastRTT)
}

// TestEncodePayloadFillerNonZeroAndDeterministic checks that payload filler
// bytes are reproducible for the same seed and do not collapse to zeroes. This
// keeps tests and diagnostics stable while avoiding unrealistically empty
// payloads that may be treated specially by networks or tooling.
func TestEncodePayloadFillerNonZeroAndDeterministic(t *testing.T) {
	// Encode the first payload using a fixed sequence, timestamp, and seed.
	buf1 := make([]byte, 64)
	EncodePayload(buf1, 1, 0, fillerSeed)

	// Encode the same payload again to check deterministic filler generation.
	buf2 := make([]byte, 64)
	EncodePayload(buf2, 1, 0, fillerSeed)
	assert.Equal(t, buf1, buf2)

	// Check only the filler area; the header bytes are allowed to contain zeroes.
	for _, b := range buf1[HeaderLen:] {
		assert.NotZero(t, b)
	}
}

// TestSeqLossTrackerInOrderNoLoss checks that an uninterrupted sequence stream
// is counted as fully received with no loss or reordering. This is the baseline
// behavior for packet accounting and prevents false loss reports on healthy
// traffic.
func TestSeqLossTrackerInOrderNoLoss(t *testing.T) {
	// Feed a long run of consecutive sequence numbers into the tracker.
	var tr SeqLossTracker
	for i := uint64(0); i < 300; i++ {
		outOfOrder, lost := tr.Received(i)

		// Every in-order packet should be accepted without reporting loss.
		assert.False(t, outOfOrder)
		assert.Zero(t, lost)
	}

	// Snapshot the aggregate counters after the stream has been processed.
	expected, received, lost, outOfOrder := tr.Stats()

	// Confirm the tracker reports exactly the packets that were sent.
	assert.Equal(t, uint64(300), expected)
	assert.Equal(t, uint64(300), received)
	assert.Zero(t, lost)
	assert.Zero(t, outOfOrder)
}

// TestSeqLossTrackerDetectsGapPastWindow checks that a sequence gap becomes
// loss once it is older than the reorder window.
// Delayed packets should get a chance to arrive, but permanently missing
// packets must eventually be counted as loss.
func TestSeqLossTrackerDetectsGapPastWindow(t *testing.T) {
	// Start with the first packet so the tracker has an initial baseline.
	var tr SeqLossTracker
	tr.Received(0)

	// Skip sequence numbers 1..9, then send enough further packets to push the gap out of
	// the trailing reorder window and finalize it as lost.
	tr.Received(10)
	var totalLost uint64
	for i := uint64(11); i < 11+seqWindowBits+10; i++ {
		_, lost := tr.Received(i)
		totalLost += lost
	}

	// The skipped nine packets should be the only packets finalized as lost.
	assert.EqualValues(t, 9, totalLost)
}

// TestSeqLossTrackerReorderWithinWindowNotLost checks that packets arriving
// out of order within the reorder window are not counted as loss. This avoids
// overstating packet loss on paths that reorder packets but still deliver them.
func TestSeqLossTrackerReorderWithinWindowNotLost(t *testing.T) {
	// Receive sequence 2 before sequence 1 to create a temporary gap.
	var tr SeqLossTracker
	tr.Received(0)
	outOfOrder, lost := tr.Received(2)

	// The gap should be considered reordering, not finalized loss.
	assert.True(t, outOfOrder)
	assert.Zero(t, lost)

	// Fill the missing sequence number before it leaves the reorder window.
	outOfOrder, lost = tr.Received(1) // Fills the gap before it falls out of the window.
	assert.True(t, outOfOrder)
	assert.Zero(t, lost)

	// Advance beyond the window to prove the repaired gap is not later counted.
	for i := uint64(3); i < 3+seqWindowBits+10; i++ {
		_, l := tr.Received(i)
		lost += l
	}

	// No loss should be reported because the missing packet eventually arrived.
	assert.Zero(t, lost)
}

// TestJitterEstimatorConstantSpacingConverges checks that identical send and
// receive spacing produces zero jitter. This verifies the estimator does not
// invent variation when the measured packet timing is perfectly stable.
func TestJitterEstimatorConstantSpacingConverges(t *testing.T) {
	// Initialize equal send and receive clocks so transit time stays constant.
	var j JitterEstimator
	send := time.Duration(0)
	recv := time.Duration(0)

	// Feed enough evenly spaced samples for the estimator to settle.
	for i := 0; i < 50; i++ {
		j.Sample(send, recv)
		send += 10 * time.Millisecond
		recv += 10 * time.Millisecond
	}

	// Constant spacing means there is no packet delay variation to report.
	assert.Zero(t, j.Jitter)
}

// TestRateTrackerSnapshot checks that a rate snapshot reports interval counts,
// total counts, and bits-per-second correctly, then resets the interval.
func TestRateTrackerSnapshot(t *testing.T) {
	// Create a tracker at a fixed time so rate calculations are deterministic.
	start := time.Unix(0, 0)
	r := NewRateTracker(start)

	// Add two packets of known size during the first one-second interval.
	r.Add(1000)
	r.Add(1000)
	res := r.Snapshot(start.Add(time.Second))

	// Verify the first snapshot's interval counts, packet count, and bit rate.
	assert.EqualValues(t, 2000, res.Bytes)
	assert.EqualValues(t, 2, res.Packets)
	assert.InDelta(t, 16000, res.BitsPerSec, 0.001)

	// Take a second snapshot with no new traffic to check interval reset behavior.
	res2 := r.Snapshot(start.Add(2 * time.Second))

	// The interval should be empty, but lifetime byte totals should remain.
	assert.Zero(t, res2.Bytes)
	assert.EqualValues(t, 2000, res2.TotalBytes)
}

// TestParseHummingbirdFlag checks accepted hummingbird flag forms and rejects
// malformed input.
func TestParseHummingbirdFlag(t *testing.T) {
	// Parse the required bandwidth and duration fields.
	p, err := parseHummingbirdFlag("3,5s", false)
	require.NoError(t, err)
	assert.EqualValues(t, 3, p.Bw)
	assert.EqualValues(t, 5, p.Duration)
	assert.Zero(t, p.ReverseBw)

	// Parse the optional reverse-bandwidth field.
	p, err = parseHummingbirdFlag("3,5s,2", false)
	require.NoError(t, err)
	assert.EqualValues(t, 2, p.ReverseBw)

	// Malformed input should fail early with a parse error.
	_, err = parseHummingbirdFlag("bad", false)
	assert.Error(t, err)

	p, err = parseHummingbirdFlag("100KBPS,20s,1MBps", true)
	require.NoError(t, err)
	assert.EqualValues(t, 100, p.Bw)
	assert.EqualValues(t, 1000, p.ReverseBw)
	_, err = parseHummingbirdFlag("100,20s", true)
	assert.Error(t, err)
	_, err = parseHummingbirdFlag("3kbps,5s", false)
	assert.Error(t, err)
}

// TestParseBandwidth checks supported bandwidth suffixes and invalid input.
func TestParseBandwidth(t *testing.T) {
	// Define representative inputs for each supported unit and the raw numeric form.
	cases := map[string]float64{
		"1Mbps":   1e6,
		"500Kbps": 500e3,
		"2Gbps":   2e9,
		"100bps":  100,
		"42":      42,
	}

	// Parse each valid case and compare against the expected bits-per-second value.
	for in, want := range cases {
		got, err := parseBandwidth(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}

	// Non-numeric input should be rejected instead of defaulting silently.
	_, err := parseBandwidth("not-a-number")
	assert.Error(t, err)
}

func TestParsePacingBandwidths(t *testing.T) {
	bandwidth, maxBurst, err := parsePacingBandwidths("10Mbps", "20Mbps")
	require.NoError(t, err)
	assert.Equal(t, 10e6, bandwidth)
	assert.Equal(t, 20e6, maxBurst)

	bandwidth, maxBurst, err = parsePacingBandwidths("10Mbps", "")
	require.NoError(t, err)
	assert.Equal(t, bandwidth, maxBurst)

	_, _, err = parsePacingBandwidths("10Mbps", "9Mbps")
	assert.ErrorContains(t, err, "must be finite and >=")
	_, _, err = parsePacingBandwidths("0Mbps", "20Mbps")
	assert.ErrorContains(t, err, "must be finite and positive")
}

func TestPayloadPacer(t *testing.T) {
	start := time.Unix(100, 0)

	t.Run("fractional byte credit produces the exact long-term rate", func(t *testing.T) {
		pacer := newPayloadPacer(start, 800, 10e6, 10e6)
		packets := 0
		for tick := 1; tick <= 8; tick++ {
			now := start.Add(time.Duration(tick) * time.Millisecond)
			pacer.beginTick(now)
			for pacer.canSend(now, 800) {
				pacer.sent(800)
				packets++
			}
		}

		// 10 Mbps schedules 10,000 bytes in 8 ms: twelve packets plus 400 bytes of credit.
		assert.Equal(t, 12, packets)
		assert.Equal(t, uint64(9600), pacer.accountedBytes)
		assert.False(t, pacer.canSend(start.Add(8*time.Millisecond), 800))
		assert.InDelta(t, 400, pacer.scheduledBytes(start.Add(8*time.Millisecond))-
			float64(pacer.accountedBytes), 1e-9)
	})

	t.Run("stalled schedule catches up in max-burst bounded batches", func(t *testing.T) {
		pacer := newPayloadPacer(start, 800, 10e6, 20e6)
		now := start.Add(10 * time.Millisecond)
		pacer.beginTick(now)

		packets := 0
		for pacer.canSend(now, 800) {
			pacer.sent(800)
			packets++
		}

		// A 10 ms stall creates fifteen packets of canonical debt, but the one-tick token bucket
		// permits only a bounded initial batch. The remaining debt is retained for later ticks.
		assert.Equal(t, 4, packets)
		behind, lateness := pacer.behind(now, 800)
		assert.True(t, behind)
		assert.Equal(t, 6800*time.Microsecond, lateness)
	})

	t.Run("absolute schedule uses elapsed time rather than tick count", func(t *testing.T) {
		pacer := newPayloadPacer(start, 800, 10e6, 100e6)
		now := start.Add(10 * time.Millisecond)
		pacer.beginTick(now)
		packets := 0
		for pacer.canSend(now, 800) {
			pacer.sent(800)
			packets++
		}

		assert.Equal(t, 15, packets)
		assert.Equal(t, uint64(12000), pacer.accountedBytes)
	})
}

func TestAdvanceProbeDeadline(t *testing.T) {
	start := time.Unix(100, 0)
	next, rebased := advanceProbeDeadline(start, 10*time.Millisecond, start.Add(35*time.Millisecond))
	assert.True(t, rebased)
	assert.Equal(t, start.Add(45*time.Millisecond), next)
}
