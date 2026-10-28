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

package marketplace

import (
	"context"
	"time"

	humm "github.com/scionproto/scion/pkg/hummingbird"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

// ReservationRequest is what OneShotReservation buys.
type ReservationRequest struct {
	BwInKbps        uint32
	ReverseBwInKbps uint32
	StartsAt        time.Time
	StopsAt         time.Time
	MaxPrice        uint64
	BuyMode         BuyMode
	FetchExisting   bool
	CombineAssets   bool
	NumRetries      int
	// Selected are the indices, into the base hops of the path, of the ASes to reserve for,
	// in strictly ascending order.
	// Nil selects every AS of the path; non-nil but empty reserves nothing.
	Selected []int
}

// OneShotReservation discovers the marketplace covering the selected ASes of the path,
// connects to it, obtains the forward and optional reverse reservations,
// and returns a Hummingbird reservation ready to be used in the dataplane.
//
// The selected ASes must be fully covered by exactly one marketplace.
// The returned reservation always has one entry per AS of the path, nil for the unselected ones.
// If req.Selected is empty but not nil, nothing is bought, no marketplace is contacted, and the
// reservation has no flyovers (nor a reverse reservation).
// If req.ReverseBwInKbps is greater than zero,
// the reverse reservation (for exactly the mirror of the selected ASes) is prepared into a
// reverse-path E2E extension, and attached to the returned forward reservation.
func OneShotReservation(
	ctx context.Context,
	p snet.Path,
	jwt string,
	querier snet.PathQuerier,
	topo snet.Topology,
	insecure bool,
	req ReservationRequest,
) (*snetpath.Reservation, error) {
	if p == nil {
		return nil, serrors.New("provided path must not be nil")
	}
	scionPath, ok := p.Dataplane().(snetpath.SCION)
	if !ok {
		return nil, serrors.New("provided path must be of type scion")
	}
	if p.Metadata() == nil {
		return nil, serrors.New("path without metadata")
	}
	pathLen := len(snetpath.InterfacesToBaseHops(p.Metadata().Interfaces))

	if req.Selected != nil && len(req.Selected) == 0 {
		log.Debug("no AS selected for reservation, not buying anything", "ases", pathLen)
		return snetpath.NewReservation(
			snetpath.WithDataplanePath(scionPath, p.Destination(),
				make(snetpath.FlyoverSequence, pathLen)),
		)
	}

	market, err := NewPathMarketplacesForASes(p, req.Selected)
	if err != nil {
		return nil, serrors.Wrap("discovering the marketplaces of the path", err)
	}
	count, coverage := market.FullCoverageCount()
	if count != 1 {
		return nil, serrors.New("the path is not covered by a single marketplace",
			"marketplaces", count, "ases", len(market.PathASes))
	}
	if err := market.Connect(ctx, coverage[0].APIAddress, jwt, querier, topo, insecure); err != nil {
		return nil, serrors.Wrap("connecting to the marketplace of the path", err)
	}
	defer func() {
		if err := market.Close(); err != nil {
			log.Error("error closing the marketplace clients", "err", err)
		}
	}()

	forward, reverse, err := market.AcquireReservations(ctx, req.BwInKbps, req.ReverseBwInKbps,
		req.StartsAt, req.StopsAt, req.MaxPrice, req.BuyMode, req.FetchExisting,
		req.CombineAssets, req.NumRetries)
	if err != nil {
		return nil, serrors.Wrap("obtaining reservations from marketplace", err)
	}
	expected := len(market.Hops)
	if err := checkFlyovers(forward, expected); err != nil {
		return nil, serrors.Wrap("checking bought reservations", err)
	}
	selected := req.Selected
	if selected == nil {
		selected = make([]int, pathLen)
		for i := range selected {
			selected[i] = i
		}
	}
	seq, err := humm.ExpandFlyovers(pathLen, selected, forward)
	if err != nil {
		return nil, serrors.Wrap("placing the bought reservations on the path", err)
	}
	reservation, err := snetpath.NewReservation(
		snetpath.WithDataplanePath(scionPath, p.Destination(), seq),
	)
	if err != nil || req.ReverseBwInKbps == 0 {
		return reservation, err
	}
	if err := checkFlyovers(reverse, expected); err != nil {
		return nil, serrors.Wrap("checking bought reverse reservations", err)
	}
	reverseSeq, err := humm.ExpandFlyovers(pathLen, humm.MirrorIndices(pathLen, selected), reverse)
	if err != nil {
		return nil, serrors.Wrap("placing the bought reverse reservations on the path", err)
	}
	extn, err := humm.BuildReverseReservationExtn(scionPath, p.Source(), reverseSeq)
	if err != nil {
		return nil, err
	}
	reservation.SetReverseReservationExtn(extn)
	return reservation, nil
}

func checkFlyovers(hops []*snetpath.Hop, expected int) error {
	if len(hops) != expected {
		return serrors.New("unexpected number of hops", "expected", expected, "actual", len(hops))
	}
	for _, hop := range hops {
		if hop == nil {
			return serrors.New("missing hop")
		}
		if hop.Flyover == nil {
			return serrors.New("hop without flyover, the asset could not be redeemed",
				"ia", hop.IA, "ingress", hop.Ingress, "egress", hop.Egress)
		}
	}
	return nil
}
