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
	"context"
	"crypto/cipher"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/daemon"
	"github.com/scionproto/scion/pkg/daemon/types"
	"github.com/scionproto/scion/pkg/private/util"
	"github.com/scionproto/scion/pkg/segment/iface"
	dppath "github.com/scionproto/scion/pkg/slayers/path"
	dphumm "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	"github.com/scionproto/scion/pkg/slayers/path/scion"
	"github.com/scionproto/scion/pkg/snet"
	"github.com/scionproto/scion/pkg/snet/path"
	"github.com/stretchr/testify/require"
)

func TestNewWithNow(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	r, err := path.NewReservation(
		path.WithNow(func() time.Time {
			return util.SecsToTime(referenceEpochTime)
		}),
		path.WithDstIA(addr.MustParseIA("1-ff00:0:112")),
	)
	require.NoError(t, err)
	require.Equal(t, referenceEpochTime, util.TimeToSecs(r.Now()))
}

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
		Now:   func() time.Time { return referenceTime },
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

func TestWithScionPath(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)

	p := createSnetScionPath(t, referenceTime)
	flyoverMap := createFlyovers(t, referenceEpochTime)
	require.Len(t, flyoverMap, 3) // Original flyovers are three.
	r, err := path.NewReservation(path.WithScionPath(p, flyoverMap))
	require.NoError(t, err)
	require.NotNil(t, r)
	require.Equal(t, addr.MustParseIA("1-ff00:0:112"), r.DstIA)
	require.Len(t, flyoverMap, 0) // All flyovers were used.

	// The path contains two segments, i.e. one xover hop.
	// There should only be three flyovers, check it.
	// The hops are:			With Flyover
	// - [0] 111[0] -> 111[41]		*
	// - [1] 110[1] -> 110[0]		*
	// - [2] 110[0] -> 110[2]
	// - [3] 112[1] -> 112[0]		*
	require.Len(t, r.Hops, 4)
	checkHop(t, r.Hops[0], "1-ff00:0:111", 0, 41, true)
	checkHop(t, r.Hops[1], "1-ff00:0:110", 1, 2, true)
	checkHop(t, r.Hops[2], "1-ff00:0:110", 999, 999, false) // ingress and egress don't matter here
	checkHop(t, r.Hops[3], "1-ff00:0:112", 1, 0, true)
}

func TestWithScionDataplane(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)

	scionDec := createScionPath(referenceTime)
	scionRaw, err := scionDec.ToRaw()
	require.NoError(t, err)

	seq := createFlyoverSequence(t, referenceEpochTime)

	r, err := path.NewReservation(
		path.WithScionDataplane(scionRaw, addr.MustParseIA("1-ff00:0:112"), seq),
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

func TestWithHummDataplane(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)

	dec := createHummingbirdPath(referenceTime)
	seq := createFlyoverSequence(t, referenceEpochTime)

	r, err := path.NewReservation(
		path.WithDstIA(addr.MustParseIA("1-ff00:0:112")),
		path.WithHummDataplane(dec, seq),
	)
	require.NoError(t, err)
	require.Same(t, dec, r.Dec)
	require.Len(t, r.Hops, 4)

	checkHop(t, r.Hops[0], "", 0, 41, true)
	checkHop(t, r.Hops[1], "", 1, 2, true)
	checkHop(t, r.Hops[2], "", 999, 999, false)
	checkHop(t, r.Hops[3], "", 1, 0, true)
}

func TestWithHummDataplaneRequiresDstIA(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)

	_, err := path.NewReservation(path.WithHummDataplane(
		createHummingbirdPath(referenceTime),
		createFlyoverSequence(t, referenceEpochTime),
	))
	require.ErrorContains(t, err, "unset destination IA")
}

// TestSetScionPathClonesMacFields checks that Reservation.setScionPath clones the values of
// the MAC fields of the SCION path.
func TestSetScionPathClonesMacFields(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)

	// Create a path and remember one of the MAC fields.
	p := createSnetScionPath(t, referenceTime)
	scionPath := p.DataplanePath.(path.SCION)
	require.NotNil(t, scionPath)
	scionDec := &scion.Decoded{}
	err := scionDec.DecodeFromBytes(scionPath.Raw)
	require.NoError(t, err)
	originalMac := scionDec.HopFields[0].Mac // Copy the array (clone).

	r := &path.Reservation{}
	err = r.SetScionPath(scionDec)
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

