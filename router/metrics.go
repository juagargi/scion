// Copyright 2020 Anapaya Systems
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

package router

import (
	"iter"
	"math/bits"
	"strconv"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/scionproto/scion/pkg/addr"
	libprom "github.com/scionproto/scion/pkg/private/prom"
)

// Metrics defines the data-plane metrics for the BR.
type Metrics struct {
	InputBytesTotal            *prometheus.CounterVec
	OutputBytesTotal           *prometheus.CounterVec
	InputPacketsTotal          *prometheus.CounterVec
	OutputPacketsTotal         *prometheus.CounterVec
	ProcessedPackets           *prometheus.CounterVec
	PriorityForwardedPackets   *prometheus.CounterVec
	DroppedPacketsTotal        *prometheus.CounterVec
	HummProcessedPackets       *prometheus.CounterVec
	HummFlyoverPackets         *prometheus.CounterVec
	HummDemotedFreshnessPkts   *prometheus.CounterVec
	HummDemotedExpiredPkts     *prometheus.CounterVec
	HummDemotedTokenBucketPkts *prometheus.CounterVec
	InterfaceUp                *prometheus.GaugeVec
	BFDInterfaceStateChanges   *prometheus.CounterVec
	BFDPacketsSent             *prometheus.CounterVec
	BFDPacketsReceived         *prometheus.CounterVec
	ServiceInstanceCount       *prometheus.GaugeVec
	ServiceInstanceChanges     *prometheus.CounterVec
	SiblingReachable           *prometheus.GaugeVec
	SiblingBFDPacketsSent      *prometheus.CounterVec
	SiblingBFDPacketsReceived  *prometheus.CounterVec
	SiblingBFDStateChanges     *prometheus.CounterVec
	// QueueDepth is a scrape-time collector over egress queue occupancy. Unlike InterfaceMetrics,
	// these metrics are tied to queue-owning underlay connections rather than to traffic size
	// classes, because detached sibling links can share the same underlying queues.
	QueueDepth *queueDepthCollector
}

// MetricLabels carries the common router interface labels for metrics.
type MetricLabels struct {
	Interface     string
	ISDAS         string
	NeighborISDAS string
}

// prometheusLabels converts the compact label holder into the map form expected by Prometheus
// vector metrics.
func (l MetricLabels) prometheusLabels() prometheus.Labels {
	return prometheus.Labels{
		"interface":       l.Interface,
		"isd_as":          l.ISDAS,
		"neighbor_isd_as": l.NeighborISDAS,
	}
}

// QueueDepthMetrics registers queue depth callbacks for one queue-owning connection.
// It is intentionally separate from InterfaceMetrics: queue depth is connection-local state,
// whereas InterfaceMetrics stores traffic counters pre-expanded by size class.
type QueueDepthMetrics struct {
	collector *queueDepthCollector
	labels    MetricLabels
}

// NewQueueDepthMetrics returns a connection-scoped registration handle for queue depth metrics.
func NewQueueDepthMetrics(metrics *Metrics, labels MetricLabels) *QueueDepthMetrics {
	if metrics == nil || metrics.QueueDepth == nil {
		return nil
	}
	return &QueueDepthMetrics{
		collector: metrics.QueueDepth,
		labels:    labels,
	}
}

// Register registers one queue depth callback under the connection's labels.
// The callback is invoked only when Prometheus scrapes, which keeps queue metrics off the packet
// enqueue/dequeue hot path.
func (m *QueueDepthMetrics) Register(queue string, readDepth func() float64) {
	if m == nil {
		return
	}
	m.collector.Register(m.labels, queue, readDepth)
}

// queueDepthCollector is a custom collector because queue depth is naturally sampled on demand:
// the underlay can expose a callback that reads len(queue) at scrape time, instead of updating a
// mutable gauge on every push/pop or on a timer.
type queueDepthCollector struct {
	desc *prometheus.Desc
	mu   sync.RWMutex
	fns  map[string]queueDepthFunc
}

type queueDepthFunc struct {
	labels [4]string
	read   func() float64
}

func newQueueDepthCollector() *queueDepthCollector {
	return &queueDepthCollector{
		desc: prometheus.NewDesc(
			"router_queue_depth",
			"Current number of packets in a router egress queue.",
			[]string{"interface", "isd_as", "neighbor_isd_as", "queue"},
			nil,
		),
		fns: make(map[string]queueDepthFunc),
	}
}

