// Copyright 2023 ETH Zurich
// Copyright 2025 SCION Association
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

package udpip

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/prometheus/client_golang/prometheus"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/private/util"
	"github.com/scionproto/scion/pkg/scrypto"
	"github.com/scionproto/scion/pkg/slayers"
	"github.com/scionproto/scion/pkg/slayers/path"
	"github.com/scionproto/scion/pkg/slayers/path/scion"
	"github.com/scionproto/scion/router"
)

var (
	testKey = []byte("testkey_xxxxxxxx")
)

func TestReceiveOverflowRecorder(t *testing.T) {
	metric := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_router_underlay_receive_overflow_pkts_total",
	}, []string{"local", "remote"})
	metrics := &router.Metrics{UnderlayReceiveOverflowPackets: metric}
	local := netip.MustParseAddrPort("127.0.0.1:10000")

	record := newReceiveOverflowRecorder(metrics, local, netip.AddrPort{})
	require.NotNil(t, record)
	record(7)

	require.Equal(t, float64(7),
		promtest.ToFloat64(metric.WithLabelValues(local.String(), "unconnected")))
}

type classifiedWriteError struct {
	temporary bool
	timeout   bool
}

func (e classifiedWriteError) Error() string   { return "write error" }
func (e classifiedWriteError) Temporary() bool { return e.temporary }
func (e classifiedWriteError) Timeout() bool   { return e.timeout }

func TestRetryableWriteError(t *testing.T) {
	testCases := map[string]struct {
		err               error
		expectedRetryable bool
		expectedDelay     time.Duration
	}{
		"no error": {
			expectedRetryable: true,
		},
		"interrupted": {
			err:               syscall.EINTR,
			expectedRetryable: true,
		},
		"would block": {
			err:               fmt.Errorf("wrapped: %w", syscall.EAGAIN),
			expectedRetryable: true,
			expectedDelay:     temporaryWriteErrorDelay,
		},
		"no buffer space": {
			err:               syscall.ENOBUFS,
			expectedRetryable: true,
			expectedDelay:     temporaryWriteErrorDelay,
		},
		"no memory": {
			err:               syscall.ENOMEM,
			expectedRetryable: true,
			expectedDelay:     temporaryWriteErrorDelay,
		},
		"temporary": {
			err: classifiedWriteError{temporary: true},
		},
		"timeout": {
			err: classifiedWriteError{temporary: true, timeout: true},
		},
		"permanent": {
			err: fmt.Errorf("permanent write error"),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			retryable, delay := retryableWriteError(tc.err)
			assert.Equal(t, tc.expectedRetryable, retryable)
			assert.Equal(t, tc.expectedDelay, delay)
		})
	}
}

func computeMAC(t *testing.T, key []byte, info path.InfoField, hf path.HopField) [path.MacLen]byte {
	mac, err := scrypto.InitMac(key)
	require.NoError(t, err)
	return path.MAC(mac, info, hf, nil)
}

// Prepares a message that is arriving at its last hop, incoming through interface 1.
func prepBaseMsg(t *testing.T, flowId uint32) *slayers.SCION {
	spkt := &slayers.SCION{
		Version:      0,
		TrafficClass: 0xb8,
		FlowID:       flowId,
		NextHdr:      slayers.L4UDP,
		PathType:     scion.PathType,
		DstIA:        addr.MustParseIA("1-ff00:0:110"),
		SrcIA:        addr.MustParseIA("1-ff00:0:111"),
		Path:         &scion.Raw{},
		PayloadLen:   18,
	}

	dpath := &scion.Decoded{
		Base: scion.Base{
			PathMeta: scion.MetaHdr{
				CurrHF: 2,
				SegLen: [3]uint8{3, 0, 0},
			},
			NumINF:  1,
			NumHops: 3,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: true, Timestamp: util.TimeToSecs(time.Now())},
		},

		HopFields: []path.HopField{
			{ConsIngress: 41, ConsEgress: 40},
			{ConsIngress: 31, ConsEgress: 30},
			{ConsIngress: 1, ConsEgress: 0},
		},
	}
	dpath.HopFields[2].Mac = computeMAC(t, testKey, dpath.InfoFields[0], dpath.HopFields[2])
	spkt.Path = dpath
	return spkt
}

