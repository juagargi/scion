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
	"crypto/cipher"
	"encoding/hex"
	"testing"
	"time"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/private/util"
	"github.com/scionproto/scion/pkg/segment/iface"
	"github.com/scionproto/scion/pkg/slayers"
	dppath "github.com/scionproto/scion/pkg/slayers/path"
	"github.com/scionproto/scion/pkg/slayers/path/epic"
	dphumm "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	"github.com/scionproto/scion/pkg/slayers/path/scion"
	"github.com/scionproto/scion/pkg/snet"
	"github.com/scionproto/scion/pkg/snet/path"
	"github.com/stretchr/testify/require"
)

// TestInterfacesToBaseHops checks that the InterfacesToBaseHops function correctly maps the
// path individual interfaces to a BaseHop sequence. We use tiny topo's 111->112 path here.
func TestInterfacesToBaseHops(t *testing.T) {
	t.Parallel()
	ifaces := []snet.PathInterface{
		{IA: addr.MustParseIA("1-ff00:0:111"), ID: iface.ID(41)},
		{IA: addr.MustParseIA("1-ff00:0:110"), ID: iface.ID(1)},
		{IA: addr.MustParseIA("1-ff00:0:110"), ID: iface.ID(2)},
		{IA: addr.MustParseIA("1-ff00:0:112"), ID: iface.ID(1)},
	}
	expected := []path.BaseHop{
		{IA: addr.MustParseIA("1-ff00:0:111"), Ingress: 0, Egress: 41},
		{IA: addr.MustParseIA("1-ff00:0:110"), Ingress: 1, Egress: 2},
		{IA: addr.MustParseIA("1-ff00:0:112"), Ingress: 1, Egress: 0},
	}
	got := path.InterfacesToBaseHops(ifaces)
	require.Equal(t, expected, got)
}

func TestSetFlyover(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)
	r := path.Reservation{
		DstIA: addr.MustParseIA("1-ff00:0:112"),
		Dec:   createHummingbirdPath(referenceTime),
	}
	r.Hops = make([]*path.Hop, len(r.Dec.HopFields))
	*r.AesBlocks() = make([]cipher.Block, len(r.Hops))
	// There are 4 hops in the path:
	require.Equal(t, 4, len(r.Hops))

	// Mock a flyover in AS 110 between ingress 1 and egress 2.
	flyoverData := path.Hop{
		BaseHop: path.BaseHop{
			IA:      addr.MustParseIA("1-ff00:0:110"),
			Ingress: 1,
			Egress:  2,
		},
		Flyover: &path.FlyoverData{
			ResID:     1,
			Bw:        1,
			StartTime: referenceEpochTime,
			Duration:  10,
			// Ak: [16]byte{},
		},
	}
	// Set the flyover to the first hop of the xover hop. Hops are:
	// - [0] 111[0] -> 111[41]
	// - [1] 110[1] -> 110[0]
	// - [2] 110[0] -> 110[2]
	// - [3] 112[1] -> 112[0]
	err := r.SetHopAndFlyover(1, &flyoverData)
	require.NoError(t, err)

	// Check that the hop indeed has the flyover.
	require.NotNil(t, r.Hops[1])
	// The xover hop corresponds to the first segment, check its size:
	require.Equal(t, 8, int(r.Dec.PathMeta.SegLen[0]))
	// Check that the second segment doesn't have any flyovers:
	require.Equal(t, 6, int(r.Dec.PathMeta.SegLen[1]))
}

func TestWithScionDataplane(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)

	scionDec := createScionPath(referenceTime)
	snetScion := path.SCION{
		Raw: make([]byte, scionDec.Len()),
	}
	err := scionDec.SerializeTo(snetScion.Raw)
	require.NoError(t, err)

	seq := createFlyoverSequence(t, referenceEpochTime)

	r, err := path.NewReservation(
		path.WithDataplanePath(snetScion, addr.MustParseIA("1-ff00:0:112"), seq),
	)
	require.NoError(t, err)
	require.NotNil(t, r)
	require.Equal(t, addr.MustParseIA("1-ff00:0:112"), r.DstIA)

	// The path contains two segments, i.e. one xover hop.
	// There should only be three flyovers, check it.
	// The hops are:			With Flyover
	// - [0] 111[0] -> 111[41]		*
	// - [1] 110[1] -> 110[0]		*
	// - [2] 110[0] -> 110[2]
	// - [3] 112[1] -> 112[0]		*
	require.Len(t, r.Hops, 4)
	checkHop(t, r.Hops[0], "", 0, 41, true)
	checkHop(t, r.Hops[1], "", 1, 2, true)
	checkHop(t, r.Hops[2], "", 999, 999, false) // ingress and egress don't matter here
	checkHop(t, r.Hops[3], "", 1, 0, true)
}

// TestWithScionDataplaneCutSegment checks a reservation for a path that starts inside a
// segment, as SCIERA 71-1916 -> 71-2:0:5c: the flyovers are bought for the hops of the path
// metadata, 71-1916 0->103, while the first hop field still reads 2->103.
func TestWithScionDataplaneCutSegment(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)
	src, dst := addr.MustParseIA("71-1916"), addr.MustParseIA("71-2:0:5c")

	scionDec := &scion.Decoded{
		Base: scion.Base{
			PathMeta: scion.MetaHdr{SegLen: [3]uint8{2, 0, 0}},
			NumINF:   1,
			NumHops:  2,
		},
		InfoFields: []dppath.InfoField{
			{ConsDir: true, Timestamp: util.TimeToSecs(referenceTime)},
		},
		HopFields: []dppath.HopField{
			{ConsIngress: 2, ConsEgress: 103, ExpTime: 8},
			{ConsIngress: 104, ConsEgress: 0, ExpTime: 8},
		},
	}
	snetScion := path.SCION{Raw: make([]byte, scionDec.Len())}
	require.NoError(t, scionDec.SerializeTo(snetScion.Raw))

	sequence := func(srcEgress uint16) path.FlyoverSequence {
		return path.FlyoverSequence{
			&path.Hop{
				BaseHop: path.BaseHop{IA: src, Ingress: 0, Egress: srcEgress},
				Flyover: createFlyover(t, referenceEpochTime),
			},
			&path.Hop{
				BaseHop: path.BaseHop{IA: dst, Ingress: 104, Egress: 0},
				Flyover: createFlyover(t, referenceEpochTime),
			},
		}
	}

	r, err := path.NewReservation(path.WithDataplanePath(snetScion, dst, sequence(103)))
	require.NoError(t, err)
	require.Len(t, r.Hops, 2)
	checkHop(t, r.Hops[0], "71-1916", 0, 103, true)
	checkHop(t, r.Hops[1], "71-2:0:5c", 104, 0, true)

	// A flyover for an interface the path does not use is still rejected.
	_, err = path.NewReservation(path.WithDataplanePath(snetScion, dst, sequence(3)))
	require.ErrorContains(t, err, "mismatch hop parameter and data-plane")
}

