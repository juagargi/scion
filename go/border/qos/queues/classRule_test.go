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

package queues_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/scionproto/scion/go/border/qos"
	"github.com/scionproto/scion/go/border/qos/conf"
	"github.com/scionproto/scion/go/border/qos/queues"
	"github.com/scionproto/scion/go/border/rpkt"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/common"
	"github.com/scionproto/scion/go/lib/l4"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/spkt"
)

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

func TestRuleMatchModes(t *testing.T) {
	log.Debug("func TestRuleMatchModes(t *testing.T) {")

	dir, _ := filepath.Abs(filepath.Dir(os.Args[0]))
	fmt.Println(dir)

	extConf, err := conf.LoadConfig("testdata/matchTypeTest-config.yaml")
	if err != nil {
		log.Debug("Load config file failed", "error", err)
		log.Debug("The testdata folder from the parent folder should be available" +
			"for this test but it isn't when running it with bazel." +
			"Just run it without Bazel and it will pass.")
	}
	qosConfig, _ := qos.InitQos(extConf, forwardPacketByDrop)

	classifiers := []queues.ClassRuleInterface{
		&queues.RegularClassRule{},
		&queues.CachelessClassRule{},
	}

	for i := 0; i < len(classifiers); i++ {
		classifiers[i].Init(len(extConf.ExternalRules))
	}

	tables := []struct {
		srcIA       string
		dstIA       string
		l4type      int
		intf        int
		ruleName    string
		queueNumber int
		shouldMatch bool
	}{
		{"11-ff00:0:299", "22-ff00:0:188", 6, 1, "Exact - Exact", 1, true},
		{"33-ff00:0:277", "44-ff00:0:166", 6, 1, "Exact - ISDONLY", 2, true},
		{"33-ff00:0:277", "44-ff00:0:165", 6, 1, "Exact - ISDONLY", 2, true},
		{"33-ff00:0:277", "44-ff00:0:000", 6, 1, "Exact - ISDONLY", 2, true},
		{"55-ff00:0:055", "66-ff00:0:344", 6, 1, "Exact - ASONLY", 3, true},
		{"55-ff00:0:055", "12-ff00:0:344", 6, 1, "Exact - ASONLY", 3, true},
		{"55-ff00:0:055", "13-ff00:0:344", 6, 1, "Exact - ASONLY", 3, true},
		{"55-ff00:0:055", "14-ff00:0:344", 6, 1, "Exact - ASONLY", 3, true},
		{"77-ff00:0:233", "85-ff00:0:222", 6, 1, "Exact - RANGE", 4, true},
		{"77-ff00:0:233", "89-ff00:0:222", 6, 1, "Exact - RANGE", 4, true},
		{"2-ff00:0:011", "89-ff00:0:222", 6, 1, "Exact - RANGE", 4, false},
		{"2-ff00:0:011", "89-ff00:0:222", 6, 1, "Exact - ANY", 5, true},
		{"2-ff00:0:011", "89-ff00:0:344", 6, 1, "Exact - ANY", 5, true},
		{"2-ff00:0:011", "344-ff00:0:222", 6, 1, "Exact - ANY", 5, true},
		{"2-ff00:0:011", "22-344:0:222", 6, 1, "Exact - ANY", 5, true},
		{"2-ff00:0:011", "123-ff00:344:222", 6, 1, "Exact - ANY", 5, true},
		{"123-ff00:344:222", "2-ff00:0:011", 6, 1, "ANY - Exact", 6, true},
		{"123-ff00:344:222", "2-ff00:0:011", 1, 1, "ANY - ANY", 7, true},
		{"123-ff00:344:222", "223-9f33:783:011", 6, 77, "ANY - ANY", 6, false},
		{"123-ff00:344:222", "223-9f33:783:011", 1, 77, "INTF - Exact 77", 9, true},
	}

	for _, classifier := range classifiers {
		for k, tab := range tables {
			pkt := genRouterPacket(tab.srcIA, tab.dstIA, tab.l4type, tab.intf)

			rul := classifier.GetRuleForPacket(qosConfig.GetConfig(), pkt)

			fmt.Println("We got the rule", rul)

			if rul == nil {
				fmt.Println("Rule was nil")
			}

			if (rul.Name == tab.ruleName) != tab.shouldMatch {
				t.Errorf("%d should match rule %v %v but matches rule %v",
					k,
					tab.shouldMatch,
					tab.ruleName,
					rul.Name)
			}
		}
	}
}

func forwardPacketByDrop(rp *rpkt.RtrPkt) {
	rp.Release()
}
