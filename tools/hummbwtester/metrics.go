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
	"math"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// clientMetrics holds every Prometheus metric emitted by the client, as listed in the design
// doc's metrics section.
type clientMetrics struct {
	// payloadPacketsSent counts payload packets sent by the client.
	payloadPacketsSent prometheus.Counter
	// payloadBytesSent counts payload bytes sent by the client.
	payloadBytesSent prometheus.Counter
	// pongRequestsSent counts pong-request packets sent by the client.
	pongRequestsSent prometheus.Counter
	// pongRepliesReceived counts pong-reply packets received by the client.
	pongRepliesReceived prometheus.Counter
	// pongLateRepliesReceived counts accepted replies whose request had already timed out.
	pongLateRepliesReceived prometheus.Counter
	// pongLost counts pong requests that timed out without a reply.
	pongLost prometheus.Counter
	// rtt records round-trip time measurements from pong requests and replies.
	rtt prometheus.Histogram
	// jitter tracks the current RFC 3550 interarrival jitter estimate for pong replies.
	jitter prometheus.Gauge
	// sendRateBps tracks the achieved payload send rate over the last report interval.
	sendRateBps prometheus.Gauge
	// marketRoundtripLast stores the most recent successful marketplace roundtrip duration.
	marketRoundtripLast prometheus.Gauge
	// marketRoundtrips counts successful marketplace roundtrips, including the initial one.
	marketRoundtrips prometheus.Counter
	// reservationExpiry tracks seconds until the currently active reservation expires.
	reservationExpiry            prometheus.Gauge
	remotePayloadPacketsReceived prometheus.Counter
	remotePayloadBytesReceived   prometheus.Counter
	remotePayloadLost            prometheus.Counter
	remotePayloadOutOfOrder      prometheus.Counter
	remotePongRequestsReceived   prometheus.Counter
	remotePongRepliesSent        prometheus.Counter
	remoteReceiveRateBps         prometheus.Gauge
	remoteStatsAge               prometheus.Gauge
}

func newClientMetrics() *clientMetrics {
	metrics := &clientMetrics{
		payloadPacketsSent: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_payload_packets_sent_total",
			Help: "Total number of payload packets sent by the client.",
		}),
		payloadBytesSent: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_payload_bytes_sent_total",
			Help: "Total number of payload bytes sent by the client.",
		}),
		pongRequestsSent: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_pong_requests_sent_total",
			Help: "Total number of pong-request packets sent by the client.",
		}),
		pongRepliesReceived: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_pong_replies_received_total",
			Help: "Total newer, non-reordered pong-reply packets accepted by the client.",
		}),
		pongLateRepliesReceived: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_pong_late_replies_received_total",
			Help: "Total accepted pong replies received after their requests timed out.",
		}),
		pongLost: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_pong_lost_total",
			Help: "Total pong requests that timed out; a late reply may still arrive and be accepted.",
		}),
		rtt: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "hummbwtester_client_rtt_seconds",
			Help:    "Round-trip time measured via pong requests/replies.",
			Buckets: prometheus.DefBuckets,
		}),
		jitter: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hummbwtester_client_jitter_seconds",
			Help: "RFC 3550 running interarrival jitter estimate over pong replies.",
		}),
		sendRateBps: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hummbwtester_client_send_rate_bps",
			Help: "Achieved payload send rate, in bits per second, over the last report interval.",
		}),
		marketRoundtripLast: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hummbwtester_client_market_roundtrip_last_seconds",
			Help: "Duration of the most recent successful marketplace roundtrip, in seconds.",
		}),
		marketRoundtrips: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_market_roundtrips_total",
			Help: "Total number of successful marketplace roundtrips.",
		}),
		reservationExpiry: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hummbwtester_client_reservation_seconds_until_expiry",
			Help: "Seconds until the currently active reservation expires.",
		}),
		remotePayloadPacketsReceived: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_remote_payload_packets_received_total",
			Help: "Total payload packets reported received by the remote server.",
		}),
		remotePayloadBytesReceived: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_remote_payload_bytes_received_total",
			Help: "Total payload bytes reported received by the remote server.",
		}),
		remotePayloadLost: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_remote_payload_lost_total",
			Help: "Total payload packets reported finalized as lost by the remote server.",
		}),
		remotePayloadOutOfOrder: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_remote_payload_out_of_order_total",
			Help: "Total payload packets reported received out of order by the remote server.",
		}),
		remotePongRequestsReceived: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_remote_pong_requests_received_total",
			Help: "Total pong requests reported received by the remote server.",
		}),
		remotePongRepliesSent: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_remote_pong_replies_sent_total",
			Help: "Total pong replies reported sent by the remote server.",
		}),
		remoteReceiveRateBps: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hummbwtester_client_remote_receive_rate_bps",
			Help: "Remote payload receive rate computed over client-local reply arrival time.",
		}),
		remoteStatsAge: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hummbwtester_client_remote_stats_age_seconds",
			Help: "Seconds on the client clock since the latest accepted remote statistics snapshot; -1 before the first snapshot.",
		}),
	}
	metrics.marketRoundtripLast.Set(math.NaN())
	return metrics
}