func TestWithHummDataplane(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)

	hummDec := createHummingbirdPath(referenceTime)
	snetHumm := &path.Reservation{
		Dec: hummDec,
	}
	seq := createFlyoverSequence(t, referenceEpochTime)

	r, err := path.NewReservation(
		path.WithDataplanePath(snetHumm, addr.MustParseIA("1-ff00:0:112"), seq),
	)
	require.NoError(t, err)
	require.Same(t, hummDec, r.Dec)
	require.Len(t, r.Hops, 4)

	checkHop(t, r.Hops[0], "", 0, 41, true)
	checkHop(t, r.Hops[1], "", 1, 2, true)
	checkHop(t, r.Hops[2], "", 999, 999, false)
	checkHop(t, r.Hops[3], "", 1, 0, true)
}

func TestReservationEndToEndExtn(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)
	seq := createFlyoverSequence(t, referenceEpochTime)

	scionDec := createScionPath(referenceTime)
	snetScion := path.SCION{
		Raw: make([]byte, scionDec.Len()),
	}
	require.NoError(t, scionDec.SerializeTo(snetScion.Raw))

	reservation, err := path.NewReservation(
		path.WithDataplanePath(snetScion, addr.MustParseIA("1-ff00:0:112"), seq),
	)
	require.NoError(t, err)

	extn, err := reservation.EndToEndExtn()
	require.NoError(t, err)
	require.Nil(t, extn)

	first := &slayers.EndToEndExtn{
		Options: []*slayers.EndToEndOption{
			{
				OptType: slayers.OptTypeReversePath,
				OptData: []byte{1, 2, 3, 4},
			},
		},
	}
	reservation.SetReverseReservationExtn(first)

	extn, err = reservation.EndToEndExtn()
	require.NoError(t, err)
	require.Same(t, first, extn)
	require.Len(t, extn.Options, 1)
	require.Equal(t, slayers.OptTypeReversePath, extn.Options[0].OptType)
	require.Equal(t, []byte{1, 2, 3, 4}, extn.Options[0].OptData)

	extn, err = reservation.EndToEndExtn()
	require.NoError(t, err)
	require.Nil(t, extn)

	second := &slayers.EndToEndExtn{
		Options: []*slayers.EndToEndOption{
			{
				OptType: slayers.OptTypeReversePath,
				OptData: []byte{5, 6, 7, 8},
			},
		},
	}
	reservation.SetReverseReservationExtn(second)

	extn, err = reservation.EndToEndExtn()
	require.NoError(t, err)
	require.Same(t, second, extn)
	require.Len(t, extn.Options, 1)
	require.Equal(t, slayers.OptTypeReversePath, extn.Options[0].OptType)
	require.Equal(t, []byte{5, 6, 7, 8}, extn.Options[0].OptData)
}

func TestWithRawPath(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)

	seq := createFlyoverSequence(t, referenceEpochTime)
	t.Run("scion", func(t *testing.T) {
		p := createScionPath(referenceTime)
		buff := make([]byte, p.Len())
		err := p.SerializeTo(buff)
		require.NoError(t, err)
		rawPath := snet.RawPath{
			PathType: scion.PathType,
			Raw:      buff,
		}

		r, err := path.NewReservation(
			path.WithRawPath(rawPath, addr.MustParseIA("1-ff00:0:112"), seq),
		)
		require.NoError(t, err)
		require.NotNil(t, r)
		require.Len(t, r.Hops, 4)
		checkHop(t, r.Hops[0], "", 0, 41, true)
		checkHop(t, r.Hops[1], "", 1, 2, true)
		checkHop(t, r.Hops[2], "", 999, 999, false)
		checkHop(t, r.Hops[3], "", 1, 0, true)
	})
	t.Run("hummingbird", func(t *testing.T) {
		p := createHummingbirdPath(referenceTime)
		buff := make([]byte, p.Len())
		err := p.SerializeTo(buff)
		require.NoError(t, err)
		rawPath := snet.RawPath{
			PathType: dphumm.PathType,
			Raw:      buff,
		}

		r, err := path.NewReservation(
			path.WithRawPath(rawPath, addr.MustParseIA("1-ff00:0:112"), seq),
		)
		require.NoError(t, err)
		require.NotNil(t, r)
		require.Len(t, r.Hops, 4)
		checkHop(t, r.Hops[0], "", 0, 41, true)
		checkHop(t, r.Hops[1], "", 1, 2, true)
		checkHop(t, r.Hops[2], "", 999, 999, false)
		checkHop(t, r.Hops[3], "", 1, 0, true)
	})
	t.Run("epic", func(t *testing.T) {
		scionRaw, err := createScionPath(referenceTime).ToRaw()
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
		err = epicPath.SerializeTo(buff)
		require.NoError(t, err)

		rawPath := snet.RawPath{
			PathType: epic.PathType,
			Raw:      buff,
		}
		r, err := path.NewReservation(
			path.WithRawPath(rawPath, addr.MustParseIA("1-ff00:0:112"), seq),
		)
		require.Error(t, err)
		require.Nil(t, r)
		require.Contains(t, err.Error(), "unsupported path type")
	})
}