func TestComputeProcId(t *testing.T) {
	randomValueBytes := []byte{1, 2, 3, 4}
	numProcs := 10000

	// ComputeProcID expects the per-receiver random number to be pre-hashed into the seed that we
	// pass.
	hashSeed := fnv1aOffset32
	for _, c := range randomValueBytes {
		hashSeed = hashFNV1a(hashSeed, c)
	}

	// this function returns the procID as we expect it by using the  slayers.SCION serialization
	// implementation.
	referenceHash := func(s *slayers.SCION) uint32 {
		flowBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(flowBuf, s.FlowID)
		flowBuf[0] &= 0xF
		tmpBuffer := make([]byte, 100)
		hasher := fnv.New32a()
		hasher.Write(randomValueBytes)
		hasher.Write(flowBuf[1:4])
		if err := s.SerializeAddrHdr(tmpBuffer); err != nil {
			panic(err)
		}
		hasher.Write(tmpBuffer[:s.AddrHdrLen()])
		return hasher.Sum32() % uint32(numProcs)
	}

	// this helper returns the procID as the router actually makes it by using the extraction
	// from dataplane.computeProcID() along with hashFNV1a() for the seed.
	computeProcIDHelper := func(payload []byte, s *slayers.SCION) (uint32, bool) {
		buffer := gopacket.NewSerializeBuffer()
		err := gopacket.SerializeLayers(buffer,
			gopacket.SerializeOptions{FixLengths: true},
			s, gopacket.Payload(payload))
		require.NoError(t, err)
		raw := buffer.Bytes()

		return computeProcID(raw, numProcs, hashSeed)
	}
	type ret struct {
		payload []byte
		s       *slayers.SCION
	}
	// Each testcase has a function that returns a set of ret structs where
	// all rets of that set are expected to return the same hash value
	testCases := map[string]func(t *testing.T) []ret{
		"basic": func(t *testing.T) []ret {
			payload := []byte("x")
			return []ret{
				{
					payload: payload,
					s:       prepBaseMsg(t, (1<<20)-1),
				},
			}
		},
		"different payload does not affect hashing": func(t *testing.T) []ret {
			rets := make([]ret, 10)
			for i := 0; i < 10; i++ {
				rets[i].payload = make([]byte, 100)
				_, err := rand.Read(rets[i].payload)
				spkt := prepBaseMsg(t, 1)
				assert.NoError(t, err)
				rets[i].s = spkt
			}
			return rets
		},
		"flowID is extracted correctly independing of trafficId": func(t *testing.T) []ret {
			rets := make([]ret, 16)
			payload := make([]byte, 100)
			for i := 0; i < 16; i++ {
				rets[i].payload = payload
				spkt := prepBaseMsg(t, 1)
				spkt.TrafficClass = uint8(i)
				rets[i].s = spkt
			}
			return rets
		},
		"ipv4 to ipv4": func(t *testing.T) []ret {
			payload := make([]byte, 100)
			spkt := prepBaseMsg(t, 1)
			assert.NoError(t,
				spkt.SetDstAddr(addr.HostIP(netip.AddrFrom4([4]byte{10, 0, 200, 200}))))
			assert.NoError(t,
				spkt.SetSrcAddr(addr.HostIP(netip.AddrFrom4([4]byte{10, 0, 200, 200}))))
			assert.Equal(t, slayers.T4Ip, spkt.DstAddrType)
			assert.Equal(t, slayers.T4Ip, spkt.SrcAddrType)
			return []ret{
				{
					payload: payload,
					s:       spkt,
				},
			}
		},
		"ipv6 to ipv4": func(t *testing.T) []ret {
			payload := make([]byte, 100)
			spkt := prepBaseMsg(t, 1)
			assert.NoError(t,
				spkt.SetDstAddr(addr.HostIP(netip.AddrFrom4([4]byte{10, 0, 200, 200}))))
			assert.NoError(t, spkt.SetSrcAddr(addr.HostIP(netip.MustParseAddr("2001:db8::68"))))
			assert.Equal(t, slayers.T4Ip, spkt.DstAddrType)
			assert.Equal(t, slayers.T16Ip, spkt.SrcAddrType)
			return []ret{
				{
					payload: payload,
					s:       spkt,
				},
			}
		},
		"svc to ipv4": func(t *testing.T) []ret {
			payload := make([]byte, 100)
			spkt := prepBaseMsg(t, 1)
			spkt.DstAddrType = slayers.T4Ip
			assert.NoError(t,
				spkt.SetDstAddr(addr.HostIP(netip.AddrFrom4([4]byte{10, 0, 200, 200}))))
			assert.NoError(t, spkt.SetSrcAddr(addr.HostSVC(addr.SvcWildcard)))
			assert.Equal(t, slayers.T4Ip, spkt.DstAddrType)
			assert.Equal(t, slayers.T4Svc, spkt.SrcAddrType)
			return []ret{
				{
					payload: payload,
					s:       spkt,
				},
			}
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			rets := tc(t)
			if len(rets) == 0 {
				return
			}
			expected := referenceHash(rets[0].s)
			for _, r := range rets {
				actual, ok := computeProcIDHelper(r.payload, r.s)
				assert.True(t, ok)
				assert.Equal(t, expected, actual)
			}
		})
	}
}

