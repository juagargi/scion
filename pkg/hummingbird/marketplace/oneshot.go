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
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

// OneShotReservation discovers the marketplace covering the path,
// connects to it, obtains the forward and optional reverse reservations,
// and returns a Hummingbird reservation ready to be used in the dataplane.
//
// The path must be fully covered by exactly one marketplace.
// If reverseBwInKbps is greater than zero,
// the reverse reservation is prepared into a reverse-path E2E extension,
// and attached to the returned forward reservation.
func OneShotReservation(
	ctx context.Context,
	p snet.Path,
	jwt string,
	querier snet.PathQuerier,
	topo snet.Topology,
	insecure bool,
	bwInKbps uint32,
	reverseBwInKbps uint32,
	startsAt time.Time,
	stopsAt time.Time,
	maxPrice uint64,
	buyMode BuyMode,
	fetchReservations bool,
	combineAssets bool,
	numRetries int,
) (*snetpath.Reservation, error) {
	if p == nil {
		return nil, serrors.New("provided path must not be nil")
	}
	scionPath, ok := p.Dataplane().(snetpath.SCION)
	if !ok {
		return nil, serrors.New("provided path must be of type scion")
	}

	market, err := NewPathMarketplaces(p)
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

	forward, reverse, err := market.AcquireReservations(ctx, bwInKbps, reverseBwInKbps,
		startsAt, stopsAt, maxPrice, buyMode, fetchReservations, combineAssets, numRetries)
	if err != nil {
		return nil, serrors.Wrap("obtaining reservations from marketplace", err)
	}
	expected := len(snetpath.InterfacesToBaseHops(p.Metadata().Interfaces))
	if err := checkFlyovers(forward, expected); err != nil {
		return nil, serrors.Wrap("checking bought reservations", err)
	}
	reservation, err := snetpath.NewReservation(
		snetpath.WithDataplanePath(scionPath, p.Destination(), forward),
	)
	if err != nil || reverseBwInKbps == 0 {
		return reservation, err
	}
	if err := checkFlyovers(reverse, expected); err != nil {
		return nil, serrors.Wrap("checking bought reverse reservations", err)
	}
	extn, err := humm.BuildReverseReservationExtn(scionPath, p.Source(), reverse)
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