// TestSetScionPathClonesMacFields checks that Reservation.setScionPath clones the values of
// the MAC fields of the SCION path.
func TestSetScionPathClonesMacFields(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)

	// Create a path and remember one of the MAC fields.
	scionDec := createScionPath(referenceTime)
	originalMac := scionDec.HopFields[0].Mac // Copy the array (clone).

	r := &path.Reservation{}
	err := r.SetScionPath(scionDec)
	require.NoError(t, err)

	// Modify one of the MAC fields.
	scionDec.HopFields[0].Mac[1] = 42
	require.NotEqual(t, originalMac, scionDec.HopFields[0])
	// Check the cloned one in Reservation still has the original value.
	require.Equal(t, originalMac, r.GetScionMACs()[0])

	// Now modify also one MAC field in the internal hummingbird path.
	r.Dec.HopFields[0].HopField.Mac[1] = 42
	require.NotEqual(t, originalMac, r.Dec.HopFields[0].HopField.Mac[1])
	require.Equal(t, originalMac, r.GetScionMACs()[0])
}

func TestHopsBitset(t *testing.T) {
	// buff [0] and [3] will be unused, [1] and [2] for the bitset.
	buff := make([]byte, 4)

	nBits := 9
	flags := path.NewHopBitSet(buff[1:], nBits)
	flags.Set(0, true)
	require.True(t, flags.Get(0))
	require.Equal(t, byte(0x01), buff[1])
	flags.Set(8, true)
	require.True(t, flags.Get(8))
	require.Equal(t, byte(0x01), buff[2])
}

func TestSerializeHop(t *testing.T) {
	h := createHopWithFlyover(t)
	h.Flyover = nil
	buff := make([]byte, 1)
	n, err := h.Serialize(buff)
	require.Error(t, err)
	buff = make([]byte, path.HopNoFlyoverLen)
	n, err = h.Serialize(buff)
	require.NoError(t, err)
	require.Equal(t, path.HopNoFlyoverLen, n)
	t.Logf("hop: %s", hex.EncodeToString(buff))

	// With a flyover.
	h = createHopWithFlyover(t)
	buff2 := make([]byte, path.HopNoFlyoverLen)
	n, err = h.Serialize(buff2)
	require.Error(t, err)
	buff2 = make([]byte, path.HopWithFlyoverLen)
	n, err = h.Serialize(buff2)
	require.NoError(t, err)
	require.Equal(t, path.HopWithFlyoverLen, n)
	t.Logf("hop with flyover: %s", hex.EncodeToString(buff2))

	// The initial part must be the same for both buffers.
	require.Equal(t, buff, buff2[:len(buff)])
}

// TestSerializeDeserializeHop checks that a Hop can be serialized and deserialized.
func TestSerializeDeserializeHop(t *testing.T) {
	h := createHopWithFlyover(t)
	h.Flyover = nil
	// Serialize:
	buff := make([]byte, h.Len())
	_, err := h.Serialize(buff)
	require.NoError(t, err)
	t.Logf("hop: %s", hex.EncodeToString(buff))
	// Deserialize:
	h2 := &path.Hop{}
	err = h2.Deserialize(buff, false)
	require.NoError(t, err)
	require.Equal(t, h, h2)

	// Same, with flyover.
	h = createHopWithFlyover(t)
	// Serialize:
	buff = make([]byte, h.Len())
	_, err = h.Serialize(buff)
	require.NoError(t, err)
	t.Logf("hop with flyover: %s", hex.EncodeToString(buff))
	// Deserialize:
	h2 = &path.Hop{}
	err = h2.Deserialize(buff, true)
	require.NoError(t, err)
	require.Equal(t, h, h2)
}

// TestSerializeDeserializeMultipleHops checks that a sequence of Hop can be serialized to
// bytes, and deserialized from them.
func TestSerializeDeserializeMultipleHops(t *testing.T) {
	hops := make([]*path.Hop, 1)
	hops[0] = createHopWithFlyover(t)
	// Serialize errors.
	buff := make([]byte, 1)
	n, err := path.SerializeHops(buff, hops)
	require.Error(t, err)
	// Serialize.
	buff = make([]byte, path.LenOfSerializedHops(hops))
	n, err = path.SerializeHops(buff, hops)
	require.NoError(t, err)
	require.Equal(t, len(buff), n)
	// Deserialize errors.
	gotHops, err := path.DeserializeHops([]byte{1})
	require.Error(t, err)
	// Deserialize.
	gotHops, err = path.DeserializeHops(nil)
	require.NoError(t, err)
	require.Len(t, gotHops, 0)
	gotHops, err = path.DeserializeHops([]byte{0})
	require.NoError(t, err)
	require.Len(t, gotHops, 0)
	// Deserialize real hops.
	gotHops, err = path.DeserializeHops(buff)
	require.NoError(t, err)
	require.Equal(t, hops, gotHops)

	// Create some hops. With flyover denoted by F, without by -.
	// -FF--F-
	hops = make([]*path.Hop, 7)
	for i := range hops {
		hops[i] = createHopWithFlyover(t)
		hops[i].Ingress = uint16(i + 42)
	}
	hops[0].Flyover = nil
	hops[3].Flyover = nil
	hops[4].Flyover = nil
	hops[6].Flyover = nil
	// Serialize / deserialize.
	buff = make([]byte, path.LenOfSerializedHops(hops))
	n, err = path.SerializeHops(buff, hops)
	require.NoError(t, err)
	require.Equal(t, len(buff), n)
	t.Logf("serialize 7 hops: %s", hex.EncodeToString(buff))
	// Deserialize the 7 hops.
	gotHops, err = path.DeserializeHops(buff)
	require.NoError(t, err)
	require.Equal(t, hops, gotHops)

	// Create some hops, including nil entries. With flyover denoted by F, without by -, and
	// nil entries by x.
	// xF-x-Fx
	hops = make([]*path.Hop, 7)
	for _, i := range []int{1, 2, 4, 5} {
		hops[i] = createHopWithFlyover(t)
		hops[i].Ingress = uint16(i + 52)
	}
	hops[2].Flyover = nil
	hops[5].Flyover = nil
	// Serialize / deserialize.
	buff = make([]byte, path.LenOfSerializedHops(hops))
	n, err = path.SerializeHops(buff, hops)
	require.NoError(t, err)
	require.Equal(t, len(buff), n)
	t.Logf("serialize 7 hops with nil entries: %s", hex.EncodeToString(buff))
	// Deserialize the 7 hops with nil entries.
	gotHops, err = path.DeserializeHops(buff)
	require.NoError(t, err)
	require.Equal(t, hops, gotHops)
}