func (c *queueDepthCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect snapshots the registered callbacks and emits one gauge per
// (interface, isd_as, neighbor_isd_as, queue) label tuple.
func (c *queueDepthCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	snapshot := make([]queueDepthFunc, 0, len(c.fns))
	for _, fn := range c.fns {
		snapshot = append(snapshot, fn)
	}
	c.mu.RUnlock()

	for _, fn := range snapshot {
		ch <- prometheus.MustNewConstMetric(
			c.desc,
			prometheus.GaugeValue,
			fn.read(),
			fn.labels[:]...,
		)
	}
}

// Register installs or replaces the callback for one labeled queue series.
func (c *queueDepthCollector) Register(
	labels MetricLabels,
	queue string,
	readDepth func() float64,
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := strings.Join([]string{
		labels.Interface,
		labels.ISDAS,
		labels.NeighborISDAS,
		queue,
	}, "\x00")
	c.fns[key] = queueDepthFunc{
		labels: [4]string{
			labels.Interface,
			labels.ISDAS,
			labels.NeighborISDAS,
			queue,
		},
		read: readDepth,
	}
}

// NewMetrics initializes the metrics for the Border Router, and registers them with the default
// registry.
//
// Most BR metrics are plain Prometheus vectors. QueueDepth is the exception: it is a custom
// collector that reads queue occupancy lazily at scrape time.
func NewMetrics() *Metrics {
	queueDepth := libprom.SafeRegister(newQueueDepthCollector()).(*queueDepthCollector)
	return &Metrics{
		ProcessedPackets: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_processed_pkts_total",
				Help: "Total number of packets processed by the processor",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as", "sizeclass"},
		),
		PriorityForwardedPackets: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_priority_forwarded_pkts_total",
				Help: "Total number of priority packets successfully forwarded by the router",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as", "sizeclass"},
		),
		InputBytesTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_input_bytes_total",
				Help: "Total number of bytes received",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as", "sizeclass"},
		),
		OutputBytesTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_output_bytes_total",
				Help: "Total number of bytes sent.",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as", "sizeclass", "type"},
		),
		InputPacketsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_input_pkts_total",
				Help: "Total number of packets received",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as", "sizeclass"},
		),
		OutputPacketsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_output_pkts_total",
				Help: "Total number of packets sent.",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as", "sizeclass", "type"},
		),
		DroppedPacketsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_dropped_pkts_total",
				Help: "Total number of packets dropped by the router.",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as", "sizeclass", "reason"},
		),
		HummProcessedPackets: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_humm_processed_pkts_total",
				Help: "Total number of Hummingbird packets received by the router processor",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as", "sizeclass"},
		),
		HummFlyoverPackets: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_humm_flyover_pkts_total",
				Help: "Total number of parsed Hummingbird packets with a flyover",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as", "sizeclass"},
		),
		HummDemotedFreshnessPkts: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_humm_demoted_freshness_total",
				Help: "Total number of Hummingbird packets demoted to best-effort due to freshness checks",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as", "sizeclass"},
		),
		HummDemotedExpiredPkts: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_humm_demoted_expired_total",
				Help: "Total number of Hummingbird packets demoted to best-effort due to expired reservations",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as", "sizeclass"},
		),
		HummDemotedTokenBucketPkts: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_humm_demoted_tokenbucket_total",
				Help: "Total number of Hummingbird packets demoted to best-effort due to token bucket checks",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as", "sizeclass"},
		),
		InterfaceUp: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "router_interface_up",
				Help: "Either zero or one depending on whether the interface is up.",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as"},
		),
		BFDInterfaceStateChanges: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_bfd_state_changes_total",
				Help: "Total number of BFD state changes.",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as"},
		),
		BFDPacketsSent: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_bfd_sent_packets_total",
				Help: "Number of BFD packets sent.",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as"},
		),
		BFDPacketsReceived: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_bfd_received_packets_total",
				Help: "Number of BFD packets received.",
			},
			[]string{"interface", "isd_as", "neighbor_isd_as"},
		),
		ServiceInstanceCount: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "router_service_instance_count",
				Help: "Number of service instances known by the data plane.",
			},
			[]string{"service", "isd_as"},
		),
		ServiceInstanceChanges: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_service_instance_changes_total",
				Help: "Number of total service instance changes. Both addition and removal of a " +
					"service instance is accumulated.",
			},
			[]string{"service", "isd_as"},
		),
		SiblingReachable: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "router_sibling_reachable",
				Help: "Either zero or one depending on whether a sibling router " +
					"instance is reachable.",
			},
			[]string{"sibling", "isd_as"},
		),
		SiblingBFDPacketsSent: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_bfd_sent_sibling_packets_total",
				Help: "Number of BFD packets sent to sibling router instance.",
			},
			[]string{"sibling", "isd_as"},
		),
		SiblingBFDPacketsReceived: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_bfd_received_sibling_packets_total",
				Help: "Number of BFD packets received from sibling router instance.",
			},
			[]string{"sibling", "isd_as"},
		),
		SiblingBFDStateChanges: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "router_bfd_sibling_state_changes_total",
				Help: "Total number of BFD state changes for sibling router instances",
			},
			[]string{"sibling", "isd_as"},
		),
		QueueDepth: queueDepth,
	}
}