func TestComputeProcIdErrorCases(t *testing.T) {
	type test struct {
		data            []byte
		expectedSuccess bool
	}
	testCases := map[string]test{
		"packet shorter than common header len": {
			data: []byte{
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0,
			},
			expectedSuccess: false,
		},
		"packet len = CmnHdrLen + addrHdrLen": {
			data: []byte{
				0, 0, 0, 0, 0xfe, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0,
			},
			expectedSuccess: true,
		},
		"packet len < CmnHdrLen + addrHdrLen": {
			data: []byte{
				0, 0, 0, 0, 0xfe, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0,
			},
			expectedSuccess: false,
		},
		"packet len = CmnHdrLen + addrHdrLen (16IP)": {
			data: []byte{
				0, 0, 0, 0, 0xfe, 0, 0, 0,
				0, 0x33, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0,
			},
			expectedSuccess: true,
		},
		"packet len < CmnHdrLen + addrHdrLen (16IP)": {
			data: []byte{
				0, 0, 0, 0, 0xfe, 0, 0, 0,
				0, 0x33, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0,
			},
			expectedSuccess: false,
		},
		"Simple STUN packet": {
			data: []byte{
				0x00, 0x00, 0x00, 0x00, 0x21, 0x12, 0xa4, 0x42,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00,
			},
			expectedSuccess: false,
		},
		"'foo' STUN packet": {
			data: []byte{
				0x00, 0x00, 0x00, 0x00, 0x21, 0x12, 0xa4, 0x42,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x66, 0x6f, 0x6f,
			},
			expectedSuccess: false,
		},
		"Non-zero first byte STUN packet": {
			data: []byte{
				0x20, 0x00, 0x00, 0x00, 0x21, 0x12, 0xa4, 0x42,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00,
			},
			expectedSuccess: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			randomValue := uint32(1234) // not a proper hash seed, but hash result is irrelevant.
			_, ok := computeProcID(tc.data, 10000, randomValue)
			assert.Equal(t, tc.expectedSuccess, ok)
		})
	}
}

// BenchmarkClassOfSize measures the overhead of calling ClassOfSize per packet.
// Since our udpConnection.send function calls it for every packet, this overhead should be small.
func BenchmarkClassOfSize(b *testing.B) {
	sizes := []int{1, 50, 100, 200, 2000, 9000}

	var sink int
	for _, sz := range sizes {
		sz := sz
		b.Run(fmt.Sprintf("size=%d", sz), func(b *testing.B) {
			var sc int
			for i := 0; i < b.N; i++ {
				sc = int(router.ClassOfSize(sz))
			}
			sink = sc
		})
	}
	_ = sink
}
