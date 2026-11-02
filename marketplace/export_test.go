// Copyright 2026 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package marketplace

import (
	"time"

	"github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
)

func (s *RedemptionService) SetEncodingPoints(encodings []uint32) {
	s.encodingPoints = encodings
}

func (s *RedemptionService) EncodeBandwidth(bw uint32) uint16 {
	return (s.encodeBandwidth(bw))
}

// Out exposes the queue of a connection, so that a test can observe the requests
// the handler routes to it.
func (c *RemoteConn) Out() <-chan *hummingbird.RedeemAssetFromASRequest {
	return c.out
}

// QueueLen exposes how many requests are waiting in a connection's queue, so that a
// test can wait until it is full and the next hand-over is certain to block.
func (c *RemoteConn) QueueLen() int {
	return len(c.out)
}

// QueueCap exposes the capacity of a connection's queue.
func (c *RemoteConn) QueueCap() int {
	return cap(c.out)
}

// PendingLen exposes how many requests are waiting for an answer, so that a test can
// wait until a request has really been registered and is parked in the hand-over.
func (h *RedemptionServerHandler) PendingLen() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pending)
}

// StatisticsWindow exposes the rounding of a requested statistics window.
func StatisticsWindow(
	start, end time.Time,
	step, granularity time.Duration,
) (time.Time, time.Time, int) {
	window_start, window_end, num_intervals := statisticsWindow(start.Unix(), end.Unix(), int64(step/time.Second), int64(granularity/time.Second))
	return time.Unix(window_start, 0).UTC(), time.Unix(window_end, 0).UTC(), int(num_intervals)
}

// BandwidthUtilization exposes the share of the published bandwidth that was bought.
func BandwidthUtilization(bought, published uint64) float64 {
	return bandwidthUtilization(bought, published)
}

// BandwidthPerInterval exposes the spreading of assets over the statistics intervals.
func BandwidthPerInterval(
	assets []*db.DBStat,
	windowStart, windowEnd time.Time,
	step time.Duration,
	numIntervals int,
) (bandwidth, income []uint64) {
	return bandwidthPerInterval(assets, windowStart.Unix(), windowEnd.Unix(), int64(step/time.Second), int64(numIntervals))
}