// trafficType labels traffic as being of either of the following types: in, out, inTransit,
// outTransit, brTransit. inTransit or outTransit means that traffic is crossing the local AS via
// two routers. If the router being observed is the one receiving the packet from the outside, then
// the type is inTransit; else it is outTransit. brTransit means that traffic is crossing only the
// observed router. Non-scion traffic or somehow malformed traffic has type Other.
// Do not change this type's length without checking the effect it has on router.packet
type trafficType uint8

const (
	ttOther trafficType = iota
	ttIn
	ttOut
	ttInTransit
	ttOutTransit
	ttBrTransit
	ttMax
)

// Returns a human-friendly representation of the given traffic type.
func (t trafficType) String() string {
	switch t {
	case ttIn:
		return "in"
	case ttOut:
		return "out"
	case ttInTransit:
		return "in_transit"
	case ttOutTransit:
		return "out_transit"
	case ttBrTransit:
		return "br_transit"
	}
	return "other"
}

// sizeClass is the number of bits needed to represent some given size. This is quicker than
// computing Log2 and serves the same purpose.
type sizeClass uint8

// maxSizeClass is the smallest NOT-supported sizeClass. This must be enough to support the largest
// valid packet size (defined by bufSize). Since this must be a constant (to allow efficient
// fixed-sized arrays), we have to assert it's large enough for bufSize. Just in case we do get
// packets larger than bufSize, they are simply put in the last class.
const maxSizeClass sizeClass = 15

// This will failto compile if bufSize cannot fit in (maxSizeClass - 1) bits.
const _ = uint(1<<(maxSizeClass-1) - 1 - bufSize)

// minSizeClass is the smallest sizeClass that we care about.
// All smaller classes are conflated with this one.
const minSizeClass sizeClass = 6

func ClassOfSize(pktSize int) sizeClass {
	cs := sizeClass(bits.Len32(uint32(pktSize)))
	if cs > maxSizeClass-1 {
		return maxSizeClass - 1
	}
	if cs <= minSizeClass {
		return minSizeClass
	}
	return cs
}

// Returns a human-friendly representation of the given size class. Avoid bracket notation to make
// the values possibly easier to use in monitoring queries.
func (sc sizeClass) String() string {
	low := strconv.Itoa((1 << sc) >> 1)
	high := strconv.Itoa((1 << sc) - 1)
	if sc == minSizeClass {
		low = "0"
	}
	if sc == maxSizeClass {
		high = "inf"
	}

	return strings.Join([]string{low, high}, "_")
}

// interfaceMetrics is the set of metrics that are relevant for one given interface. It is a map
// that associates each (traffic-type, size-class) pair with the set of metrics belonging to that
// interface that have these label values. This set of metrics is itself a trafficMetric structure.
// Explanation: Metrics are labeled by interface, local-as, neighbor-as, packet size, and (for
// output metrics only) traffic type. Instances are grouped in a hierarchical manner for efficient
// access by the using code. forwardingMetrics is a map of interface to interfaceMetrics. To access
// a specific InputPacketsTotal counter, one refers to:
//
//	dataplane.forwardingMetrics[interface][size-class].
//
// trafficMetrics.Output is an array of outputMetrics indexed by traffic type.
type InterfaceMetrics [maxSizeClass]trafficMetrics