// TestDataplaneToBaseHops checks the mapping from a dataplane path to its logical hop sequence.
func TestDataplaneToBaseHops(t *testing.T) {
	t.Parallel()

	plain := func(in, eg uint16) dphumm.FlyoverHopField {
		return dphumm.FlyoverHopField{
			HopField: dppath.HopField{ConsIngress: in, ConsEgress: eg},
		}
	}
	flyover := func(in, eg uint16) dphumm.FlyoverHopField {
		h := plain(in, eg)
		h.Flyover = true
		h.ResID = 42
		h.Bw = 129
		h.Duration = 301
		return h
	}

	testCases := map[string]struct {
		infos []dppath.InfoField
		// hops holds the hop fields of each segment.
		hops [][]dphumm.FlyoverHopField
		// wantErr expects the mapping to be rejected; wantHops and wantIndices are then unused.
		wantErr     bool
		wantHops    []path.BaseHop
		wantIndices []uint8
	}{
		// One down segment, cons dir so no ingress/egress swap:
		// AS a [0->11], AS b [12->13], AS c [14->0]. The next three cases repeat it with
		// increasingly many flyovers, which change the segment's line count but not its hop
		// field count, so the answer must not move.
		"one segment, no flyovers": {
			infos: []dppath.InfoField{{ConsDir: true}},
			hops: [][]dphumm.FlyoverHopField{
				{plain(0, 11), plain(12, 13), plain(14, 0)},
			},
			wantHops:    []path.BaseHop{{Ingress: 0, Egress: 11}, {Ingress: 12, Egress: 13}, {Ingress: 14, Egress: 0}},
			wantIndices: []uint8{0, 1, 2},
		},
		"one segment, one flyover": {
			// SegLen is 11 lines and SegLen/HopLines is 3, so the old derivation happened to
			// agree here. It stops agreeing at two flyovers.
			infos: []dppath.InfoField{{ConsDir: true}},
			hops: [][]dphumm.FlyoverHopField{
				{flyover(0, 11), plain(12, 13), plain(14, 0)},
			},
			wantHops:    []path.BaseHop{{Ingress: 0, Egress: 11}, {Ingress: 12, Egress: 13}, {Ingress: 14, Egress: 0}},
			wantIndices: []uint8{0, 1, 2},
		},
		"one segment, two flyovers": {
			// SegLen is 16 lines; SegLen/HopLines would be 5, one more than the path holds.
			infos: []dppath.InfoField{{ConsDir: true}},
			hops: [][]dphumm.FlyoverHopField{
				{flyover(0, 11), flyover(12, 13), plain(14, 15), plain(16, 0)},
			},
			wantHops: []path.BaseHop{
				{Ingress: 0, Egress: 11}, {Ingress: 12, Egress: 13},
				{Ingress: 14, Egress: 15}, {Ingress: 16, Egress: 0},
			},
			wantIndices: []uint8{0, 1, 2, 3},
		},
		"one segment, all flyovers": {
			// SegLen is 20 lines; SegLen/HopLines would be 6.
			infos: []dppath.InfoField{{ConsDir: true}},
			hops: [][]dphumm.FlyoverHopField{
				{flyover(0, 11), flyover(12, 13), flyover(14, 15), flyover(16, 0)},
			},
			wantHops: []path.BaseHop{
				{Ingress: 0, Egress: 11}, {Ingress: 12, Egress: 13},
				{Ingress: 14, Egress: 15}, {Ingress: 16, Egress: 0},
			},
			wantIndices: []uint8{0, 1, 2, 3},
		},
		// Tiny topo 111->112: an up segment and a down segment glued at core AS 110.
		// Crossover at hop fields 1 and 2, they fold into the single logical hop 110 [1->2],
		// carried only by hop field 1.
		"two segments, crossover": {
			infos: []dppath.InfoField{{ConsDir: false}, {ConsDir: true}},
			hops: [][]dphumm.FlyoverHopField{
				{plain(41, 0), plain(0, 1)},
				{plain(0, 2), plain(1, 0)},
			},
			wantHops: []path.BaseHop{
				{Ingress: 0, Egress: 41}, {Ingress: 1, Egress: 2}, {Ingress: 1, Egress: 0},
			},
			wantIndices: []uint8{0, 1, 3},
		},
		"two segments, crossover, flyovers in both": {
			// The same path with five-line hop fields throughout: the answer must not move.
			infos: []dppath.InfoField{{ConsDir: false}, {ConsDir: true}},
			hops: [][]dphumm.FlyoverHopField{
				{flyover(41, 0), flyover(0, 1)},
				{flyover(0, 2), flyover(1, 0)},
			},
			wantHops: []path.BaseHop{
				{Ingress: 0, Egress: 41}, {Ingress: 1, Egress: 2}, {Ingress: 1, Egress: 0},
			},
			wantIndices: []uint8{0, 1, 3},
		},
		// A shortcut joins two segments at a non-core AS, so that AS sits mid-segment in both,
		// and each of its hop fields carries a non-zero ConsIngress: interface 99,
		// toward its parent, which the packet never traverses.
		// The crossover fold must still produce 71->72 and drop 99 unconditionally without
		// assuming that egress was zero.
		// The control plane makes the same choice from the other side,
		// by leaving interface 99 out of the path metadata entirely.
		"two segments, shortcut at a non-core AS": {
			infos: []dppath.InfoField{{ConsDir: false}, {ConsDir: true}},
			hops: [][]dphumm.FlyoverHopField{
				{plain(41, 0), plain(99, 71)},
				{plain(99, 72), plain(1, 0)},
			},
			wantHops: []path.BaseHop{
				{Ingress: 0, Egress: 41}, {Ingress: 71, Egress: 72}, {Ingress: 1, Egress: 0},
			},
			wantIndices: []uint8{0, 1, 3},
		},
		// SCIERA 71-1916 -> 71-2:0:5c: a down segment from a core AS enters 71-1916 through
		// interface 2 and leaves through 103 towards its child. A path from 71-1916 uses the
		// segment from there on, keeping the hop field 2->103, but the packet is sent from
		// 71-1916 itself: its ingress is 0, as the border router uses for the flyover.
		"one segment, cut at its start": {
			infos: []dppath.InfoField{{ConsDir: true}},
			hops: [][]dphumm.FlyoverHopField{
				{flyover(2, 103), flyover(104, 0)},
			},
			wantHops:    []path.BaseHop{{Ingress: 0, Egress: 103}, {Ingress: 104, Egress: 0}},
			wantIndices: []uint8{0, 1},
		},
		// The reverse path, 71-2:0:5c -> 71-1916, uses the up segment of 71-2:0:5c cut at
		// 71-1916: against construction direction its hop field reads 103->2, but the packet is
		// delivered in 71-1916, so its egress is 0.
		"one segment, cut at its end": {
			infos: []dppath.InfoField{{ConsDir: false}},
			hops: [][]dphumm.FlyoverHopField{
				{plain(104, 0), plain(2, 103)},
			},
			wantHops:    []path.BaseHop{{Ingress: 0, Egress: 104}, {Ingress: 103, Egress: 0}},
			wantIndices: []uint8{0, 1},
		},
		"three segments, two crossovers": {
			infos: []dppath.InfoField{{ConsDir: false}, {ConsDir: true}, {ConsDir: true}},
			hops: [][]dphumm.FlyoverHopField{
				{plain(41, 0), plain(0, 1)},
				{plain(0, 2), plain(1, 0)},
				{plain(0, 3), plain(2, 0)},
			},
			wantHops: []path.BaseHop{
				{Ingress: 0, Egress: 41}, {Ingress: 1, Egress: 2},
				{Ingress: 1, Egress: 3}, {Ingress: 2, Egress: 0},
			},
			wantIndices: []uint8{0, 1, 3, 5},
		},
		// AS 5 --(511|151)-- AS 1 ==peer(121|211)== AS 2. Hop fields 1 and 2 sit on either side
		// of the peering link and belong to different ASes, so neither folds away and both can
		// carry a flyover. The destination AS is the peer itself, which is what leaves the last
		// segment holding a single hop field.
		//
		// This and the next three cases are the four shapes braccept covers in
		// hummingbirdPeeringCase.
		"peering, child to peer": {
			infos: []dppath.InfoField{
				{ConsDir: false, Peer: true}, {ConsDir: true, Peer: true},
			},
			hops: [][]dphumm.FlyoverHopField{
				{plain(511, 0), plain(121, 151)},
				{plain(211, 0)},
			},
			wantHops: []path.BaseHop{
				{Ingress: 0, Egress: 511}, {Ingress: 151, Egress: 121}, {Ingress: 211, Egress: 0},
			},
			wantIndices: []uint8{0, 1, 2},
		},
		"peering, child to peer, flyover": {
			// braccept HummingbirdFlyoverChildToPeer. The five-line hop field must not move the
			// answer.
			infos: []dppath.InfoField{
				{ConsDir: false, Peer: true}, {ConsDir: true, Peer: true},
			},
			hops: [][]dphumm.FlyoverHopField{
				{plain(511, 0), flyover(121, 151)},
				{plain(211, 0)},
			},
			wantHops: []path.BaseHop{
				{Ingress: 0, Egress: 511}, {Ingress: 151, Egress: 121}, {Ingress: 211, Egress: 0},
			},
			wantIndices: []uint8{0, 1, 2},
		},
		"peering, upstream": {
			// braccept HummingbirdBestEffortPeeringUpstream: one hop further from the peering
			// link, so the first segment holds three hop fields.
			infos: []dppath.InfoField{
				{ConsDir: false, Peer: true}, {ConsDir: true, Peer: true},
			},
			hops: [][]dphumm.FlyoverHopField{
				{plain(511, 0), plain(121, 151), plain(121, 0)},
				{plain(211, 0)},
			},
			wantHops: []path.BaseHop{
				{Ingress: 0, Egress: 511}, {Ingress: 151, Egress: 121},
				{Ingress: 0, Egress: 121}, {Ingress: 211, Egress: 0},
			},
			wantIndices: []uint8{0, 1, 2, 3},
		},
		"peering, upstream, flyover": {
			// braccept HummingbirdFlyoverPeeringUpstream.
			infos: []dppath.InfoField{
				{ConsDir: false, Peer: true}, {ConsDir: true, Peer: true},
			},
			hops: [][]dphumm.FlyoverHopField{
				{plain(511, 0), flyover(121, 151), plain(121, 0)},
				{plain(211, 0)},
			},
			wantHops: []path.BaseHop{
				{Ingress: 0, Egress: 511}, {Ingress: 151, Egress: 121},
				{Ingress: 0, Egress: 121}, {Ingress: 211, Egress: 0},
			},
			wantIndices: []uint8{0, 1, 2, 3},
		},
		// A path is either peering or not: both of its info fields carry the peer flag, or
		// neither does. Only one of them carrying it leaves no way to tell whether the two
		// segments are joined by a crossover or by a peering link, so the path is rejected
		// rather than guessed at. Unlike a bad segment count, this one survives the wire: the
		// peer flag is a per-info-field bit and nothing cross-checks the two while decoding.
		"inconsistent peering, peer then non-peer": {
			infos: []dppath.InfoField{
				{ConsDir: false, Peer: true}, {ConsDir: true, Peer: false},
			},
			hops: [][]dphumm.FlyoverHopField{
				{plain(511, 0), plain(121, 151)},
				{plain(211, 0)},
			},
			wantErr: true,
		},
		"inconsistent peering, non-peer then peer": {
			infos: []dppath.InfoField{
				{ConsDir: false, Peer: false}, {ConsDir: true, Peer: true},
			},
			hops: [][]dphumm.FlyoverHopField{
				{plain(511, 0), plain(121, 151)},
				{plain(211, 0)},
			},
			wantErr: true,
		},
		"peering, two hops after the link": {
			infos: []dppath.InfoField{
				{ConsDir: false, Peer: true}, {ConsDir: true, Peer: true},
			},
			hops: [][]dphumm.FlyoverHopField{
				{plain(511, 0), plain(121, 151)},
				{plain(211, 212), plain(1, 0)},
			},
			wantHops: []path.BaseHop{
				{Ingress: 0, Egress: 511}, {Ingress: 151, Egress: 121},
				{Ingress: 211, Egress: 212}, {Ingress: 1, Egress: 0},
			},
			wantIndices: []uint8{0, 1, 2, 3},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dec := &dphumm.Decoded{
				Base:       dphumm.Base{NumINF: len(tc.infos)},
				InfoFields: tc.infos,
			}
			for segIdx, seg := range tc.hops {
				for _, hf := range seg {
					lines := dphumm.HopLines
					if hf.Flyover {
						lines = dphumm.FlyoverLines
					}
					dec.PathMeta.SegLen[segIdx] += uint8(lines)
					dec.NumLines += lines
					dec.HopFields = append(dec.HopFields, hf)
				}
			}
			// Take FirstHopPerSeg from the decoder rather than hand-writing it here.
			buff := make([]byte, dec.Len())
			require.NoError(t, dec.SerializeTo(buff))
			require.NoError(t, dec.DecodeFromBytes(buff))

			gotHops, gotIndices, err := path.HummDataplaneToBaseHops(dec)
			if tc.wantErr {
				require.Error(t, err)
				// The reservation setup must refuse the path for the same reason.
				r := &path.Reservation{}
				require.Error(t, r.SetupWithHummDecoded(dec, addr.MustParseIA("1-ff00:0:2"),
					make(path.FlyoverSequence, len(dec.HopFields))))
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantHops, gotHops)
			require.Equal(t, tc.wantIndices, gotIndices)
			// Each logical hop is carried by exactly one hop field of the path.
			require.Len(t, gotIndices, len(gotHops))
			for _, i := range gotIndices {
				require.Less(t, int(i), len(dec.HopFields), "index past the end of the path")
			}

			// The reservation setup must agree on how many logical hops the path has.
			// A sequence with no flyovers is enough: the setup still has to settle the count
			// before it can decide there is nothing to assign.
			r := &path.Reservation{}
			require.NoError(t, r.SetupWithHummDecoded(dec, addr.MustParseIA("1-ff00:0:2"),
				make(path.FlyoverSequence, len(tc.wantHops))))
			require.Len(t, r.Hops, len(dec.HopFields))

			// A sequence of the wrong length must be rejected, so that a regression in the hop
			// accounting cannot pass this test by making both sides wrong together.
			r = &path.Reservation{}
			require.Error(t, r.SetupWithHummDecoded(dec, addr.MustParseIA("1-ff00:0:2"),
				make(path.FlyoverSequence, len(tc.wantHops)+1)))
		})
	}
}

