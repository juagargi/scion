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
	queueMetrics.Register("priority", func() float64 {
		return float64(priorityDepth)
	})
	queueMetrics.Register("best_effort", func() float64 {
		return float64(bestEffortDepth)
	})

	expected := `
# HELP router_queue_depth Current number of packets in a router egress queue.
# TYPE router_queue_depth gauge
router_queue_depth{interface="1",isd_as="1-ff00:0:110",neighbor_isd_as="1-ff00:0:111",queue="best_effort"} 5
router_queue_depth{interface="1",isd_as="1-ff00:0:110",neighbor_isd_as="1-ff00:0:111",queue="priority"} 2
`
	require.NoError(t, promtest.GatherAndCompare(
		reg,
		strings.NewReader(strings.TrimLeft(expected, "\n")),
		"router_queue_depth",
	))
}