// trafficMetrics groups all the metrics instances that all share the same interface AND
// sizeClass label values (but have different names - i.e. they count different things).
type trafficMetrics struct {
	InputBytesTotal             prometheus.Counter
	InputPacketsTotal           prometheus.Counter
	DroppedPacketsInvalid       prometheus.Counter
	DroppedPacketsBusyProcessor prometheus.Counter
	DroppedPacketsBusyForwarder prometheus.Counter
	DroppedPacketsBusySlowPath  prometheus.Counter
	ProcessedPackets            prometheus.Counter
	PriorityForwardedPackets    prometheus.Counter
	HummProcessedPackets        prometheus.Counter
	HummFlyoverPackets          prometheus.Counter
	HummDemotedFreshnessPkts    prometheus.Counter
	HummDemotedExpiredPkts      prometheus.Counter
	HummDemotedTokenBucketPkts  prometheus.Counter
	Output                      [ttMax]outputMetrics
}

// outputMetrics groups all the metrics about traffic that has reached the output stage. Metrics
// instances in each of these all have the same interface AND sizeClass AND trafficType label
// values.
type outputMetrics struct {
	OutputBytesTotal   prometheus.Counter
	OutputPacketsTotal prometheus.Counter
}

// newInterfaceMetrics creates the per-interface, per-size-class counter bundle used on the traffic
// fast path. Queue depth is intentionally not part of this structure because queue ownership does
// not always align one-to-one with logical interfaces.
func newInterfaceMetrics(
	metrics *Metrics,
	id uint16,
	localIA addr.IA,
	sibling string,
	neighbor addr.IA) *InterfaceMetrics {

	ifLabels := newMetricLabels(id, localIA, sibling, neighbor).prometheusLabels()
	m := InterfaceMetrics{}
	for sc := minSizeClass; sc < maxSizeClass; sc++ {
		scLabels := prometheus.Labels{"sizeclass": sc.String()}
		m[sc] = newTrafficMetrics(metrics, ifLabels, scLabels)
	}
	return &m
}

func newTrafficMetrics(
	metrics *Metrics,
	ifLabels prometheus.Labels,
	scLabels prometheus.Labels) trafficMetrics {

	c := trafficMetrics{
		InputBytesTotal:   metrics.InputBytesTotal.MustCurryWith(ifLabels).With(scLabels),
		InputPacketsTotal: metrics.InputPacketsTotal.MustCurryWith(ifLabels).With(scLabels),
		ProcessedPackets:  metrics.ProcessedPackets.MustCurryWith(ifLabels).With(scLabels),
		PriorityForwardedPackets: metrics.PriorityForwardedPackets.MustCurryWith(ifLabels).
			With(scLabels),
		HummProcessedPackets: metrics.HummProcessedPackets.MustCurryWith(ifLabels).With(scLabels),
		HummFlyoverPackets:   metrics.HummFlyoverPackets.MustCurryWith(ifLabels).With(scLabels),
		HummDemotedFreshnessPkts: metrics.HummDemotedFreshnessPkts.MustCurryWith(ifLabels).
			With(scLabels),
		HummDemotedExpiredPkts: metrics.HummDemotedExpiredPkts.MustCurryWith(ifLabels).With(scLabels),
		HummDemotedTokenBucketPkts: metrics.HummDemotedTokenBucketPkts.MustCurryWith(ifLabels).
			With(scLabels),
	}

	// Output metrics have the extra "trafficType" label.
	for t := ttOther; t < ttMax; t++ {
		ttLabels := prometheus.Labels{"type": t.String()}
		c.Output[t] = newOutputMetrics(metrics, ifLabels, scLabels, ttLabels)
	}

	// Dropped metrics have the extra "Reason" label.
	reasonMap := map[string]string{}

	reasonMap["reason"] = "invalid"
	c.DroppedPacketsInvalid =
		metrics.DroppedPacketsTotal.MustCurryWith(ifLabels).MustCurryWith(scLabels).With(reasonMap)

	reasonMap["reason"] = "busy_processor"
	c.DroppedPacketsBusyProcessor =
		metrics.DroppedPacketsTotal.MustCurryWith(ifLabels).MustCurryWith(scLabels).With(reasonMap)

	reasonMap["reason"] = "busy_forwarder"
	c.DroppedPacketsBusyForwarder =
		metrics.DroppedPacketsTotal.MustCurryWith(ifLabels).MustCurryWith(scLabels).With(reasonMap)

	reasonMap["reason"] = "busy_slow_path"
	c.DroppedPacketsBusySlowPath =
		metrics.DroppedPacketsTotal.MustCurryWith(ifLabels).MustCurryWith(scLabels).With(reasonMap)

	c.InputBytesTotal.Add(0)
	c.InputPacketsTotal.Add(0)
	c.DroppedPacketsInvalid.Add(0)
	c.DroppedPacketsBusyProcessor.Add(0)
	c.DroppedPacketsBusyForwarder.Add(0)
	c.DroppedPacketsBusySlowPath.Add(0)
	c.ProcessedPackets.Add(0)
	c.PriorityForwardedPackets.Add(0)
	c.HummProcessedPackets.Add(0)
	c.HummFlyoverPackets.Add(0)
	c.HummDemotedFreshnessPkts.Add(0)
	c.HummDemotedExpiredPkts.Add(0)
	c.HummDemotedTokenBucketPkts.Add(0)
	return c
}