// createHummingbirdPath creates a valid Hummingbird path between 111 and 112 from the tiny topo.
// This path contains no flyovers.
func createHummingbirdPath(iniTime time.Time) *dphumm.Decoded {
	const hfValidity = 8

	dec := &dphumm.Decoded{
		Base: dphumm.Base{
			PathMeta: dphumm.MetaHdr{
				SegLen: [3]uint8{6, 6, 0},
			},
			NumINF:   2,
			NumLines: 4 * 3, // 4 non-flyover hops.
		},
		InfoFields: []dppath.InfoField{
			// up
			{
				ConsDir:   false,
				Timestamp: util.TimeToSecs(iniTime),
			},
			// down
			{
				ConsDir:   true,
				Timestamp: util.TimeToSecs(iniTime),
			},
		},
		FirstHopPerSeg: [2]uint8{2, 4}, // Second segment starts at index 2. There's no third one.
		HopFields: []dphumm.FlyoverHopField{
			// 111: 0->41 up
			{
				HopField: dppath.HopField{
					ConsIngress: 41,
					ConsEgress:  0,
					ExpTime:     hfValidity,
				},
			},
			// 110: 1->0  up
			{
				HopField: dppath.HopField{
					ConsIngress: 0,
					ConsEgress:  1,
					ExpTime:     hfValidity,
				},
			},
			// 110: 0->2  down
			{
				HopField: dppath.HopField{
					ConsIngress: 0,
					ConsEgress:  2,
					ExpTime:     hfValidity,
				},
			},
			// 112: 1->0  down
			{
				HopField: dppath.HopField{
					ConsIngress: 1,
					ConsEgress:  0,
					ExpTime:     hfValidity,
				},
			},
		},
	}
	return dec
}

