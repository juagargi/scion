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
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

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

func (c *Client) findExistingReservations(
	ctx context.Context,
	pairs []InterfacePair,
	bwInKbps uint32,
	startsAt int64,
	stopsAt int64,
) ([]*snetpath.Hop, error) {
	req := &hummingbird.FetchReservationsRequest{
		Bandwidth: &bwInKbps,
		StartsAt:  &startsAt,
		StopsAt:   &stopsAt,
	}
	res, err := c.Marketplace.FetchReservations(
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
						StartTime: uint32(r.StartsAt),
						Duration:  uint16(r.StopsAt - r.StartsAt),
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

type UnwrappedAsset struct {
	AssetId         []byte
	Ia              uint64
	IfIdIngress     *uint32
	IfIdEgress      *uint32
	BandwidthMin    uint32
	BandwidthMax    uint32
	TimeGranularity uint32
	TimeMinDuration uint32
	TimeMaxDuration uint32
	Price           uint32
	StartsAt        int64
	StopsAt         int64
	Bandwidth       uint32
}

func (a *UnwrappedAsset) UniqueID() string {
	return strconv.Itoa(len(a.AssetId)) + string(a.AssetId) + strconv.Itoa(int(a.StartsAt))
}

func activeAt(
	elements []*UnwrappedAsset,
	t int64,
	bw uint32,
) []*UnwrappedAsset {
	// Find first element with StartsAt > t
	end := sort.Search(len(elements), func(i int) bool {
		return elements[i].StartsAt > t
	})

	result := make([]*UnwrappedAsset, 0)

	for i := 0; i < end; i++ {
		if t < (elements[i].StopsAt) && elements[i].Bandwidth >= bw {
			result = append(result, elements[i])
		}
	}
	return result
}

// recursiveSelect
func (c *Client) recursiveSelectStart(
	assets []*UnwrappedAsset,
	bwInKbps uint32,
	startsAt int64,
	stopsAt int64,
	combineCost uint64,
) ([]*hummingbird.BuyAsset, uint64, bool) {
	sort.Slice(assets, func(i, j int) bool {
		return assets[i].StartsAt < assets[j].StartsAt
	})

	assetMap := make(map[string]*UnwrappedAsset)
	for _, asset := range assets {
		assetMap[asset.UniqueID()] = asset
	}
	filterValidAt := func(
		t int64,
		assets []*UnwrappedAsset,
	) []*UnwrappedAsset {
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
	actualPrice := func(a *UnwrappedAsset, durationInSeconds int64) uint64 {
		price, err := ReservationPrice(
			a.Price,
			bwInKbps,
			a.BandwidthMin,
			a.TimeMinDuration,
			a.TimeGranularity,
			time.Duration(durationInSeconds)*time.Second)
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
			currCost += actualPrice(asset, chain.stopsAt-startsAt)
			currSelect = append(currSelect, &hummingbird.BuyAsset{
				AssetId:         asset.AssetId,
				StartsAtExactly: startsAt,
				StopsAtExactly:  chain.stopsAt,
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
					duration := min(currAsset.StopsAt, chain.stopsAt) - currStartsAt
					currCost += actualPrice(currAsset, duration)

					currSelect = append(currSelect, &hummingbird.BuyAsset{
						AssetId:         currAsset.AssetId,
						StartsAtExactly: currStartsAt,
						StopsAtExactly:  min(currAsset.StopsAt, chain.stopsAt),
						BandwidthExact:  chain.bandwidthMin,
					})
					currStartsAt = min(currAsset.StopsAt, chain.stopsAt)

				} else {
					duration := min(nextAsset.StartsAt, chain.stopsAt) - currStartsAt
					currCost += actualPrice(currAsset, duration)
					currSelect = append(currSelect, &hummingbird.BuyAsset{
						AssetId:         currAsset.AssetId,
						StartsAtExactly: currStartsAt,
						StopsAtExactly:  min(nextAsset.StartsAt, chain.stopsAt),
						BandwidthExact:  chain.bandwidthMin,
					})
					currStartsAt = min(nextAsset.StartsAt, chain.stopsAt)
				}
			}
			lastAsset := assetMap[string(chain.ids[i])]
			duration := min(lastAsset.StopsAt, chain.stopsAt) - (currStartsAt)
			currCost += actualPrice(lastAsset, duration)

			currSelect = append(currSelect, &hummingbird.BuyAsset{
				AssetId:         lastAsset.AssetId,
				StartsAtExactly: currStartsAt,
				StopsAtExactly:  min(lastAsset.StopsAt, chain.stopsAt),
				BandwidthExact:  chain.bandwidthMin,
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
	ids             []string
	timeGranularity uint32
	timeMinDuration uint32
	bandwidthMin    uint32
	stopsAt         int64
}

func (c *Client) recursiveSelect(
	currentAsset *UnwrappedAsset,
	otherAssets []*UnwrappedAsset,
	previousBw uint32,
	previousTimeMinDuration uint32,
	previousTimeGranularity uint32,
	globalStart int64,
	globalStop int64,
) ([]chain, bool) {
	currBW := max(previousBw, currentAsset.BandwidthMin)
	timeGranularity := lcm(previousTimeGranularity, currentAsset.TimeGranularity)
	timeMinDuration := max(previousTimeMinDuration, currentAsset.TimeMinDuration)
	filterValidAt := func(
		t int64,
		assets []*UnwrappedAsset,
	) []*UnwrappedAsset {
		return activeAt(assets, t, currBW)
	}
	remainder := uint32(globalStop-globalStart) % timeGranularity
	if remainder != 0 {
		addedDuration := timeGranularity - remainder
		globalStop = globalStop + int64(addedDuration)
	}
	if uint32(globalStop-globalStart) < timeMinDuration {
		globalStop = globalStart + int64(timeMinDuration)
	}
	if currentAsset.StopsAt >= globalStop {
		// with this asset we found a chain from start till end
		return []chain{{
			ids:             []string{currentAsset.UniqueID()},
			bandwidthMin:    currBW,
			timeGranularity: timeGranularity,
			timeMinDuration: timeMinDuration,
			stopsAt:         globalStop}}, true
	}
	nextAssets := filterValidAt(currentAsset.StopsAt, otherAssets)
	if len(nextAssets) == 0 {
		fmt.Println("no assets found to be valid at ", currentAsset.StopsAt)
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
			resultChain.ids = append([]string{currentAsset.UniqueID()}, resultChain.ids...)
			allChains = append(allChains, resultChain)
		}
	}
	return allChains, len(allChains) != 0

}

func (c *Client) searchAllAssets(ctx context.Context, owned bool, ia *uint64, ingress *uint32, egress *uint32,
	minBW uint32, startsAtLatest int64, stopsAtLatest int64, partialOverlap bool) ([]*UnwrappedAsset, error) {
	validityMode := hummingbird.ValidityMode_full_overlap
	if partialOverlap {
		validityMode = hummingbird.ValidityMode_partial_overlap
	}
	var resp *connect.Response[hummingbird.SearchAssetsResponse]
	var err error
	if owned {
		resp, err = c.Marketplace.SearchAssets(ctx, &connect.Request[hummingbird.SearchAssetsRequest]{
			Msg: &hummingbird.SearchAssetsRequest{
				Request: &hummingbird.SearchAssetsRequest_Owned{
					Owned: &hummingbird.SearchOwnedAssetsRequest{
						Ia:              ia,
						IfIdIngress:     ingress,
						IfIdEgress:      egress,
						MinRequiredBw:   &minBW,
						StartsAtLatest:  &startsAtLatest,
						StopsAtEarliest: &stopsAtLatest,
					},
				},
				ValidityMode: validityMode,
			},
		})
	} else {
		resp, err = c.Marketplace.SearchAssets(ctx, &connect.Request[hummingbird.SearchAssetsRequest]{
			Msg: &hummingbird.SearchAssetsRequest{
				Request: &hummingbird.SearchAssetsRequest_Listed{
					Listed: &hummingbird.SearchListedAssetsRequest{
						Ia:              *ia,
						IfIdIngress:     ingress,
						IfIdEgress:      egress,
						MinRequiredBw:   minBW,
						StartsAtLatest:  startsAtLatest,
						StopsAtEarliest: stopsAtLatest,
					},
				},
				ValidityMode: validityMode,
			},
		})
	}
	assets := []*UnwrappedAsset{}
	for _, asset := range resp.Msg.Assets {
		for _, interval := range asset.Intervals {
			assets = append(assets, &UnwrappedAsset{
				AssetId:         asset.AssetId,
				Ia:              asset.Ia,
				IfIdIngress:     asset.IfIdIngress,
				IfIdEgress:      asset.IfIdEgress,
				BandwidthMin:    asset.BandwidthMin,
				BandwidthMax:    asset.BandwidthMax,
				TimeGranularity: asset.TimeGranularity,
				TimeMinDuration: asset.TimeMinDuration,
				TimeMaxDuration: asset.TimeMaxDuration,
				Price:           asset.Price,
				StartsAt:        interval.StartsAt,
				StopsAt:         interval.StopsAt,
				Bandwidth:       interval.Bandwidth,
			})
		}
	}
	return assets, err
}

// checkoutAssetForInterfacePairWithCombine tries to find assets that can be combined on the time axis. It does not check for combinations on the bandwidth axis.
func (c *Client) checkoutAssetForInterfacePairWithCombine(ctx context.Context, pair InterfacePair, bwInKbps uint32,
	startsAt int64, stopsAt int64, combineCost uint64) ([]*hummingbird.BuyAsset, error) {

	ingressAndEgressSuccess := false
	var ingressBuyAssets, egressBuyAssets, pairBuyAssets []*hummingbird.BuyAsset
	var ingressAssetPrice, egressAssetPrice, pairAssetPrice uint64
	var ok bool
	ingressAssetsResponse, err := c.searchAllAssets(ctx, false, &pair.IA, &pair.Ingress, nil, bwInKbps, startsAt, stopsAt, true)
	if err != nil {
		return nil, err
	}
	ingressSearchAssets := filter(ingressAssetsResponse, func(a *UnwrappedAsset) bool {
		return a.IfIdEgress == nil
	})
	ingressBuyAssets, ingressAssetPrice, ok = c.recursiveSelectStart(ingressSearchAssets, bwInKbps, startsAt, stopsAt, combineCost)
	if ok {
		egressAssetsResponse, err := c.searchAllAssets(ctx, false, &pair.IA, nil, &pair.Egress, bwInKbps, startsAt, stopsAt, true)
		if err != nil {
			return nil, err
		}
		egressSearchAssets := filter(egressAssetsResponse, func(a *UnwrappedAsset) bool {
			return a.IfIdIngress == nil
		})
		egressBuyAssets, egressAssetPrice, ok = c.recursiveSelectStart(egressSearchAssets, bwInKbps, startsAt, stopsAt, combineCost)
		if ok {
			ingressAndEgressSuccess = true
		}
	}
	pairAssetsResponse, err := c.searchAllAssets(ctx, false, &pair.IA, &pair.Ingress, &pair.Egress, bwInKbps, startsAt, stopsAt, true)
	if err != nil {
		return nil, err
	}
	pairBuyAssets, pairAssetPrice, ok = c.recursiveSelectStart(pairAssetsResponse, bwInKbps, startsAt, stopsAt, combineCost)
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
func (c *Client) checkoutAssetForInterfacePair(
	ctx context.Context,
	pair InterfacePair,
	bwInKbps uint32,
	startsAt int64,
	stopsAt int64,
) ([]*hummingbird.BuyAsset, error) {
	var ingressSearchAssets, egressSearchAssets, pairSearchAssets []*UnwrappedAsset
	ingressAssetsResponse, err := c.searchAllAssets(ctx, false, &pair.IA, &pair.Ingress, nil, bwInKbps, startsAt, stopsAt, false)
	if err != nil {
		return nil, err
	}
	ingressSearchAssets = filter(ingressAssetsResponse, func(a *UnwrappedAsset) bool {
		return a.IfIdEgress == nil
	})
	if len(ingressSearchAssets) != 0 {
		egressAssetsResponse, err := c.searchAllAssets(ctx, false, &pair.IA, nil, &pair.Ingress, bwInKbps, startsAt, stopsAt, false)
		if err != nil {
			return nil, err
		}
		egressSearchAssets = filter(egressAssetsResponse, func(a *UnwrappedAsset) bool {
			return a.IfIdIngress == nil
		})
	}
	pairSearchAssets, err = c.searchAllAssets(ctx, false, &pair.IA, &pair.Ingress, &pair.Egress, bwInKbps, startsAt, stopsAt, false)
	if err != nil {
		return nil, err
	}

	duration := time.Duration(stopsAt-startsAt) * time.Second
	actualPrice := func(a *UnwrappedAsset) uint64 {
		price, err := ReservationPrice(
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
	filterAssets := func(assets []*UnwrappedAsset) []*UnwrappedAsset {
		return filter(assets, func(a *UnwrappedAsset) bool {
			billableDuration, err :=
				ReservationDuration(duration, a.TimeMinDuration, a.TimeGranularity)
			if err != nil || a.StopsAt < (startsAt+int64(billableDuration/time.Second)) {
				return false
			}
			return a.Bandwidth >= max(bwInKbps, a.BandwidthMin)
		})
	}
	sortAssets := func(assets []*UnwrappedAsset) []*UnwrappedAsset {
		sort.Slice(assets, func(i, j int) bool {
			return actualPrice(assets[i]) < actualPrice(assets[j])
		})
		return assets
	}
	buildBuyRequest := func(a *UnwrappedAsset) *hummingbird.BuyAsset {
		billableDuration, _ :=
			ReservationDuration(duration, a.TimeMinDuration, a.TimeGranularity)
		return &hummingbird.BuyAsset{
			AssetId:         a.AssetId,
			BandwidthExact:  max(bwInKbps, a.BandwidthMin),
			StartsAtExactly: startsAt,
			StopsAtExactly:  startsAt + int64(billableDuration/time.Second),
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
	startsAt int64
	stopsAt  int64
}

func (c *Client) ObtainReservationsForInterfacePairs(
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
	infoRep, err := c.Marketplace.Info(ctx, &connect.Request[hummingbird.MarketplaceInfoRequest]{})
	if err != nil {
		return nil, err
	}
	startsAtUnix := startsAt.Unix()
	stopsAtUnix := stopsAt.Unix()
	combineCost := uint64(infoRep.Msg.SplitCombineFeeAbsolute)
	startsAt = startsAt.Truncate(time.Second)
	stopsAt = stopsAt.Truncate(time.Second)
	hops := make([]*snetpath.Hop, len(pairs))
	if fetchReservations {
		hops, err = c.findExistingReservations(ctx, pairs, bwInKbps, startsAtUnix, stopsAtUnix)
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
					ctx, pair, bwInKbps, startsAtUnix, stopsAtUnix, combineCost)
				if err != nil {
					if buyMode == FailOnError {
						return nil, err
					} else if buyMode == ContinueOnError {
						continue
					}
				}
				orders = append(orders, o...)
			} else {
				o, err := c.checkoutAssetForInterfacePair(ctx, pair, bwInKbps, startsAtUnix, stopsAtUnix)
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
				c.Marketplace.BuyAssets(ctx, &connect.Request[hummingbird.BuyAssetsRequest]{
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
	foundAssets, err := c.searchAllAssets(ctx, true, nil, nil, nil, bwInKbps, startsAtUnix, stopsAtUnix, combineAssets)
	if err != nil {
		return nil, err
	}

	foundAssetsMap := make(map[uint64][]*UnwrappedAsset)
	for _, boughtAsset := range boughtAssets {
		for _, foundAsset := range foundAssets {
			if bytes.Equal(boughtAsset.AssetId, foundAsset.AssetId) {
				currentSlice, found := foundAssetsMap[foundAsset.Ia]
				if found {
					foundAssetsMap[foundAsset.Ia] = append(currentSlice, foundAsset)
				} else {
					foundAssetsMap[foundAsset.Ia] = []*UnwrappedAsset{foundAsset}
				}
				break
			}
		}
	}
	if combineAssets {
		for k, v := range foundAssetsMap {
			ingressAssets := []*UnwrappedAsset{}
			egressAssets := []*UnwrappedAsset{}
			pairAssets := []*UnwrappedAsset{}
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
					return pairAssets[i].StartsAt < pairAssets[j].StartsAt
				})
				currAssetId := pairAssets[0].AssetId
				for i := 0; i < len(pairAssets)-1; i++ {
					combineResp, err := c.Marketplace.CombineAssets(
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
					startsAt: pairAssets[0].StartsAt,
					stopsAt:  pairAssets[len(pairAssets)-1].StopsAt}}
			}
			if len(ingressAssets) != 0 && len(ingressAssets) == len(egressAssets) {
				sort.Slice(ingressAssets, func(i, j int) bool {
					return ingressAssets[i].StartsAt < ingressAssets[j].StartsAt
				})
				currIngressAssetId := ingressAssets[0].AssetId
				for i := 0; i < len(ingressAssets)-1; i++ {
					combineResp, err := c.Marketplace.CombineAssets(ctx, &connect.Request[hummingbird.CombineAssetRequest]{
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
					return egressAssets[i].StartsAt < egressAssets[j].StartsAt
				})
				currEgressAssetId := egressAssets[0].AssetId
				for i := 0; i < len(egressAssets)-1; i++ {
					combineResp, err := c.Marketplace.CombineAssets(ctx, &connect.Request[hummingbird.CombineAssetRequest]{
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
						startsAt: ingressAssets[0].StartsAt,
						stopsAt:  ingressAssets[len(ingressAssets)-1].StopsAt,
					},
					{
						id:       currEgressAssetId,
						startsAt: egressAssets[0].StartsAt,
						stopsAt:  egressAssets[len(egressAssets)-1].StopsAt,
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
						stopsAt:  asset.StopsAt,
						startsAt: asset.StartsAt})
				} else {
					iaAssets[asset.Ia] = []assetInfo{{
						id:       asset.AssetId,
						stopsAt:  asset.StopsAt,
						startsAt: asset.StartsAt}}
				}
			}
		}
	}
	redeemedHops, err := c.redeemHopsConcurrently(ctx, pairs, iaAssets, startsAtUnix, stopsAtUnix)
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

func (c *Client) redeemHopsConcurrently(
	ctx context.Context,
	pairs []InterfacePair,
	iaAssets map[uint64][]assetInfo,
	startsAt int64,
	stopsAt int64,
) ([]*snetpath.Hop, error) {
	type requestInfo struct {
		request  *hummingbird.RedeemAssetRequest
		startsAt int64
		stopsAt  int64
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
				startsAt: max(assets[0].startsAt, assets[1].startsAt),
				stopsAt:  min(assets[0].stopsAt, assets[1].stopsAt),
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
			rep, err := c.Marketplace.RedeemAsset(
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
					StartTime: uint32(requests[i].startsAt),
					Duration:  uint16(requests[i].stopsAt - requests[i].startsAt),
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
func (c *Client) ObtainReservationsFullPath(
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