func newOutputMetrics(
	metrics *Metrics,
	ifLabels prometheus.Labels,
	scLabels prometheus.Labels,
	ttLabels prometheus.Labels) outputMetrics {

	om := outputMetrics{}
	om.OutputBytesTotal =
		metrics.OutputBytesTotal.MustCurryWith(ifLabels).MustCurryWith(scLabels).With(ttLabels)
	om.OutputPacketsTotal =
		metrics.OutputPacketsTotal.MustCurryWith(ifLabels).MustCurryWith(scLabels).With(ttLabels)
	om.OutputBytesTotal.Add(0)
	om.OutputPacketsTotal.Add(0)
	return om
}

// newMetricLabels centralizes the router's interface labeling scheme so both vector metrics and
// scrape-time collectors use identical label values.
func newMetricLabels(
	id uint16, localIA addr.IA, sibling string, neighbor addr.IA) MetricLabels {

	if sibling != "" {
		// For siblings, we label with the address of the sibling router. The ifID isn't relevant
		// (it's just a unique key in the metrics table but the link is shared with other ifIDs).
		// and we don't know the far AS (the neighbor's is the same as local; that's not useful
		// so we don't make a label with it).
		return MetricLabels{
			ISDAS:         localIA.String(),
			Interface:     "sibling->" + sibling,
			NeighborISDAS: "unknown",
		}
	}

	if id == 0 {
		// Internal interface
		return MetricLabels{
			ISDAS:         localIA.String(),
			Interface:     "internal",
			NeighborISDAS: localIA.String(),
		}
	}

	// External interface
	return MetricLabels{
		ISDAS:         localIA.String(),
		Interface:     strconv.FormatUint(uint64(id), 10),
		NeighborISDAS: neighbor.String(),
	}
}

func serviceLabels(localIA addr.IA, svc addr.SVC) prometheus.Labels {
	return prometheus.Labels{
		"isd_as":  localIA.String(),
		"service": svc.BaseString(),
	}
}

// UpdateOutputMetrics updates the given InterfaceMetrics in bulk according
// to the given set of just sent packets. This is much faster than looking up
// the right set of metrics by size class and traffic type for each packet.
func UpdateOutputMetrics(metrics *InterfaceMetrics, packets iter.Seq[*Packet]) {
	// We need to collect stats by traffic type and size class.
	// Try to reduce the metrics lookup penalty by using some
	// simpler staging data structure.
	writtenPkts := [ttMax][maxSizeClass]int{}
	writtenBytes := [ttMax][maxSizeClass]int{}
	for p := range packets {
		s := len(p.RawPacket)
		sc := ClassOfSize(s)
		tt := p.trafficType
		writtenPkts[tt][sc]++
		writtenBytes[tt][sc] += s
	}
	for t := ttOther; t < ttMax; t++ {
		for sc := minSizeClass; sc < maxSizeClass; sc++ {
			if writtenPkts[t][sc] > 0 {
				metrics[sc].Output[t].OutputPacketsTotal.Add(float64(writtenPkts[t][sc]))
				metrics[sc].Output[t].OutputBytesTotal.Add(float64(writtenBytes[t][sc]))
			}
		}
	}
}