// createScionPath creates a mock scion path between the tiny topology's 111 AS and 112 one.
func createScionPath(iniTime time.Time) *scion.Decoded {
	const hfValidity = 8

	dec := &scion.Decoded{
		Base: scion.Base{
			PathMeta: scion.MetaHdr{
				SegLen: [3]uint8{2, 2, 0},
			},
			NumINF:  2,
			NumHops: 4,
		},
		InfoFields: []dppath.InfoField{
			// up
			{
				ConsDir:   false,
				Timestamp: util.TimeToSecs(iniTime),
			},
			// down
			{
				ConsDir:   true,
				Timestamp: util.TimeToSecs(iniTime),
			},
		},
		HopFields: []dppath.HopField{
			// 111: 0->41 up
			{
				ConsIngress: 41,
				ConsEgress:  0,
				ExpTime:     hfValidity,
			},
			// 110: 1->0  up
			{
				ConsIngress: 0,
				ConsEgress:  1,
				ExpTime:     hfValidity,
			},
			// 110: 0->2  down
			{
				ConsIngress: 0,
				ConsEgress:  2,
				ExpTime:     hfValidity,
			},
			// 112: 1->0  down
			{
				ConsIngress: 1,
				ConsEgress:  0,
				ExpTime:     hfValidity,
			},
		},
	}
	return dec
}

func createFlyoverSequence(t *testing.T, startTime uint32) path.FlyoverSequence {
	return path.FlyoverSequence{
		// 111: 0 -> 41
		&path.Hop{
			BaseHop: path.BaseHop{
				IA:      addr.MustParseIA("1-ff00:0:111"),
				Ingress: 0,
				Egress:  41,
			},
			Flyover: createFlyover(t, startTime),
		},
		// 110: 1 ->  2
		&path.Hop{
			BaseHop: path.BaseHop{
				IA:      addr.MustParseIA("1-ff00:0:110"),
				Ingress: 1,
				Egress:  2,
			},
			Flyover: createFlyover(t, startTime),
		},
		// 112: 1 ->  0
		&path.Hop{
			BaseHop: path.BaseHop{
				IA:      addr.MustParseIA("1-ff00:0:112"),
				Ingress: 1,
				Egress:  0,
			},
			Flyover: createFlyover(t, startTime),
		},
	}
}

