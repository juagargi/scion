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
	// pongLost counts pong requests that timed out without a reply.
	pongLost prometheus.Counter
	// rtt records round-trip time measurements from pong requests and replies.
	rtt prometheus.Histogram
	// jitter tracks the current RFC 3550 interarrival jitter estimate for pong replies.
	jitter prometheus.Gauge
	// sendRateBps tracks the achieved payload send rate over the last report interval.
	sendRateBps prometheus.Gauge
	// pacingOverrunTotal counts scheduled sends that were already late when reached.
	pacingOverrunTotal prometheus.Counter
	// pacingDelay records the lateness of pacing overruns.
	pacingDelay prometheus.Histogram
	// reservationRenewals counts Hummingbird reservation renewal attempts by result.
	reservationRenewals *prometheus.CounterVec
	// reservationExpiry tracks seconds until the currently active reservation expires.
	reservationExpiry prometheus.Gauge
}

func newClientMetrics() *clientMetrics {
	return &clientMetrics{
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
			Help: "Total number of pong-reply packets received by the client.",
		}),
		pongLost: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_pong_lost_total",
			Help: "Total number of pong requests that timed out without a reply.",
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
		pacingOverrunTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "hummbwtester_client_pacing_overrun_total",
			Help: "Total number of scheduled sends that were already late when reached.",
		}),
		pacingDelay: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "hummbwtester_client_pacing_delay_seconds",
			Help:    "Lateness of pacing overruns.",
			Buckets: prometheus.DefBuckets,
		}),
		reservationRenewals: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "hummbwtester_client_reservation_renewals_total",
			Help: "Total number of Hummingbird reservation renewal attempts, by result.",
		}, []string{"result"}),
		reservationExpiry: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hummbwtester_client_reservation_seconds_until_expiry",
			Help: "Seconds until the currently active reservation expires.",
		}),
	}
}

// serverMetrics holds every Prometheus metric emitted by the server, as listed in the design
// doc's metrics section. All per-client metrics are labeled by the client's address string.
type serverMetrics struct {
	payloadPacketsReceived *prometheus.CounterVec
	payloadBytesReceived   *prometheus.CounterVec
	payloadLost            *prometheus.CounterVec
	payloadOutOfOrder      *prometheus.CounterVec
	pongRequestsReceived   *prometheus.CounterVec
	pongRepliesSent        *prometheus.CounterVec
	receiveRateBps         *prometheus.GaugeVec
	activeClients          prometheus.Gauge
}

func newServerMetrics() *serverMetrics {
	clientLabels := []string{"client"}
	return &serverMetrics{
		payloadPacketsReceived: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "hummbwtester_server_payload_packets_received_total",
			Help: "Total number of payload packets received, by client.",
		}, clientLabels),
		payloadBytesReceived: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "hummbwtester_server_payload_bytes_received_total",
			Help: "Total number of payload bytes received, by client.",
		}, clientLabels),
		payloadLost: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "hummbwtester_server_payload_lost_total",
			Help: "Total number of payload packets finalized as lost, by client.",
		}, clientLabels),
		payloadOutOfOrder: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "hummbwtester_server_payload_out_of_order_total",
			Help: "Total number of payload packets received out of order, by client.",
		}, clientLabels),
		pongRequestsReceived: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "hummbwtester_server_pong_requests_received_total",
			Help: "Total number of pong-request packets received, by client.",
		}, clientLabels),
		pongRepliesSent: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "hummbwtester_server_pong_replies_sent_total",
			Help: "Total number of pong-reply packets sent, by client.",
		}, clientLabels),
		receiveRateBps: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "hummbwtester_server_receive_rate_bps",
			Help: "Achieved payload receive rate, in bits per second, over the last report " +
				"interval, by client.",
		}, clientLabels),
		activeClients: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hummbwtester_server_active_clients",
			Help: "Number of clients with state currently tracked by the server.",
		}),
	}
}