func TestFlyoversForPath(t *testing.T) {
	const referenceEpochTime uint32 = 123456
	referenceTime := util.SecsToTime(referenceEpochTime)

	p := createSnetScionPath(t, referenceTime)
	require.NotNil(t, p)

	flyovers, err := getFlyoversForPath(p, referenceEpochTime)
	require.NoError(t, err)
	require.Len(t, flyovers, 3)

	expectedFlyovers := createFlyovers(t, referenceEpochTime)
	require.EqualValues(t, expectedFlyovers, flyovers)
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

func createSnetScionPath(t *testing.T, iniTime time.Time) path.Path {
	scion, err := path.NewSCIONFromDecoded(*createScionPath(iniTime))
	require.NoError(t, err)
	as111 := addr.MustParseIA("1-ff00:0:111")
	as110 := addr.MustParseIA("1-ff00:0:110")
	as112 := addr.MustParseIA("1-ff00:0:112")
	return path.Path{
		Src:           addr.MustParseIA("1-ff00:0:111"),
		Dst:           addr.MustParseIA("1-ff00:0:112"),
		DataplanePath: scion,
		Meta: snet.PathMetadata{
			MTU:    1500,
			Expiry: iniTime.Add(time.Hour),
			Interfaces: []snet.PathInterface{
				// First segment:
				{
					IA: as111,
					ID: 41,
				},
				{
					IA: as110,
					ID: 1,
				},
				// Second segment:
				{
					IA: as110,
					ID: 2,
				},
				{
					IA: as112,
					ID: 1,
				},
			},
		},
	}
}

// createFlyovers creates all the flyovers for the path 111->112 of the tiny topology.
func createFlyovers(t *testing.T, startTime uint32) path.FlyoverMap {
	return path.FlyoversToMap(createFlyoverSequence(t, startTime))
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

func getPaths(srcIA addr.IA, dstIA addr.IA) ([]snet.Path, error) {
	ctx, cancelF := context.WithTimeout(context.Background(), time.Second)
	defer cancelF()
	daemonLocation := ""
	configDir := ""
	sd, err := daemon.NewAutoConnector(ctx,
		daemon.WithDaemon(daemonLocation),
		daemon.WithConfigDir(configDir),
	)
	if err != nil {
		return nil, err
	}
	paths, err := sd.Paths(ctx, dstIA, srcIA, types.PathReqFlags{})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// getPathsToTransitASes returns one path to each on-path AS of the passes snet.Path.
func getPathsToTransitASes(t *testing.T, p snet.Path) []snet.Path {
	t.Helper()
	intfs := p.Metadata().Interfaces
	require.NotEmpty(t, intfs)
	// Find all on-path ASes.
	ases := make([]addr.IA, 0, len(intfs)/2+1)
	ases = append(ases, intfs[0].IA)
	for i := 1; i < len(intfs); i += 2 {
		ases = append(ases, intfs[i].IA)
	}

	// For each AS, find a path to it and store it.
	paths := make([]snet.Path, len(ases))
	errs := make([]error, len(ases))
	resolved := make([][]snet.Path, len(ases))
	srcIA := p.Source()
	var wg sync.WaitGroup
	wg.Add(len(ases))
	for i, as := range ases {
		go func(i int, as addr.IA) {
			defer wg.Done()
			asPaths, err := getPaths(srcIA, as)
			errs[i] = err
			resolved[i] = asPaths
		}(i, as)
	}
	wg.Wait()

	for i := range ases {
		require.NoError(t, errs[i])
		require.NotEmpty(t, resolved[i])
		paths[i] = resolved[i][0]
	}

	return paths
}

// getFlyoversForPath returns a FlyoverMap with all returned flyovers for the given path.
// Compatibility wrapper: keeps the old mocked behavior.
// deleteme! TODO remove this temporary function and modify tests.
func getFlyoversForPath(p snet.Path, startTime uint32) (path.FlyoverMap, error) {
	interfaces := p.Metadata().Interfaces
	if len(interfaces) == 0 {
		return path.FlyoverMap{}, nil
	}
	baseHops := path.InterfacesToBaseHops(interfaces)
	return getFlyoversForHops(baseHops, startTime)
}

func getFlyoversForHops(baseHops []path.BaseHop, startTime uint32) (path.FlyoverMap, error) {
	// For each found triplet <AS,ingress,egress> call redeemFlyover and store the result.
	redeemed := make([]*path.Hop, len(baseHops))

	for i := range baseHops {
		redeemed[i] = redeemFlyover(baseHops[i], startTime)
	}

	return path.FlyoversToMap(redeemed), nil
}

// redeemFlyover mocks the redemption of a flyover for a given AS, ingress, and egress interfaces.
// The real function will require a daemon.Connector to find a path to the given AS, or the path
// to the given AS.
func redeemFlyover(baseHop path.BaseHop, startTime uint32) *path.Hop {
	return &path.Hop{
		BaseHop: baseHop,
		Flyover: &path.FlyoverData{
			ResID:     1,
			StartTime: startTime,
			Duration:  10,
			Bw:        64,
			Ak:        [16]byte{1, 2, 3, 4},
		},
	}
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