// createFlyover mocks the redemption of a flyover for a given AS, ingress, and egress interfaces.
// The real function will require a daemon.Connector to find a path to the given AS, or the path
// to the given AS.
func createFlyover(t *testing.T, startTime uint32) *path.FlyoverData {
	t.Helper()
	return &path.FlyoverData{
		ResID:     1,
		StartTime: startTime,
		Duration:  10,
		Bw:        64,
		Ak:        [16]byte{1, 2, 3, 4},
	}
}

func checkHop(t *testing.T, hop *path.Hop, ia string, in uint16, eg uint16, expectHop bool) {
	if expectHop {
		require.NotNil(t, hop)
		require.NotNil(t, hop.Flyover)
	} else {
		require.Nil(t, hop)
		return
	}
	if ia != "" {
		require.Equal(t, addr.MustParseIA(ia), hop.IA)
	}
	require.Equal(t, in, hop.Ingress)
	require.Equal(t, eg, hop.Egress)
}

func createHopWithFlyover(t *testing.T) *path.Hop {
	return &path.Hop{
		BaseHop: path.BaseHop{
			IA:      addr.MustParseIA("1025-ff00:dead:abcd"),
			Ingress: 0xdead,
			Egress:  0xbeef,
		},
		Flyover: &path.FlyoverData{
			ResID:     123456,
			Ak:        mustDecode16bytes(t, "0123456789abcdef0123456789abcdef"),
			Bw:        1023,
			StartTime: 4_123_456,
			Duration:  0xfe,
		},
	}
}

func mustDecode16bytes(t *testing.T, s string) [16]byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	require.Len(t, b, 16)
	return [16]byte(b)
}

// partialSelections lists, for the 3 base hops of the 111 -> 112 tiny-topology path, which base
// hops carry a flyover. Base hop 1 (110) is the crossover AS, mapped to hop field index 1.
var partialSelections = []struct {
	name string
	sel  [3]bool
}{
	{"none", [3]bool{false, false, false}},
	{"first", [3]bool{true, false, false}},
	{"middle", [3]bool{false, true, false}},
	{"last", [3]bool{false, false, true}},
	{"first+middle", [3]bool{true, true, false}},
	{"middle+last", [3]bool{false, true, true}},
	{"first+last", [3]bool{true, false, true}},
	{"all", [3]bool{true, true, true}},
}

// selectedSequence returns the full-length sequence with nil holes for unselected base hops.
func selectedSequence(
	t *testing.T,
	full path.FlyoverSequence,
	sel [3]bool,
) path.FlyoverSequence {
	t.Helper()
	require.Len(t, full, 3)
	seq := make(path.FlyoverSequence, len(full))
	for i := range full {
		if sel[i] {
			seq[i] = full[i]
		}
	}
	return seq
}

// TestPartialReservation builds reservations over the 3-AS path with holes in the flyover
// sequence, and checks the resulting hops, line counts, and the serialization round trip.
func TestPartialReservation(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)
	dstIA := addr.MustParseIA("1-ff00:0:112")
	// Hop field indices of the base hops (the crossover hop field, index 2, has no base hop).
	hfIdx := [3]int{0, 1, 3}

	for _, tc := range partialSelections {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scionDec := createScionPath(referenceTime)
			raw := make([]byte, scionDec.Len())
			require.NoError(t, scionDec.SerializeTo(raw))
			seq := selectedSequence(t, createFlyoverSequence(t, referenceEpochTime), tc.sel)

			r, err := path.NewReservation(
				path.WithDataplanePath(path.SCION{Raw: raw}, dstIA, seq),
			)
			require.NoError(t, err)
			require.Len(t, r.Hops, 4)
			require.Nil(t, r.Hops[2], "crossover hop field never has a hop")

			count := 0
			for i, selected := range tc.sel {
				h := r.Hops[hfIdx[i]]
				hf := r.Dec.HopFields[hfIdx[i]]
				if selected {
					count++
					require.NotNil(t, h, "base hop %d", i)
					require.NotNil(t, h.Flyover, "base hop %d", i)
					require.True(t, hf.Flyover, "base hop %d", i)
					require.NotNil(t, (*r.AesBlocks())[hfIdx[i]], "base hop %d", i)
					require.Equal(t, seq[i].Ingress, h.Ingress)
					require.Equal(t, seq[i].Egress, h.Egress)
				} else {
					require.Nil(t, h, "base hop %d", i)
					require.False(t, hf.Flyover, "base hop %d", i)
					require.Nil(t, (*r.AesBlocks())[hfIdx[i]], "base hop %d", i)
				}
			}
			require.False(t, r.Dec.HopFields[2].Flyover)

			// Line counts: 3 lines per hop field, 5 per flyover hop field.
			require.Equal(t, 12+2*count, int(r.Dec.NumLines))
			wantSeg0 := 6
			if tc.sel[0] {
				wantSeg0 += 2
			}
			if tc.sel[1] {
				wantSeg0 += 2
			}
			wantSeg1 := 6
			if tc.sel[2] {
				wantSeg1 += 2
			}
			require.Equal(t, wantSeg0, int(r.Dec.PathMeta.SegLen[0]))
			require.Equal(t, wantSeg1, int(r.Dec.PathMeta.SegLen[1]))
			require.Equal(t, 0, int(r.Dec.PathMeta.SegLen[2]))
			require.Equal(t, int(r.Dec.NumLines),
				int(r.Dec.PathMeta.SegLen[0])+int(r.Dec.PathMeta.SegLen[1]))
			for i := range 3 {
				require.Equal(t, r.Dec.NumberOfHFsInSegment(i) > 0, i < 2)
			}
			// The decoded path must serialize to its declared length and decode back.
			dpRaw := make([]byte, r.Dec.Len())
			require.NoError(t, r.Dec.SerializeTo(dpRaw))
			var dec2 dphumm.Decoded
			require.NoError(t, dec2.DecodeFromBytes(dpRaw))
			for i, hf := range dec2.HopFields {
				require.Equal(t, r.Dec.HopFields[i].Flyover, hf.Flyover, "hf %d", i)
			}

			// Serialize / deserialize round trip of the reservation state.
			buff := make([]byte, r.SerializedLen())
			require.NoError(t, r.Serialize(buff))
			var r2 path.Reservation
			require.NoError(t, r2.Deserialize(buff))
			require.Equal(t, r.Hops, r2.Hops)
			require.Equal(t, r.GetScionMACs(), r2.GetScionMACs())
			for i := range r.Hops {
				require.Equal(t, r.Hops[i] == nil, r2.Hops[i] == nil, "hop %d", i)
				if r.Hops[i] != nil {
					require.Equal(t, r.Hops[i].Flyover != nil, r2.Hops[i].Flyover != nil)
				}
			}
		})
	}
}

