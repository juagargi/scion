// Copyright 2020 ETH Zurich
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

package qos

import (
	"io/ioutil"
	"net"
	"testing"

	"github.com/inconshreveable/log15"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/go/border/qos/conf"
	"github.com/scionproto/scion/go/border/rpkt"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/common"
	"github.com/scionproto/scion/go/lib/l4"
	"github.com/scionproto/scion/go/lib/spkt"
)

var blocks chan bool

func genRouterPacket(sourceIA string, destinationIA string, L4Type, intf int) *rpkt.RtrPkt {

	srcIA, _ := addr.IAFromString(sourceIA)
	dstIA, _ := addr.IAFromString(destinationIA)

	pkt := spkt.ScnPkt{

		SrcIA:   srcIA,
		DstIA:   dstIA,
		SrcHost: addr.HostFromIP(net.IP{127, 0, 0, 1}),
		DstHost: addr.HostFromIP(net.IP{127, 0, 0, 1}),
		L4: &l4.UDP{
			SrcPort: 8080,
			DstPort: 8080,
		},
		Pld: common.RawBytes{1, 2, 3, 4},
	}

	_ = pkt

	rp, _ := rpkt.RtrPktFromScnPkt(&pkt, nil)

	rp.L4Type = common.L4ProtocolType(L4Type)
	rp.Ingress.IfID = common.IFIDType(intf)
	return rp
}

func bBenchmarkQueueSinglePacket(b *testing.B) {
	root := log15.Root()
	file, err := ioutil.TempFile("", "benchmark-log")
	require.NoError(b, err)
	root.SetHandler(log15.Must.FileHandler(file.Name(), log15.LogfmtFormat()))

	extConfig, err := conf.LoadConfig("testdata/sample-config.yaml")
	require.NoError(b, err)
	qosConfig, _ := InitQos(extConfig, forwardPacketByDrop)
	singlePkt := genRouterPacket("1-ff00:0:110", "1-ff00:0:111", 1, 1)

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		qosConfig.QueuePacket(singlePkt)
	}
}

// BenchmarkQueueSinglePacket measures the performance of the queue. Run with
// go test -v -run=^$ -bench=BenchmarkQueueSinglePacket ./go/border/qos/ \
//    -benchtime=20s -cpuprofile=newprofile.pprof
func BenchmarkQueueSinglePacket(t *testing.B) {
	root := log15.Root()
	file, err := ioutil.TempFile("", "benchmark-log")
	require.NoError(t, err)
	root.SetHandler(log15.Must.FileHandler(file.Name(), log15.LogfmtFormat()))

	extConfig, err := conf.LoadConfig("testdata/sample-config.yaml")
	require.NoError(t, err)
	qosConfig, _ := InitQos(extConfig, forwardPacketByDrop)
	arr := getPackets(1)

	t.ResetTimer()
	for n := 0; n < t.N; n++ {
		for _, pkt := range arr {
			qosConfig.QueuePacket(pkt)
		}
	}
}

func forwardPacketByDrop(rp *rpkt.RtrPkt) {
	rp.Release()
}

func getPackets(numberOfPackets int) []*rpkt.RtrPkt {
	pkts := []*rpkt.RtrPkt{
		genRouterPacket("1-ff00:0:110", "1-ff00:0:111", 1, 1),
		genRouterPacket("2-ff00:0:212", "1-ff00:0:111", 1, 1),
		genRouterPacket("3-ff00:0:212", "1-ff00:0:111", 1, 1),
		genRouterPacket("4-ff00:0:212", "1-ff00:0:111", 1, 1),
		genRouterPacket("5-ff00:0:212", "1-ff00:0:111", 1, 1),
		genRouterPacket("6-ff00:0:212", "1-ff00:0:111", 1, 1),
	}
	arr := make([]*rpkt.RtrPkt, numberOfPackets*len(pkts))
	for i := 0; i < numberOfPackets; i++ {
		copy(arr[i*len(pkts):], pkts)
	}
	return arr
}
