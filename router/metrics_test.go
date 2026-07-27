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

package router

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestQueueDepthCollector(t *testing.T) {
	collector := newQueueDepthCollector()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(collector))

	priorityDepth := 2
	bestEffortDepth := 5
	labels := MetricLabels{
		Interface:     "1",
		ISDAS:         "1-ff00:0:110",
		NeighborISDAS: "1-ff00:0:111",
	}
	queueMetrics := &QueueDepthMetrics{
		collector: collector,
		labels:    labels,
	}
	priorityObserver := queueMetrics.Register("priority", func() int {
		return priorityDepth
	})
	bestEffortObserver := queueMetrics.Register("best_effort", func() int {
		return bestEffortDepth
	})
	priorityObserver.Observe(7)
	bestEffortObserver.Observe(9)

	expected := `
# HELP router_queue_depth Current number of packets in a router egress queue.
# TYPE router_queue_depth gauge
router_queue_depth{interface="1",isd_as="1-ff00:0:110",neighbor_isd_as="1-ff00:0:111",queue="best_effort"} 5
router_queue_depth{interface="1",isd_as="1-ff00:0:110",neighbor_isd_as="1-ff00:0:111",queue="priority"} 2
# HELP router_queue_depth_high_watermark Maximum number of packets observed in a router egress queue since the previous scrape.
# TYPE router_queue_depth_high_watermark gauge
router_queue_depth_high_watermark{interface="1",isd_as="1-ff00:0:110",neighbor_isd_as="1-ff00:0:111",queue="best_effort"} 9
router_queue_depth_high_watermark{interface="1",isd_as="1-ff00:0:110",neighbor_isd_as="1-ff00:0:111",queue="priority"} 7
`
	require.NoError(t, promtest.GatherAndCompare(
		reg,
		strings.NewReader(strings.TrimLeft(expected, "\n")),
		"router_queue_depth",
		"router_queue_depth_high_watermark",
	))

	expected = `
# HELP router_queue_depth_high_watermark Maximum number of packets observed in a router egress queue since the previous scrape.
# TYPE router_queue_depth_high_watermark gauge
router_queue_depth_high_watermark{interface="1",isd_as="1-ff00:0:110",neighbor_isd_as="1-ff00:0:111",queue="best_effort"} 5
router_queue_depth_high_watermark{interface="1",isd_as="1-ff00:0:110",neighbor_isd_as="1-ff00:0:111",queue="priority"} 2
`
	require.NoError(t, promtest.GatherAndCompare(
		reg,
		strings.NewReader(strings.TrimLeft(expected, "\n")),
		"router_queue_depth_high_watermark",
	))
}

func TestRecordBusyForwarderDropUsesEgressLinkMetrics(t *testing.T) {
	newLink := func() *MockLink {
		metrics := &InterfaceMetrics{}
		for sc := minSizeClass; sc < maxSizeClass; sc++ {
			metrics[sc].DroppedPacketsBusyForwarder =
				prometheus.NewCounter(prometheus.CounterOpts{})
			metrics[sc].DroppedPriorityPacketsBusyForwarder =
				prometheus.NewCounter(prometheus.CounterOpts{})
		}
		return &MockLink{metrics: metrics}
	}

	t.Run("priority", func(t *testing.T) {
		egressLink := newLink()
		packet := &Packet{RawPacket: make([]byte, 128)} // Zero label means priority.
		sc := ClassOfSize(len(packet.RawPacket))

		recordBusyForwarderDrop(packet, egressLink)

		require.Equal(t, float64(1),
			promtest.ToFloat64(egressLink.metrics[sc].DroppedPacketsBusyForwarder))
		require.Equal(t, float64(1),
			promtest.ToFloat64(egressLink.metrics[sc].DroppedPriorityPacketsBusyForwarder))
	})

	t.Run("best effort", func(t *testing.T) {
		egressLink := newLink()
		packet := new(Packet).init(new([bufSize]byte))
		packet.reset(0)
		packet.RawPacket = packet.RawPacket[:128]
		sc := ClassOfSize(len(packet.RawPacket))

		recordBusyForwarderDrop(packet, egressLink)

		require.Equal(t, float64(1),
			promtest.ToFloat64(egressLink.metrics[sc].DroppedPacketsBusyForwarder))
		require.Zero(t,
			promtest.ToFloat64(egressLink.metrics[sc].DroppedPriorityPacketsBusyForwarder))
	})
}