// buildForwardAndReverse builds a forward reservation with the given selection, and the serialized
// reverse reservation (built by the "client" over the reversed SCION path) with revSel, given
// in reverse path order (index 0 is 112).
func buildForwardAndReverse(
	t *testing.T,
	fwdSel, revSel [3]bool,
) (snet.RawPath, []byte) {
	t.Helper()
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)

	// Forward reservation, whose dataplane path is what the server receives.
	scionDec := createScionPath(referenceTime)
	raw := make([]byte, scionDec.Len())
	require.NoError(t, scionDec.SerializeTo(raw))
	fwd, err := path.NewReservation(path.WithDataplanePath(path.SCION{Raw: raw},
		addr.MustParseIA("1-ff00:0:112"),
		selectedSequence(t, createFlyoverSequence(t, referenceEpochTime), fwdSel)))
	require.NoError(t, err)
	fwdRaw := make([]byte, fwd.Dec.Len())
	require.NoError(t, fwd.Dec.SerializeTo(fwdRaw))
	carrier := snet.RawPath{PathType: dphumm.PathType, Raw: fwdRaw}

	// Reverse reservation, built over the reversed SCION path.
	revDec := createScionPath(referenceTime)
	_, err = revDec.Reverse()
	require.NoError(t, err)
	revRaw := make([]byte, revDec.Len())
	require.NoError(t, revDec.SerializeTo(revRaw))
	revSeq := path.FlyoverSequence{
		{BaseHop: path.BaseHop{IA: addr.MustParseIA("1-ff00:0:112"), Ingress: 0, Egress: 1}},
		{BaseHop: path.BaseHop{IA: addr.MustParseIA("1-ff00:0:110"), Ingress: 2, Egress: 1}},
		{BaseHop: path.BaseHop{IA: addr.MustParseIA("1-ff00:0:111"), Ingress: 41, Egress: 0}},
	}
	for i := range revSeq {
		if revSel[i] {
			revSeq[i].Flyover = createFlyover(t, referenceEpochTime)
		} else if !revSel[i] {
			revSeq[i] = nil
		}
	}
	rev, err := path.NewReservation(path.WithDataplanePath(path.SCION{Raw: revRaw},
		addr.MustParseIA("1-ff00:0:111"), revSeq))
	require.NoError(t, err)
	buff := make([]byte, rev.SerializedLen())
	require.NoError(t, rev.Serialize(buff))
	return carrier, buff
}

// TestReverseFromBidirectionalPartial checks that a reverse reservation that mirrors the forward
// selection yields a usable reservation, and that a reverse reservation that does not provide a
// flyover for a forward flyover hop field is rejected.
func TestReverseFromBidirectionalPartial(t *testing.T) {
	srcIA := addr.MustParseIA("1-ff00:0:111")
	mirror := func(s [3]bool) [3]bool { return [3]bool{s[2], s[1], s[0]} }

	for _, tc := range partialSelections {
		t.Run("mirrored_"+tc.name, func(t *testing.T) {
			t.Parallel()
			carrier, buff := buildForwardAndReverse(t, tc.sel, mirror(tc.sel))
			r, err := path.NewReservation(path.WithReverseFromBidirectional(buff, carrier, srcIA))
			require.NoError(t, err)
			require.Equal(t, srcIA, r.DstIA)
			require.Len(t, r.Hops, 4)
			count := 0
			for i, hf := range r.Dec.HopFields {
				hasFlyover := r.Hops[i] != nil && r.Hops[i].Flyover != nil
				require.Equal(t, hasFlyover, hf.Flyover, "hf %d", i)
				require.Equal(t, hasFlyover, (*r.AesBlocks())[i] != nil, "hf %d", i)
				if hasFlyover {
					count++
				}
			}
			require.Equal(t, 12+2*count, int(r.Dec.NumLines))
			require.Equal(t, int(r.Dec.NumLines),
				int(r.Dec.PathMeta.SegLen[0])+int(r.Dec.PathMeta.SegLen[1]))
			// Must be able to derive the dataplane path (computes flyover MACs).
			s := &slayers.SCION{}
			require.NoError(t, r.SetPath(s))
		})
	}

	// Non mirrored selections are still usable: WithReverseFromBidirectional reverses the
	// received path, which drops all of its flyover hop fields (Decoded.Reverse calls
	// RemoveFlyovers), so no flyover hop field can be left without a bound Ak. The reverse
	// path carries flyovers exactly where the reverse reservation has them.
	for _, tc := range []struct {
		name     string
		fwd, rev [3]bool
	}{
		{"fwd_first_rev_none", [3]bool{true, false, false}, [3]bool{}},
		{"fwd_all_rev_subset", [3]bool{true, true, true}, [3]bool{true, false, true}},
		{"fwd_first_rev_same_order", [3]bool{true, false, false}, [3]bool{true, false, false}},
		{"fwd_none_rev_all", [3]bool{}, [3]bool{true, true, true}},
	} {
		t.Run("not_mirrored_"+tc.name, func(t *testing.T) {
			t.Parallel()
			carrier, buff := buildForwardAndReverse(t, tc.fwd, tc.rev)
			r, err := path.NewReservation(path.WithReverseFromBidirectional(buff, carrier, srcIA))
			require.NoError(t, err)
			count := 0
			for i, hf := range r.Dec.HopFields {
				hasFlyover := r.Hops[i] != nil && r.Hops[i].Flyover != nil
				require.Equal(t, hasFlyover, hf.Flyover, "hf %d", i)
				require.Equal(t, hasFlyover, (*r.AesBlocks())[i] != nil, "hf %d", i)
				if hasFlyover {
					count++
				}
			}
			require.Equal(t, 12+2*count, int(r.Dec.NumLines))
			require.NoError(t, r.SetPath(&slayers.SCION{}))
		})
	}
}
