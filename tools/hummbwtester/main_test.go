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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeaderRoundTrip(t *testing.T) {
	buf := make([]byte, HeaderLen)
	EncodeHeader(buf, Header{
		Type:               PacketTypePongRequest,
		SequenceNumber:     42,
		SendTimestampNanos: 1234567890,
	})
	got, err := DecodeHeader(buf)
	require.NoError(t, err)
	assert.Equal(t, PacketTypePongRequest, got.Type)
	assert.Equal(t, uint64(42), got.SequenceNumber)
	assert.Equal(t, int64(1234567890), got.SendTimestampNanos)
}

func TestDecodeHeaderVersionMismatch(t *testing.T) {
	buf := make([]byte, HeaderLen)
	EncodeHeader(buf, Header{Type: PacketTypePayload})
	buf[0] = Version + 1
	_, err := DecodeHeader(buf)
	assert.Error(t, err)
}

func TestPongReplyRoundTrip(t *testing.T) {
	buf := make([]byte, PongReplyLen)
	EncodePongReply(buf, PongReply{
		Header: Header{
			Type:               PacketTypePongReply,
			SequenceNumber:     7,
			SendTimestampNanos: 100,
		},
		ServerRecvTimestampNanos: 200,
		ServerSendTimestampNanos: 300,
	})
	got, err := DecodePongReply(buf)
	require.NoError(t, err)
	assert.Equal(t, uint64(7), got.SequenceNumber)
	assert.Equal(t, int64(100), got.SendTimestampNanos)
	assert.Equal(t, int64(200), got.ServerRecvTimestampNanos)
	assert.Equal(t, int64(300), got.ServerSendTimestampNanos)
}

func TestEncodePayloadFillerNonZeroAndDeterministic(t *testing.T) {
	buf1 := make([]byte, 64)
	EncodePayload(buf1, 1, 0, fillerSeed)
	buf2 := make([]byte, 64)
	EncodePayload(buf2, 1, 0, fillerSeed)
	assert.Equal(t, buf1, buf2)
	for _, b := range buf1[HeaderLen:] {
		assert.NotZero(t, b)
	}
}

func TestSeqLossTrackerInOrderNoLoss(t *testing.T) {
	var tr SeqLossTracker
	for i := uint64(0); i < 300; i++ {
		outOfOrder, lost := tr.Received(i)
		assert.False(t, outOfOrder)
		assert.Zero(t, lost)
	}
	expected, received, lost, outOfOrder := tr.Stats()
	assert.Equal(t, uint64(300), expected)
	assert.Equal(t, uint64(300), received)
	assert.Zero(t, lost)
	assert.Zero(t, outOfOrder)
}

func TestSeqLossTrackerDetectsGapPastWindow(t *testing.T) {
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
	assert.EqualValues(t, 9, totalLost)
}

func TestSeqLossTrackerReorderWithinWindowNotLost(t *testing.T) {
	var tr SeqLossTracker
	tr.Received(0)
	outOfOrder, lost := tr.Received(2)
	assert.True(t, outOfOrder)
	assert.Zero(t, lost)
	outOfOrder, lost = tr.Received(1) // Fills the gap before it falls out of the window.
	assert.True(t, outOfOrder)
	assert.Zero(t, lost)

	for i := uint64(3); i < 3+seqWindowBits+10; i++ {
		_, l := tr.Received(i)
		lost += l
	}
	assert.Zero(t, lost)
}

func TestJitterEstimatorConstantSpacingConverges(t *testing.T) {
	var j JitterEstimator
	send := time.Duration(0)
	recv := time.Duration(0)
	for i := 0; i < 50; i++ {
		j.Sample(send, recv)
		send += 10 * time.Millisecond
		recv += 10 * time.Millisecond
	}
	assert.Zero(t, j.Jitter)
}

func TestRateTrackerSnapshot(t *testing.T) {
	start := time.Unix(0, 0)
	r := NewRateTracker(start)
	r.Add(1000)
	r.Add(1000)
	res := r.Snapshot(start.Add(time.Second))
	assert.EqualValues(t, 2000, res.Bytes)
	assert.EqualValues(t, 2, res.Packets)
	assert.InDelta(t, 16000, res.BitsPerSec, 0.001)

	res2 := r.Snapshot(start.Add(2 * time.Second))
	assert.Zero(t, res2.Bytes)
	assert.EqualValues(t, 2000, res2.TotalBytes)
}

func TestParseHummingbirdFlag(t *testing.T) {
	p, err := parseHummingbirdFlag("3,5s")
	require.NoError(t, err)
	assert.EqualValues(t, 3, p.Bw)
	assert.EqualValues(t, 5, p.Duration)
	assert.Zero(t, p.ReverseBw)

	p, err = parseHummingbirdFlag("3,5s,2")
	require.NoError(t, err)
	assert.EqualValues(t, 2, p.ReverseBw)

	_, err = parseHummingbirdFlag("bad")
	assert.Error(t, err)
}

func TestParseBandwidth(t *testing.T) {
	cases := map[string]float64{
		"1Mbps":   1e6,
		"500Kbps": 500e3,
		"2Gbps":   2e9,
		"100bps":  100,
		"42":      42,
	}
	for in, want := range cases {
		got, err := parseBandwidth(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := parseBandwidth("not-a-number")
	assert.Error(t, err)
}
