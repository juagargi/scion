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
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/marketplace/storage"
	"github.com/scionproto/scion/pkg/addr"
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

// NewSearchService returns a service that can only answer asset searches,
// with pages of at most maxReturnedAssets assets.
func NewSearchService(store *storage.MarketplaceStorage, maxReturnedAssets uint32) *Service {
	return &Service{
		info:  &MarketplaceInfo{MaxReturnedAssets: maxReturnedAssets},
		store: store,
	}
}

// SearchAssetsAs runs a search the way the authentication interceptor would hand it to the
// service, for the AS with the given ISD-AS.
func (s *Service) SearchAssetsAs(
	ia addr.IA,
	req *hummingbird.SearchAssetsRequest,
) (*hummingbird.SearchAssetsResponse, error) {
	rep, err := s.SearchAssets(context.WithValue(context.Background(), "user", ia),
		connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return rep.Msg, nil
}

// StatisticsWindow exposes the rounding of a requested statistics window.
func StatisticsWindow(
	start, end time.Time,
	step, granularity time.Duration,
) (time.Time, time.Time, int) {
	return statisticsWindow(start, end, step, granularity)
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
	return bandwidthPerInterval(assets, windowStart, windowEnd, step, numIntervals)
}
