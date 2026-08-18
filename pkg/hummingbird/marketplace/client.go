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
	"bytes"
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/scionproto/scion/pkg/addr"
	hbird "github.com/scionproto/scion/pkg/hummingbird"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	"github.com/scionproto/scion/pkg/proto/hummingbird/v1/hummingbirdconnect"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type MarketplaceClient struct {
	client hummingbirdconnect.MarketplaceServiceClient
}

// Creates a new marketplace client.
// Valid URL forms:
// https://127.0.0.1:31888
// https://my-marketplace.local:31888
// [1-ff00:0:111,127.0.0.1]:31888
// [1-ff00:0:111,my-marketplace.local]:31888
// If a SCION url is provided, the client will connect over the SCION network,
// otherwise over the public internet.
// If a dns name is provided, dns resolution will be done using local dns resolver.
// If insecure = true, server certificate validation will be disabled.
// path querier and local topology is only required for scion connections
func NewMarketplaceClient(
	ctx context.Context,
	url string,
	token string,
	querier snet.PathQuerier,
	topo snet.Topology,
	insecure bool,
) (*MarketplaceClient, error) {
	clients, err := NewClientSet(ctx, url, token, ClientOptions{
		Querier:  querier,
		Topology: topo,
		Insecure: insecure,
	})
	if err != nil {
		return nil, err
	}
	return &MarketplaceClient{client: clients.Marketplace}, nil
}

type BuyMode int

const (
	FailOnError BuyMode = iota
	ContinueOnError
)

type InterfacePair struct {
	IA      uint64
	Ingress uint32
	Egress  uint32
}

func interfacePairsFromInterfaces(ifaces []snet.PathInterface) []InterfacePair {
	baseHops := snetpath.InterfacesToBaseHops(ifaces)
	pairs := make([]InterfacePair, 0, len(baseHops))
	for _, hop := range baseHops {
		pairs = append(pairs, InterfacePair{
			IA:      uint64(hop.IA),
			Ingress: uint32(hop.Ingress),
			Egress:  uint32(hop.Egress),
		})
	}
	return pairs
}

func (c *MarketplaceClient) findExistingReservations(
	ctx context.Context,
	pairs []InterfacePair,
	bwInKbps uint32,
	startsAt time.Time,
	stopsAt time.Time,
) ([]*snetpath.Hop, error) {
	req := &hummingbird.FetchReservationsRequest{
		Bandwidth: &bwInKbps,
		StartsAt:  timestamppb.New(startsAt),
		StopsAt:   timestamppb.New(stopsAt),
	}
	res, err := c.client.FetchReservations(
		ctx, &connect.Request[hummingbird.FetchReservationsRequest]{
			Msg: req,
		})
	if err != nil {
		return nil, err
	}
	ret := make([]*snetpath.Hop, len(pairs))
	for i, pair := range pairs {
		for _, r := range res.Msg.Reservations {
			if r.Ia == pair.IA && r.IngressId == pair.Ingress && r.EgressId == pair.Egress {
				ret[i] = &snetpath.Hop{
					BaseHop: snetpath.BaseHop{
						IA:      addr.IA(r.Ia),
						Ingress: uint16(r.IngressId),
						Egress:  uint16(r.EgressId),
					},
					Flyover: &snetpath.FlyoverData{
						ResID:     r.ReservationId,
						Ak:        [16]byte(r.AuthenticationKey),
						Bw:        uint16(r.DataplaneEncoding),
						StartTime: uint32(r.StartsAt.Seconds),
						Duration:  uint16(r.StopsAt.Seconds - r.StartsAt.Seconds),
					},
				}
				break
			}
		}
	}
	return ret, nil
}

func filter[T any](s []*T, keep func(*T) bool) []*T {
	result := make([]*T, 0, len(s))
	for _, v := range s {
		if keep(v) {
			result = append(result, v)
		}
	}
	return result
}

func gcd(a, b uint32) uint32 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func lcm(a, b uint32) uint32 {
	return a / gcd(a, b) * b
}

