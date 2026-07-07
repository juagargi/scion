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
	"reflect"
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
	"github.com/stretchr/testify/require"
)

// TestHummReplyPather checks that HummReplyPather only caches a reversed
// Hummingbird reservation after SetState sees valid reverse-path state on a
// Hummingbird carrier packet, and otherwise falls back to
// snet.DefaultReplyPather for reply-path construction.
func TestHummReplyPather(t *testing.T) {
	timestamp := util.SecsToTime(123456)
	srcIA := addr.MustParseIA("1-ff00:0:111")
	carrierPath := mustRawHummingbirdPathForReplyPather(t, timestamp)
	validReverseState := mustSerializedReverseReservationState(t, timestamp)

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
		wantSetStateErr       bool
		wantCachedReservation bool
	}{
		"never_set_state": {},
		"set_state_invalid_packet/no_reverse_option": {
			packet:                packetWithoutReverseState(srcIA, carrierPath),
			wantCachedReservation: false,
		},
		"set_state_invalid_packet/non_hummingbird_carrier": {
			packet:                packetWithReverseState(srcIA, mustRawSCIONPathForReplyPather(t, timestamp), validReverseState),
			wantSetStateErr:       true,
			wantCachedReservation: false,
		},
		"set_state_invalid_packet/bad_serialized_state": {
			packet:                packetWithReverseState(srcIA, carrierPath, []byte{0xde, 0xad, 0xbe, 0xef}),
			wantSetStateErr:       true,
			wantCachedReservation: false,
		},
		"set_state_valid_packet": {
			packet:                packetWithReverseState(srcIA, carrierPath, validReverseState),
			wantCachedReservation: true,
		},
	}

	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			rp := path.NewHummReplyPather()
			if tc.packet != nil {
				err := rp.SetState(*tc.packet)
				if tc.wantSetStateErr {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			}

			for replyName, input := range replyInputs {
				replyName, input := replyName, input
				t.Run(replyName, func(t *testing.T) {
					got, err := rp.ReplyPath(cloneRawPath(input))
					if tc.wantCachedReservation {
						// Once a valid reverse reservation is cached, reply-path
						// selection should no longer depend on the incoming path type.
						require.NoError(t, err)
						gotReservation, ok := got.(*path.Reservation)
						require.True(t, ok, "expected cached reservation, got %T", got)

						wantReservation := mustReservationFromReverseState(t, carrierPath, validReverseState, srcIA)
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

	// Nil receivers are currently unsupported; document the panic contract
	// explicitly so future changes are intentional.
	t.Run("nil_receiver", func(t *testing.T) {
		var rp *path.HummReplyPather
		pkt := *packetWithoutReverseState(srcIA, carrierPath)
		rpath := mustRawSCIONPathForReplyPather(t, timestamp)

		require.Panics(t, func() {
			_ = rp.SetState(pkt)
		})
		require.Panics(t, func() {
			_, _ = rp.ReplyPath(rpath)
		})
	})
}

// packetWithoutReverseState builds a packet whose SetState call should behave
// like a no-op for reverse-reservation caching.
func packetWithoutReverseState(srcIA addr.IA, carrierPath snet.RawPath) *snet.Packet {
	return &snet.Packet{
		PacketInfo: snet.PacketInfo{
			Source: snet.SCIONAddress{IA: srcIA},
			Path:   carrierPath,
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
func packetWithReverseState(srcIA addr.IA, carrierPath snet.RawPath, state []byte) *snet.Packet {
	return &snet.Packet{
		PacketInfo: snet.PacketInfo{
			Source: snet.SCIONAddress{IA: srcIA},
			Path:   carrierPath,
			E2eExtnContents: []*slayers.EndToEndOption{
				{
					OptType: slayers.OptTypeReversePath,
					OptData: append([]byte(nil), state...),
				},
			},
		},
	}
}

// mustRawSCIONPathForReplyPather serializes the synthetic SCION path fixture
// used by the reply-pather tests into an snet.RawPath.
func mustRawSCIONPathForReplyPather(t *testing.T, when time.Time) snet.RawPath {
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
func mustRawHummingbirdPathForReplyPather(t *testing.T, when time.Time) snet.RawPath {
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
func mustRawEPICPath(t *testing.T, when time.Time) snet.RawPath {
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
func mustRawOneHopPath(t *testing.T, when time.Time) snet.RawPath {
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
func mustSerializedReverseReservationState(t *testing.T, when time.Time) []byte {
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

// mustReservationFromReverseState reconstructs the reservation that SetState is
// expected to cache for successful bidirectional-reply setup.
func mustReservationFromReverseState(
	t *testing.T,
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
func mustReversedSCIONPathForReplyPather(t *testing.T, when time.Time) path.SCION {
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
func mustSerializeReservation(t *testing.T, reservation *path.Reservation) []byte {
	t.Helper()

	raw := make([]byte, reservation.SerializedLen())
	require.NoError(t, reservation.Serialize(raw))
	return raw
}

// mustSerializeSlayersPath encodes a decoded slayers path into bytes so tests
// can compare reply-path results independently of concrete Go values.
func mustSerializeSlayersPath(t *testing.T, p dppath.Path) []byte {
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