func timeMin(a time.Time, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func activeAt(
	elements []*hummingbird.SearchAsset,
	t time.Time,
	bw uint32,
) []*hummingbird.SearchAsset {
	// Find first element with StartsAt > t
	end := sort.Search(len(elements), func(i int) bool {
		return elements[i].StartsAt.AsTime().After(t)
	})

	result := make([]*hummingbird.SearchAsset, 0)

	for i := 0; i < end; i++ {
		if t.Before(elements[i].StopsAt.AsTime()) && elements[i].Bandwidth >= bw {
			result = append(result, elements[i])
		}
	}
	return result
}

// recursiveSelect
func (c *MarketplaceClient) recursiveSelectStart(
	assets []*hummingbird.SearchAsset,
	bwInKbps uint32,
	startsAt time.Time,
	stopsAt time.Time,
	combineCost uint64,
) ([]*hummingbird.BuyAsset, uint64, bool) {

	sort.Slice(assets, func(i, j int) bool {
		return assets[i].StartsAt.Seconds < assets[j].StartsAt.Seconds
	})
	assetMap := make(map[string]*hummingbird.SearchAsset)
	for _, asset := range assets {
		assetMap[string(asset.AssetId)] = asset
	}
	filterValidAt := func(
		t time.Time,
		assets []*hummingbird.SearchAsset,
	) []*hummingbird.SearchAsset {
		return activeAt(assets, t, bwInKbps)
	}

	nextAssets := filterValidAt(startsAt, assets)
	if len(nextAssets) == 0 {
		return nil, 0, false
	}
	allChains := []chain{}
	for _, nextAsset := range nextAssets {
		resultChains, ok := c.recursiveSelect(nextAsset, assets, bwInKbps, 1, 1, startsAt, stopsAt)
		if !ok {
			continue
		}
		for _, resultChain := range resultChains {
			allChains = append(allChains, resultChain)
		}
	}
	actualPrice := func(a *hummingbird.SearchAsset, duration time.Duration) uint64 {
		price, err := hbird.ReservationPrice(
			a.Price,
			bwInKbps,
			a.BandwidthMin,
			a.TimeMinDuration,
			a.TimeGranularity,
			duration)
		if err != nil {
			return math.MaxUint64
		}
		return price
	}
	bestCost := uint64(math.MaxInt64)
	bestSelect := []*hummingbird.BuyAsset{}
	for _, chain := range allChains {
		currCost := uint64(0)
		currSelect := []*hummingbird.BuyAsset{}
		if len(chain.ids) == 1 {
			asset := assetMap[string(chain.ids[0])]
			currCost += actualPrice(asset, chain.stopsAt.Sub(startsAt))
			currSelect = append(currSelect, &hummingbird.BuyAsset{
				AssetId:         asset.AssetId,
				StartsAtExactly: timestamppb.New(startsAt),
				StopsAtExactly:  timestamppb.New(chain.stopsAt),
				BandwidthExact:  chain.bandwidthMin,
			})
		} else {
			currCost = combineCost * uint64(len(chain.ids)-1)
			currStartsAt := startsAt
			i := 0
			for ; i < len(chain.ids)-1; i++ {
				currAsset := assetMap[string(chain.ids[i])]
				nextAsset := assetMap[string(chain.ids[i+1])]
				if currAsset.Price <= nextAsset.Price {
					duration := timeMin(
						currAsset.StopsAt.AsTime(),
						chain.stopsAt).Sub(currStartsAt)
					currCost += actualPrice(currAsset, duration)

					currSelect = append(currSelect, &hummingbird.BuyAsset{
						AssetId:         currAsset.AssetId,
						StartsAtExactly: timestamppb.New(currStartsAt),
						StopsAtExactly: timestamppb.New(timeMin(
							currAsset.StopsAt.AsTime(),
							chain.stopsAt)),
						BandwidthExact: chain.bandwidthMin,
					})
					currStartsAt = timeMin(currAsset.StopsAt.AsTime(), chain.stopsAt)

				} else {
					duration := timeMin(
						nextAsset.StartsAt.AsTime(),
						chain.stopsAt).Sub(currStartsAt)
					currCost += actualPrice(currAsset, duration)
					currSelect = append(currSelect, &hummingbird.BuyAsset{
						AssetId:         currAsset.AssetId,
						StartsAtExactly: timestamppb.New(currStartsAt),
						StopsAtExactly: timestamppb.New(timeMin(
							nextAsset.StartsAt.AsTime(),
							chain.stopsAt)),
						BandwidthExact: chain.bandwidthMin,
					})
					currStartsAt = timeMin(nextAsset.StartsAt.AsTime(), chain.stopsAt)
				}
			}
			lastAsset := assetMap[string(chain.ids[i])]
			duration := timeMin(lastAsset.StopsAt.AsTime(), chain.stopsAt).Sub(currStartsAt)
			currCost += actualPrice(lastAsset, duration)

			currSelect = append(currSelect, &hummingbird.BuyAsset{
				AssetId:         lastAsset.AssetId,
				StartsAtExactly: timestamppb.New(currStartsAt),
				StopsAtExactly: timestamppb.New(timeMin(
					lastAsset.StopsAt.AsTime(),
					chain.stopsAt)),
				BandwidthExact: chain.bandwidthMin,
			})
		}
		if currCost < bestCost {
			bestCost = currCost
			bestSelect = currSelect
		}
	}

	return bestSelect, bestCost, len(bestSelect) != 0
}

type chain struct {
	ids             [][]byte
	timeGranularity uint32
	timeMinDuration uint32
	bandwidthMin    uint32
	stopsAt         time.Time
}

func (c *MarketplaceClient) recursiveSelect(
	currentAsset *hummingbird.SearchAsset,
	otherAssets []*hummingbird.SearchAsset,
	previousBw uint32,
	previousTimeMinDuration uint32,
	previousTimeGranularity uint32,
	globalStart time.Time,
	globalStop time.Time,
) ([]chain, bool) {
	currBW := max(previousBw, currentAsset.BandwidthMin)
	timeGranularity := lcm(previousTimeGranularity, currentAsset.TimeGranularity)
	timeMinDuration := max(previousTimeMinDuration, currentAsset.TimeMinDuration)
	filterValidAt := func(
		t time.Time,
		assets []*hummingbird.SearchAsset,
	) []*hummingbird.SearchAsset {
		return activeAt(assets, t, currBW)
	}
	remainder := uint32(globalStop.Sub(globalStart).Seconds()) % timeGranularity
	if remainder != 0 {
		addedDuration := timeGranularity - remainder
		globalStop = globalStop.Add(time.Duration(addedDuration) * time.Second)
	}
	if uint32(globalStop.Sub(globalStart).Seconds()) < timeMinDuration {
		globalStop = globalStart.Add(time.Duration(timeMinDuration) * time.Second)
	}
	if !currentAsset.StopsAt.AsTime().Before(globalStop) {
		// with this asset we found a chain from start till end
		return []chain{{
			ids:             [][]byte{currentAsset.AssetId},
			bandwidthMin:    currBW,
			timeGranularity: timeGranularity,
			timeMinDuration: timeMinDuration,
			stopsAt:         globalStop}}, true
	}
	nextAssets := filterValidAt(currentAsset.StopsAt.AsTime(), otherAssets)
	if len(nextAssets) == 0 {
		fmt.Println("no assets found to be valid at ", currentAsset.StopsAt.AsTime())
		return nil, false
	}
	allChains := []chain{}
	for _, nextAsset := range nextAssets {
		resultChains, ok := c.recursiveSelect(
			nextAsset,
			otherAssets,
			currBW,
			timeMinDuration,
			timeGranularity,
			globalStart,
			globalStop)
		if !ok {
			fmt.Println("recursive select failed")
			continue
		}
		for _, resultChain := range resultChains {
			if resultChain.bandwidthMin > currentAsset.Bandwidth {
				fmt.Println("bandwidth cannot be satisfied")
				continue
			}
			resultChain.ids = append([][]byte{currentAsset.AssetId}, resultChain.ids...)
			allChains = append(allChains, resultChain)
		}
	}
	return allChains, len(allChains) != 0

}

// checkoutAssetForInterfacePairWithCombine tries to find assets that can be combined
// on the time axis. It does not check for combinations on the bandwidth axis.
func (c *MarketplaceClient) checkoutAssetForInterfacePairWithCombine(
	ctx context.Context,
	pair InterfacePair,
	bwInKbps uint32,
	startsAt time.Time,
	stopsAt time.Time,
	combineCost uint64,
) ([]*hummingbird.BuyAsset, error) {

	ingressAndEgressSuccess := false
	var ingressBuyAssets, egressBuyAssets, pairBuyAssets []*hummingbird.BuyAsset
	var ingressAssetPrice, egressAssetPrice, pairAssetPrice uint64
	var ok bool
	ingressAssetsResponse, err := c.client.SearchAssets(ctx,
		&connect.Request[hummingbird.SearchAssetsRequest]{Msg: &hummingbird.SearchAssetsRequest{
			Owned:           false,
			Ia:              &pair.IA,
			IfIdIngress:     &pair.Ingress,
			MinRequiredBw:   &bwInKbps,
			StartsAtLatest:  timestamppb.New(stopsAt),
			StopsAtEarliest: timestamppb.New(startsAt),
		},
		})
	if err != nil {
		return nil, err
	}
	ingressSearchAssets :=
		filter(ingressAssetsResponse.Msg.Assets, func(a *hummingbird.SearchAsset) bool {
			return a.IfIdEgress == nil
		})
	ingressBuyAssets, ingressAssetPrice, ok =
		c.recursiveSelectStart(ingressSearchAssets, bwInKbps, startsAt, stopsAt, combineCost)
	if ok {
		egressAssetsResponse, err :=
			c.client.SearchAssets(ctx, &connect.Request[hummingbird.SearchAssetsRequest]{
				Msg: &hummingbird.SearchAssetsRequest{
					Owned:           false,
					Ia:              &pair.IA,
					IfIdEgress:      &pair.Egress,
					MinRequiredBw:   &bwInKbps,
					StartsAtLatest:  timestamppb.New(stopsAt),
					StopsAtEarliest: timestamppb.New(startsAt),
				},
			})
		if err != nil {
			return nil, err
		}
		egressSearchAssets :=
			filter(egressAssetsResponse.Msg.Assets, func(a *hummingbird.SearchAsset) bool {
				return a.IfIdIngress == nil
			})
		egressBuyAssets, egressAssetPrice, ok =
			c.recursiveSelectStart(egressSearchAssets, bwInKbps, startsAt, stopsAt, combineCost)
		if ok {
			ingressAndEgressSuccess = true
		}
	}

	pairAssetsResponse, err :=
		c.client.SearchAssets(ctx, &connect.Request[hummingbird.SearchAssetsRequest]{
			Msg: &hummingbird.SearchAssetsRequest{
				Owned:           false,
				Ia:              &pair.IA,
				IfIdIngress:     &pair.Ingress,
				IfIdEgress:      &pair.Egress,
				MinRequiredBw:   &bwInKbps,
				StartsAtLatest:  timestamppb.New(stopsAt),
				StopsAtEarliest: timestamppb.New(startsAt),
			},
		})
	if err != nil {
		return nil, err
	}
	pairBuyAssets, pairAssetPrice, ok = c.recursiveSelectStart(
		pairAssetsResponse.Msg.Assets, bwInKbps, startsAt, stopsAt, combineCost)
	if ok {
		if ingressAndEgressSuccess {
			if ingressAssetPrice+egressAssetPrice < pairAssetPrice {
				return append(ingressBuyAssets, egressBuyAssets...), nil
			}
			return pairBuyAssets, nil
		} else {
			return pairBuyAssets, nil
		}
	} else {
		if ingressAndEgressSuccess {
			return append(ingressBuyAssets, egressBuyAssets...), nil
		} else {
			return nil, serrors.New("no assets found")
		}
	}
}

// checkoutAssetForInterfacePair searches on the marketplace for assets that match
// the interface pair. It searches for ingress, egress and interface-pair assets.
// It ensures that the selected assets when bought should be redeemable without
// further splits or combines necessary.
// The function does not combine assets. If no single (ingress-asset, egress-asset) tuple
// or interface-pair asset can satisfy the request, no buy order is returned.
func (c *MarketplaceClient) checkoutAssetForInterfacePair(
	ctx context.Context,
	pair InterfacePair,
	bwInKbps uint32,
	startsAt time.Time,
	stopsAt time.Time,
) ([]*hummingbird.BuyAsset, error) {
	var ingressSearchAssets, egressSearchAssets, pairSearchAssets []*hummingbird.SearchAsset
	ingressAssetsResponse, err :=
		c.client.SearchAssets(ctx, &connect.Request[hummingbird.SearchAssetsRequest]{
			Msg: &hummingbird.SearchAssetsRequest{
				Owned:           false,
				Ia:              &pair.IA,
				IfIdIngress:     &pair.Ingress,
				MinRequiredBw:   &bwInKbps,
				StartsAtLatest:  timestamppb.New(startsAt),
				StopsAtEarliest: timestamppb.New(stopsAt),
			},
		})
	if err != nil {
		return nil, err
	}
	ingressSearchAssets =
		filter(ingressAssetsResponse.Msg.Assets, func(a *hummingbird.SearchAsset) bool {
			return a.IfIdEgress == nil
		})
	if len(ingressSearchAssets) != 0 {
		egressAssetsResponse, err :=
			c.client.SearchAssets(ctx, &connect.Request[hummingbird.SearchAssetsRequest]{
				Msg: &hummingbird.SearchAssetsRequest{
					Owned:           false,
					Ia:              &pair.IA,
					IfIdEgress:      &pair.Egress,
					MinRequiredBw:   &bwInKbps,
					StartsAtLatest:  timestamppb.New(startsAt),
					StopsAtEarliest: timestamppb.New(stopsAt),
				},
			})
		if err != nil {
			return nil, err
		}
		egressSearchAssets =
			filter(egressAssetsResponse.Msg.Assets, func(a *hummingbird.SearchAsset) bool {
				return a.IfIdIngress == nil
			})
	}

	pairAssetsResponse, err :=
		c.client.SearchAssets(ctx, &connect.Request[hummingbird.SearchAssetsRequest]{
			Msg: &hummingbird.SearchAssetsRequest{
				Owned:           false,
				Ia:              &pair.IA,
				IfIdIngress:     &pair.Ingress,
				IfIdEgress:      &pair.Egress,
				MinRequiredBw:   &bwInKbps,
				StartsAtLatest:  timestamppb.New(startsAt),
				StopsAtEarliest: timestamppb.New(stopsAt),
			},
		})
	if err != nil {
		return nil, err
	}
	pairSearchAssets = pairAssetsResponse.Msg.Assets

	duration := stopsAt.Sub(startsAt)
	actualPrice := func(a *hummingbird.SearchAsset) uint64 {
		price, err := hbird.ReservationPrice(
			a.Price,
			bwInKbps,
			a.BandwidthMin,
			a.TimeMinDuration,
			a.TimeGranularity,
			duration)
		if err != nil {
			return math.MaxUint64
		}
		return price
	}
	filterAssets := func(assets []*hummingbird.SearchAsset) []*hummingbird.SearchAsset {
		return filter(assets, func(a *hummingbird.SearchAsset) bool {
			billableDuration, err :=
				hbird.ReservationDuration(duration, a.TimeMinDuration, a.TimeGranularity)
			if err != nil || a.StopsAt.AsTime().Before(startsAt.Add(billableDuration)) {
				return false
			}
			return a.Bandwidth >= max(bwInKbps, a.BandwidthMin)
		})
	}
	sortAssets := func(assets []*hummingbird.SearchAsset) []*hummingbird.SearchAsset {
		sort.Slice(assets, func(i, j int) bool {
			return actualPrice(assets[i]) < actualPrice(assets[j])
		})
		return assets
	}
	buildBuyRequest := func(a *hummingbird.SearchAsset) *hummingbird.BuyAsset {
		billableDuration, _ :=
			hbird.ReservationDuration(duration, a.TimeMinDuration, a.TimeGranularity)
		return &hummingbird.BuyAsset{
			AssetId:         a.AssetId,
			BandwidthExact:  max(bwInKbps, a.BandwidthMin),
			StartsAtExactly: timestamppb.New(startsAt),
			StopsAtExactly:  timestamppb.New(startsAt.Add(billableDuration)),
		}
	}
	ingressAssets := sortAssets(filterAssets(ingressSearchAssets))
	egressAssets := sortAssets(filterAssets(egressSearchAssets))
	pairAssets := sortAssets(filterAssets(pairSearchAssets))

	order := []*hummingbird.BuyAsset{}
	if len(pairAssets) != 0 {
		if len(ingressAssets) != 0 && len(egressAssets) != 0 {
			if actualPrice(pairAssets[0]) <=
				actualPrice(ingressAssets[0])+actualPrice(egressAssets[0]) {
				order = append(order, buildBuyRequest(pairAssets[0]))
			} else {
				order = append(order,
					buildBuyRequest(ingressAssets[0]), buildBuyRequest(egressAssets[0]))
			}
		} else {
			order = append(order, buildBuyRequest(pairAssets[0]))
		}
	} else if len(ingressAssets) != 0 && len(egressAssets) != 0 {
		order = append(order, buildBuyRequest(ingressAssets[0]), buildBuyRequest(egressAssets[0]))
	} else {
		log.Debug("no assets found", "ia", addr.IA(pair.IA).String(),
			"ingress", pair.Ingress, "egress", pair.Egress,
			"startsat", startsAt, "stopsat", stopsAt)
		return nil, serrors.New("no assets found", "ia", addr.IA(pair.IA).String(),
			"ingress", pair.Ingress, "egress", pair.Egress,
			"startsat", startsAt, "stopsat", stopsAt)
	}
	return order, nil
}

type assetInfo struct {
	id       []byte
	startsAt time.Time
	stopsAt  time.Time
}

func (c *MarketplaceClient) ObtainReservationsForInterfacePairs(
	ctx context.Context,
	pairs []InterfacePair,
	bwInKbps uint32,
	startsAt time.Time,
	stopsAt time.Time,
	maxPrice uint64,
	buyMode BuyMode,
	fetchReservations bool,
	combineAssets bool,
	num_retries int,
) ([]*snetpath.Hop, error) {
	infoRep, err := c.client.Info(ctx, &connect.Request[hummingbird.MarketplaceInfoRequest]{})
	if err != nil {
		return nil, err
	}
	combineCost := uint64(infoRep.Msg.SplitCombineFeeAbsolute)
	startsAt = startsAt.Truncate(time.Second)
	stopsAt = stopsAt.Truncate(time.Second)
	hops := make([]*snetpath.Hop, len(pairs))
	if fetchReservations {
		hops, err = c.findExistingReservations(ctx, pairs, bwInKbps, startsAt, stopsAt)
		if err != nil {
			return nil, err
		}
	}
	var orders = []*hummingbird.BuyAsset{}
	iaAssets := map[uint64][]assetInfo{}
	var boughtAssets []*hummingbird.BoughtAsset
	success := false
	for retry := -1; retry < num_retries; retry++ {
		orders = []*hummingbird.BuyAsset{}
		for i, pair := range pairs {
			if hops[i] != nil {
				continue
			}
			if combineAssets {
				o, err := c.checkoutAssetForInterfacePairWithCombine(
					ctx, pair, bwInKbps, startsAt, stopsAt, combineCost)
				if err != nil {
					if buyMode == FailOnError {
						return nil, err
					} else if buyMode == ContinueOnError {
						continue
					}
				}
				orders = append(orders, o...)
			} else {
				o, err := c.checkoutAssetForInterfacePair(ctx, pair, bwInKbps, startsAt, stopsAt)
				if err != nil {
					if buyMode == FailOnError {
						return nil, err
					} else if buyMode == ContinueOnError {
						continue
					}
				}
				orders = append(orders, o...)
			}
		}
		if len(orders) == 0 {
			log.FromCtx(ctx).Debug("Skip purchase since no assets are selected for purchasing")
		} else {
			log.FromCtx(ctx).Debug("Purchase", "assets", orders)
			boughtAssetsResponse, err :=
				c.client.BuyAssets(ctx, &connect.Request[hummingbird.BuyAssetsRequest]{
					Msg: &hummingbird.BuyAssetsRequest{
						Assets:   orders,
						MaxPrice: maxPrice,
					},
				})
			if err != nil {
				log.FromCtx(ctx).Debug("Error buying asset", "err", err)
				continue
			}
			boughtAssets = boughtAssetsResponse.Msg.Assets
		}
		success = true
		break
	}
	if !success {
		return nil, err
	}
	// Look up the assets that were just bought, to learn where and when they apply.
	// If combineAssets == True, we need to find assets that "touch" the time window.
	// If not, we need to find assets which each one of them covers the time window.
	var foundAssets []*hummingbird.SearchAsset
	if combineAssets {
		foundAssetsResp, err :=
			c.client.SearchAssets(ctx, &connect.Request[hummingbird.SearchAssetsRequest]{
				Msg: &hummingbird.SearchAssetsRequest{
					Owned:           true,
					MinRequiredBw:   &bwInKbps,
					StartsAtLatest:  timestamppb.New(stopsAt),
					StopsAtEarliest: timestamppb.New(startsAt),
				},
			})
		if err != nil {
			return nil, err
		}
		foundAssets = foundAssetsResp.Msg.Assets
	} else {
		foundAssetsResp, err :=
			c.client.SearchAssets(ctx, &connect.Request[hummingbird.SearchAssetsRequest]{
				Msg: &hummingbird.SearchAssetsRequest{
					Owned:           true,
					MinRequiredBw:   &bwInKbps,
					StartsAtLatest:  timestamppb.New(startsAt),
					StopsAtEarliest: timestamppb.New(stopsAt),
				},
			})
		if err != nil {
			return nil, err
		}
		foundAssets = foundAssetsResp.Msg.Assets
	}

	foundAssetsMap := make(map[uint64][]*hummingbird.SearchAsset)
	for _, boughtAsset := range boughtAssets {
		for _, foundAsset := range foundAssets {
			if bytes.Equal(boughtAsset.AssetId, foundAsset.AssetId) {
				currentSlice, found := foundAssetsMap[foundAsset.Ia]
				if found {
					foundAssetsMap[foundAsset.Ia] = append(currentSlice, foundAsset)
				} else {
					foundAssetsMap[foundAsset.Ia] = []*hummingbird.SearchAsset{foundAsset}
				}
				break
			}
		}
	}
	if combineAssets {
		for k, v := range foundAssetsMap {
			ingressAssets := []*hummingbird.SearchAsset{}
			egressAssets := []*hummingbird.SearchAsset{}
			pairAssets := []*hummingbird.SearchAsset{}
			for _, asset := range v {
				if asset.IfIdIngress != nil {
					if asset.IfIdEgress != nil {
						pairAssets = append(pairAssets, asset)
					} else {
						ingressAssets = append(ingressAssets, asset)
					}
				} else {
					egressAssets = append(egressAssets, asset)
				}
			}
			if len(pairAssets) != 0 {
				sort.Slice(pairAssets, func(i, j int) bool {
					return pairAssets[i].StartsAt.Seconds < pairAssets[j].StartsAt.Seconds
				})
				currAssetId := pairAssets[0].AssetId
				for i := 0; i < len(pairAssets)-1; i++ {
					combineResp, err := c.client.CombineAssets(
						ctx, &connect.Request[hummingbird.CombineAssetRequest]{
							Msg: &hummingbird.CombineAssetRequest{
								AssetIds: [][]byte{
									currAssetId,
									pairAssets[i+1].AssetId,
								},
							},
						})
					if err != nil {
						return nil, err
					}
					currAssetId = combineResp.Msg.AssetId
				}
				iaAssets[k] = []assetInfo{{
					id:       currAssetId,
					startsAt: pairAssets[0].StartsAt.AsTime(),
					stopsAt:  pairAssets[len(pairAssets)-1].StopsAt.AsTime()}}
			}
			if len(ingressAssets) != 0 && len(ingressAssets) == len(egressAssets) {
				sort.Slice(ingressAssets, func(i, j int) bool {
					return ingressAssets[i].StartsAt.Seconds < ingressAssets[j].StartsAt.Seconds
				})
				currIngressAssetId := ingressAssets[0].AssetId
				for i := 0; i < len(ingressAssets)-1; i++ {
					combineResp, err := c.client.CombineAssets(ctx, &connect.Request[hummingbird.CombineAssetRequest]{
						Msg: &hummingbird.CombineAssetRequest{
							AssetIds: [][]byte{
								currIngressAssetId,
								ingressAssets[i+1].AssetId,
							},
						},
					})
					if err != nil {
						return nil, err
					}
					currIngressAssetId = combineResp.Msg.AssetId
				}
				sort.Slice(egressAssets, func(i, j int) bool {
					return egressAssets[i].StartsAt.Seconds < egressAssets[j].StartsAt.Seconds
				})
				currEgressAssetId := egressAssets[0].AssetId
				for i := 0; i < len(egressAssets)-1; i++ {
					combineResp, err := c.client.CombineAssets(ctx, &connect.Request[hummingbird.CombineAssetRequest]{
						Msg: &hummingbird.CombineAssetRequest{
							AssetIds: [][]byte{
								currEgressAssetId,
								egressAssets[i+1].AssetId,
							},
						},
					})
					if err != nil {
						return nil, err
					}
					currEgressAssetId = combineResp.Msg.AssetId
				}
				iaAssets[k] = []assetInfo{
					{
						id:       currIngressAssetId,
						startsAt: ingressAssets[0].StartsAt.AsTime(),
						stopsAt:  ingressAssets[len(ingressAssets)-1].StopsAt.AsTime(),
					},
					{
						id:       currEgressAssetId,
						startsAt: egressAssets[0].StartsAt.AsTime(),
						stopsAt:  egressAssets[len(egressAssets)-1].StopsAt.AsTime(),
					},
				}
			}
		}
	} else {
		for _, v := range foundAssetsMap {
			for _, asset := range v {
				current, found := iaAssets[asset.Ia]
				if found {
					iaAssets[asset.Ia] = append(current, assetInfo{
						id:       asset.AssetId,
						stopsAt:  asset.StopsAt.AsTime(),
						startsAt: asset.StartsAt.AsTime()})
				} else {
					iaAssets[asset.Ia] = []assetInfo{{
						id:       asset.AssetId,
						stopsAt:  asset.StopsAt.AsTime(),
						startsAt: asset.StartsAt.AsTime()}}
				}
			}
		}
	}
	redeemedHops, err := c.redeemHopsConcurrently(ctx, pairs, iaAssets, startsAt, stopsAt)
	if err != nil {
		return nil, err
	}
	for i, hop := range hops {
		if hop != nil {
			redeemedHops[i] = hops[i]
		}
	}
	return redeemedHops, nil
}

func later(a time.Time, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}
func earlier(a time.Time, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (c *MarketplaceClient) redeemHopsConcurrently(
	ctx context.Context,
	pairs []InterfacePair,
	iaAssets map[uint64][]assetInfo,
	startsAt time.Time,
	stopsAt time.Time,
) ([]*snetpath.Hop, error) {
	type requestInfo struct {
		request  *hummingbird.RedeemAssetRequest
		startsAt time.Time
		stopsAt  time.Time
	}
	requests := make([]*requestInfo, len(pairs))
	responses := make([]*hummingbird.RedeemAssetResponse, len(pairs))
	errors := make([]error, len(pairs))
	for i, pair := range pairs {
		assets, found := iaAssets[pair.IA]
		if !found || len(assets) == 0 {
			continue
		}
		if len(assets) == 1 {
			requests[i] = &requestInfo{
				request: &hummingbird.RedeemAssetRequest{
					Interfaces: &hummingbird.RedeemAssetRequest_IfPairAssetId{
						IfPairAssetId: assets[0].id,
					},
				},
				startsAt: assets[0].startsAt,
				stopsAt:  assets[0].stopsAt,
			}
		} else if len(assets) == 2 {
			requests[i] = &requestInfo{
				request: &hummingbird.RedeemAssetRequest{
					Interfaces: &hummingbird.RedeemAssetRequest_Pair{
						Pair: &hummingbird.IngressEgressPair{
							IngressAssetId: assets[0].id,
							EgressAssetId:  assets[1].id,
						},
					},
				},
				startsAt: later(assets[0].startsAt, assets[1].startsAt),
				stopsAt:  earlier(assets[0].stopsAt, assets[1].stopsAt),
			}
		}
	}
	wg := sync.WaitGroup{}
	wg.Add(len(pairs))
	for i := range pairs {
		go func(i int) {
			defer wg.Done()
			if requests[i] == nil {
				return
			}
			rep, err := c.client.RedeemAsset(
				ctx, &connect.Request[hummingbird.RedeemAssetRequest]{Msg: requests[i].request})
			if err != nil {
				errors[i] = err
				return
			}
			responses[i] = rep.Msg
		}(i)
	}
	wg.Wait()
	ret := make([]*snetpath.Hop, len(pairs))
	for i := 0; i < len(pairs); i++ {
		if responses[i] != nil {
			ret[i] = &snetpath.Hop{
				BaseHop: snetpath.BaseHop{
					IA:      addr.IA(pairs[i].IA),
					Ingress: uint16(pairs[i].Ingress),
					Egress:  uint16(pairs[i].Egress),
				},
				Flyover: &snetpath.FlyoverData{
					ResID:     responses[i].ReservationId,
					Ak:        [16]byte(responses[i].AuthenticationKey),
					Bw:        uint16(responses[i].BwDataplaneEncoding),
					StartTime: uint32(requests[i].startsAt.Unix()),
					Duration:  uint16(requests[i].stopsAt.Sub(requests[i].startsAt) / time.Second),
				},
			}
		} else if errors[i] != nil {
			log.FromCtx(ctx).Debug("error redeeming asset",
				"asset ID", requests[i].request.Interfaces,
				"err", errors[i])
			ret[i] = &snetpath.Hop{
				BaseHop: snetpath.BaseHop{
					IA:      addr.IA(pairs[i].IA),
					Ingress: uint16(pairs[i].Ingress),
					Egress:  uint16(pairs[i].Egress),
				},
			}
		} else {
			ret[i] = &snetpath.Hop{
				BaseHop: snetpath.BaseHop{
					IA:      addr.IA(pairs[i].IA),
					Ingress: uint16(pairs[i].Ingress),
					Egress:  uint16(pairs[i].Egress),
				},
			}
		}
	}
	return ret, nil
}

// ObtainReservationsFullPath tries to obtain reservations for the full path provided.
// `buyMode` defines whether it should try to continue searching and buying assets in other ASes
// if purchase of an asset for one AS fails (e.g. because no asset matching the filters exists).
// `fetchReservations` enables fetching already owned reservations.
// If reservations that match IA, ingress, egress, bwInKbps, startsAt and stopsAt are found,
// those are used instead of buying new ones.
//
// It could happen that an asset gets bought by some other user between
// searching and buying the asset ourselves.
// With `num_retries` we can repeat the search and buy step if this happens.
func (c *MarketplaceClient) ObtainReservationsFullPath(
	ctx context.Context,
	path snet.Path,
	bwInKbps uint32,
	startsAt time.Time,
	stopsAt time.Time,
	maxPrice uint64,
	buyMode BuyMode,
	fetchReservations bool,
	combineAssets bool,
	num_retries int,
) ([]*snetpath.Hop, error) {

	pairs := interfacePairsFromInterfaces(path.Metadata().Interfaces)
	return c.ObtainReservationsForInterfacePairs(ctx, pairs, bwInKbps, startsAt, stopsAt,
		maxPrice, buyMode, fetchReservations, combineAssets, num_retries)
}
