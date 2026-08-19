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
	"database/sql"
	"encoding/binary"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/marketplace/storage"
	"github.com/scionproto/scion/pkg/addr"
	hbird "github.com/scionproto/scion/pkg/hummingbird"
	"github.com/scionproto/scion/pkg/hummingbird/registration"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service struct {
	redemptionServerPeers map[addr.IA]*RedemptionServerPeer
	mtx                   sync.Mutex
	info                  *MarketplaceInfo
	store                 *storage.MarketplaceStorage
	registrationService   *registration.Service
	signer                *registration.Signer
}

type MarketplaceInfo struct {
	ApiMajorVersion              uint32
	ApiMinorVersion              uint32
	Currency                     string
	StatisticsTimeGranularity    uint32
	SupportsRedemptionDelegation bool
	CurrencyExponent             uint32
	PricingStrategy              hummingbird.PricingStrategy
	TransactionFeeRelative       float32
	TransactionFeeAbsolute       uint64
	SplitCombineFeeAbsolute      uint64
	DelegationHourlyFee          uint64
	MaxReturnedAssets            uint32
	AssetValidityMax             uint32
}

func databaseAssetID(id []byte) (int64, error) {
	idInt := binary.BigEndian.Uint64(id)
	return db.AssetID(idInt).Int64()
}
func protoAssetID(assetId int64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(assetId))
	return buf
}

func NewService(ctx context.Context, info *MarketplaceInfo, store *storage.MarketplaceStorage, regService *registration.Service, signer *registration.Signer) (*Service, error) {
	s := &Service{
		redemptionServerPeers: make(map[addr.IA]*RedemptionServerPeer),
		info:                  info,
		store:                 store,
		registrationService:   regService,
		signer:                signer,
	}
	if s.info.SupportsRedemptionDelegation {
		d, err := store.FindRedemptionDelegations(ctx)
		if err != nil {
			return nil, err
		}
		for _, delegation := range d {
			delegation.EncodingsToInts()
			peer := s.newRedemptionServerPeer(delegation.IA)
			err = s.startOrUpdateRedemptionDelegation(ctx, peer.ia, &RedemptionDelegationUpdate{
				ExpirationTime: delegation.Expiration,
				IdLimitLow:     delegation.ResIdLow,
				IdLimitHigh:    delegation.ResIdHigh,
				Key:            delegation.Key,
				EncodingPoints: delegation.EncodingsToInts(),
			}, false)
			if err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

func (s *Service) CombineAssets(ctx context.Context, req *connect.Request[hummingbird.CombineAssetRequest]) (*connect.Response[hummingbird.CombineAssetResponse], error) {
	user_id, ok := ctx.Value("user").(int64)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, serrors.New("user_id not provided"))
	}
	if len(req.Msg.AssetIds) < 2 {
		return nil, connect.NewError(connect.CodeInvalidArgument, serrors.New("at least 2 asset IDs are required when combining assets"))
	}
	assetIds := make([]int64, 0, len(req.Msg.AssetIds))
	for _, id := range req.Msg.AssetIds {
		assetId, err := databaseAssetID(id)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		assetIds = append(assetIds, assetId)
	}
	combinedId, err := s.store.CombineAssets(ctx, user_id, assetIds)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return &connect.Response[hummingbird.CombineAssetResponse]{
		Msg: &hummingbird.CombineAssetResponse{
			AssetId: protoAssetID(combinedId),
		},
	}, nil
}

func (s *Service) SplitAsset(ctx context.Context, req *connect.Request[hummingbird.SplitAssetRequest]) (*connect.Response[hummingbird.SplitAssetResponse], error) {
	userId, ok := ctx.Value("user").(int64)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, serrors.New("user_id not provided"))
	}
	assetId, err := databaseAssetID(req.Msg.AssetId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	var ids []int64
	switch req.Msg.SplitOption.(type) {
	case *hummingbird.SplitAssetRequest_BwSplit:
		bwSplit := req.Msg.GetBwSplit()
		ids, err = s.store.SplitAsset(ctx, userId, assetId, bwSplit.Splits, nil)
	case *hummingbird.SplitAssetRequest_TimeSplit:
		splits := req.Msg.GetTimeSplit().Splits
		times := make([]time.Time, 0, len(splits))
		for _, split := range splits {
			times = append(times, split.AsTime())
		}
		ids, err = s.store.SplitAsset(ctx, userId, assetId, nil, times)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, serrors.New("invalid split request"))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	protoAssetIDs := make([][]byte, 0, len(ids))
	for _, id := range ids {
		protoAssetIDs = append(protoAssetIDs, protoAssetID(id))
	}
	return &connect.Response[hummingbird.SplitAssetResponse]{
		Msg: &hummingbird.SplitAssetResponse{
			AssetIds: protoAssetIDs,
		},
	}, nil
}

func (s *Service) BuyAssets(ctx context.Context, req *connect.Request[hummingbird.BuyAssetsRequest]) (*connect.Response[hummingbird.BuyAssetsResponse], error) {
	user, ok := ctx.Value("user").(int64)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, serrors.New("user_id not provided"))
	}
	boughtAssetIDs, totalCost, err := s.store.BuyAssets(ctx, user, req.Msg.Assets, req.Msg.MaxPrice)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	boughtAssets := make([]*hummingbird.BoughtAsset, 0, len(boughtAssetIDs))
	for _, a := range boughtAssetIDs {
		boughtAssets = append(boughtAssets, &hummingbird.BoughtAsset{
			AssetId: protoAssetID(a),
		})
	}
	return &connect.Response[hummingbird.BuyAssetsResponse]{
		Msg: &hummingbird.BuyAssetsResponse{
			Assets: boughtAssets,
			Cost:   uint64(totalCost),
		},
	}, nil
}

func (s *Service) FetchReservations(ctx context.Context, req *connect.Request[hummingbird.FetchReservationsRequest]) (*connect.Response[hummingbird.FetchReservationsResponse], error) {
	user, ok := ctx.Value("user").(int64)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, serrors.New("user_id not provided"))
	}
	var startsAt *string
	var stopsAt *string
	if req.Msg.StartsAt != nil {
		start := req.Msg.StartsAt.AsTime().UTC().Format(time.RFC3339)
		startsAt = &start
	}
	if req.Msg.StopsAt != nil {
		stop := req.Msg.StopsAt.AsTime().UTC().Format(time.RFC3339)
		stopsAt = &stop
	}
	var ia *addr.IA
	if req.Msg.Ia != nil {
		tmp := addr.IA(*req.Msg.Ia)
		ia = &tmp
	}
	reservations, err := s.store.FetchReservations(ctx, &db.ReservationQuery{
		IA:        ia,
		Ingress:   req.Msg.IngressId,
		Egress:    req.Msg.EgressId,
		StartsAt:  startsAt,
		StopsAt:   stopsAt,
		Bandwidth: req.Msg.Bandwidth,
		AccountId: user,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	resp := make([]*hummingbird.Reservation, 0, len(reservations))
	for _, reservation := range reservations {
		resp = append(resp, &hummingbird.Reservation{
			ReservationId:     reservation.ReservationID,
			Ia:                uint64(reservation.IA),
			IngressId:         reservation.Ingress,
			EgressId:          reservation.Egress,
			Bandwidth:         reservation.Bandwidth,
			DataplaneEncoding: uint32(reservation.EncodedBandwidth),
			StartsAt:          timestamppb.New(reservation.StartsAt),
			StopsAt:           timestamppb.New(reservation.StopsAt),
			AuthenticationKey: reservation.Key,
		})
	}
	return &connect.Response[hummingbird.FetchReservationsResponse]{
		Msg: &hummingbird.FetchReservationsResponse{
			Reservations: resp,
		},
	}, nil
}

func (s *Service) Info(context.Context, *connect.Request[hummingbird.MarketplaceInfoRequest]) (*connect.Response[hummingbird.MarketplaceInfoResponse], error) {
	return &connect.Response[hummingbird.MarketplaceInfoResponse]{
		Msg: &hummingbird.MarketplaceInfoResponse{
			ApiMajorVersion:              s.info.ApiMajorVersion,
			ApiMinorVersion:              s.info.ApiMinorVersion,
			Currency:                     s.info.Currency,
			MaxStatisticsGranularity:     s.info.StatisticsTimeGranularity,
			SupportsRedemptionDelegation: s.info.SupportsRedemptionDelegation,
			CurrencyExponent:             s.info.CurrencyExponent,
			PricingStrategy:              s.info.PricingStrategy,
			TransactionFeeRelative:       s.info.TransactionFeeRelative,
			TransactionFeeAbsolute:       s.info.TransactionFeeAbsolute,
			SplitCombineFeeAbsolute:      s.info.SplitCombineFeeAbsolute,
			DelegationHourlyFee:          s.info.DelegationHourlyFee,
			AssetValidityMax:             s.info.AssetValidityMax,
		},
	}, nil
}

func (s *Service) UpdateAssets(ctx context.Context, req *connect.Request[hummingbird.UpdateAssetsRequest]) (*connect.Response[hummingbird.UpdateAssetsResponse], error) {
	ia, ok := ctx.Value("user").(addr.IA)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, serrors.New("ia not provided"))
	}
	handleAsset := func(update *hummingbird.AssetUpdate) (int64, error) {
		assetId, err := databaseAssetID(update.AssetId)
		if err != nil {
			return 0, err
		}
		switch t := update.Operation.(type) {
		case *hummingbird.AssetUpdate_Remove:
			x, err := s.store.DeleteListedAsset(ctx, ia, assetId)
			if err != nil {
				return 0, err
			}
			if x != 1 {
				return 0, serrors.New("no modifiable asset with that ID found")
			}
			return 0, nil
		case *hummingbird.AssetUpdate_Update:
			dbAsset := &db.DBAsset{
				ID:              assetId,
				IA:              ia,
				Bandwidth:       t.Update.Bandwidth,
				BandwidthMin:    t.Update.BandwidthMin,
				BandwidthMax:    t.Update.BandwidthMax,
				StartAt:         t.Update.StartsAt.AsTime(),
				StopsAt:         t.Update.StopsAt.AsTime(),
				Price:           t.Update.Price,
				TimeGranularity: t.Update.TimeGranularity,
				TimeMinDuration: t.Update.TimeMinDuration,
				TimeMaxDuration: t.Update.TimeMaxDuration,
			}
			if dbAsset.StopsAt.After(time.Now().Add(time.Duration(s.info.AssetValidityMax) * time.Second)) {
				return 0, serrors.New("validity outside allowed range")
			}
			if t.Update.IfIdIngress != nil {
				dbAsset.IfIdIngress = sql.NullInt32{
					Valid: true,
					Int32: int32(*t.Update.IfIdIngress),
				}
			}
			if t.Update.IfIdEgress != nil {
				dbAsset.IfIdEgress = sql.NullInt32{
					Valid: true,
					Int32: int32(*t.Update.IfIdEgress),
				}
			}
			newId, err := s.store.UpdateListedAsset(ctx, dbAsset)
			if err != nil {
				return 0, err
			}
			return newId, nil
		default:
			return 0, serrors.New("unknown update")
		}
	}
	res := make([]*hummingbird.UpdateAssetResult, 0, len(req.Msg.Assets))
	for _, update := range req.Msg.Assets {
		newId, err := handleAsset(update)
		if err != nil {
			res = append(res, &hummingbird.UpdateAssetResult{
				ResultType: &hummingbird.UpdateAssetResult_Error{
					Error: err.Error(),
				},
			})
		} else {
			res = append(res, &hummingbird.UpdateAssetResult{
				ResultType: &hummingbird.UpdateAssetResult_NewId{
					NewId: protoAssetID(newId),
				},
			})
		}
	}
	return &connect.Response[hummingbird.UpdateAssetsResponse]{
		Msg: &hummingbird.UpdateAssetsResponse{
			Result: res,
		},
	}, nil
}

func (s *Service) PublishAsset(ctx context.Context, req *connect.Request[hummingbird.PublishAssetRequest]) (*connect.Response[hummingbird.PublishAssetResponse], error) {
	ia, ok := ctx.Value("user").(addr.IA)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, serrors.New("ia not provided"))
	}
	dbAsset := &db.DBAsset{
		IA:              ia,
		Bandwidth:       req.Msg.Asset.Bandwidth,
		BandwidthMin:    req.Msg.Asset.BandwidthMin,
		BandwidthMax:    req.Msg.Asset.BandwidthMax,
		StartAt:         req.Msg.Asset.StartsAt.AsTime().Truncate(time.Second),
		StopsAt:         req.Msg.Asset.StopsAt.AsTime().Truncate(time.Second),
		Price:           req.Msg.Asset.Price,
		TimeGranularity: req.Msg.Asset.TimeGranularity,
		TimeMinDuration: req.Msg.Asset.TimeMinDuration,
		IfIdIngress:     sql.NullInt32{},
		IfIdEgress:      sql.NullInt32{},
	}
	if dbAsset.StopsAt.After(time.Now().Add(time.Duration(s.info.AssetValidityMax) * time.Second)) {
		return nil, connect.NewError(connect.CodeInvalidArgument, serrors.New("validity outside allowed range"))
	}
	if !dbAsset.StopsAt.After(dbAsset.StartAt) {
		return nil, connect.NewError(connect.CodeInvalidArgument, serrors.New("stopsAt must come after startsAt"))
	}
	if req.Msg.Asset.IfIdIngress != nil {
		dbAsset.IfIdIngress.Int32 = int32(*req.Msg.Asset.IfIdIngress)
		dbAsset.IfIdIngress.Valid = true
	}
	if req.Msg.Asset.IfIdEgress != nil {
		dbAsset.IfIdEgress.Int32 = int32(*req.Msg.Asset.IfIdEgress)
		dbAsset.IfIdEgress.Valid = true
	}
	assetID, err := s.store.PublishAsset(ctx, dbAsset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	return &connect.Response[hummingbird.PublishAssetResponse]{
		Msg: &hummingbird.PublishAssetResponse{
			AssetId: protoAssetID(assetID),
		},
	}, nil
}

func (s *Service) RedeemAsset(
	ctx context.Context,
	req *connect.Request[hummingbird.RedeemAssetRequest],
) (*connect.Response[hummingbird.RedeemAssetResponse], error) {
	user, ok := ctx.Value("user").(int64)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, serrors.New("user_id not provided"))
	}
	var assets []*db.DBAsset
	var err error
	switch t := req.Msg.Interfaces.(type) {
	case *hummingbird.RedeemAssetRequest_Pair:
		ingressAssetId, err := databaseAssetID(t.Pair.IngressAssetId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, serrors.New("invalid assets"))
		}
		egressAssetId, err := databaseAssetID(t.Pair.EgressAssetId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, serrors.New("invalid assets"))
		}
		assets, err = s.store.PrepareRedemption(ctx, user, &ingressAssetId, &egressAssetId, nil)
	case *hummingbird.RedeemAssetRequest_IfPairAssetId:
		pairAssetId, err := databaseAssetID(t.IfPairAssetId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, serrors.New("invalid assets"))
		}
		assets, err = s.store.PrepareRedemption(ctx, user, nil, nil, &pairAssetId)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, serrors.New("invalid interface pair"))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	var bw uint32
	var ingressID uint32
	var egressID uint32
	var startsAt time.Time
	var stopsAt time.Time
	var ia addr.IA
	if len(assets) == 1 {
		// interface pair
		pairAsset := assets[0]
		bw = pairAsset.Bandwidth
		ingressID = uint32(pairAsset.IfIdIngress.Int32)
		egressID = uint32(pairAsset.IfIdEgress.Int32)
		startsAt = pairAsset.StartAt
		stopsAt = pairAsset.StopsAt
		ia = pairAsset.IA
	} else if len(assets) == 2 {
		// ingress and egress assets
		ingressAsset := assets[0]
		egressAsset := assets[1]
		bw = min(ingressAsset.Bandwidth, egressAsset.Bandwidth)
		ingressID = uint32(ingressAsset.IfIdIngress.Int32)
		egressID = uint32(egressAsset.IfIdEgress.Int32)
		ia = ingressAsset.IA
		if ingressAsset.StartAt.Before(egressAsset.StartAt) {
			startsAt = egressAsset.StartAt
		} else {
			startsAt = ingressAsset.StartAt
		}
		if ingressAsset.StopsAt.Before(egressAsset.StopsAt) {
			stopsAt = ingressAsset.StopsAt
		} else {
			stopsAt = egressAsset.StopsAt
		}
	} else {
		return nil, connect.NewError(connect.CodeInvalidArgument, serrors.New("invalid assets"))
	}
	undoRedemption := func() error {
		// we use context.Background here because if the client disconnected in the meantime,
		// we cannot undo the redemption using the request's context.
		switch t := req.Msg.Interfaces.(type) {
		case *hummingbird.RedeemAssetRequest_Pair:
			ingressAssetId, _ := databaseAssetID(t.Pair.IngressAssetId)
			egressAssetId, _ := databaseAssetID(t.Pair.EgressAssetId)
			err = s.store.UndoRedemption(context.Background(), user, &ingressAssetId, &egressAssetId, nil)
		case *hummingbird.RedeemAssetRequest_IfPairAssetId:
			pairAssetId, _ := databaseAssetID(t.IfPairAssetId)
			err = s.store.UndoRedemption(context.Background(), user, nil, nil, &pairAssetId)
		}
		if err != nil {
			log.Error("Error while undoing redemption", "err", err)
		}
		return err
	}
	if stopsAt.Before(startsAt) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			serrors.Join(serrors.New("stops at before starts at"), undoRedemption()))
	}
	peer, found := s.redemptionServerPeers[ia]
	if !found {
		return nil, connect.NewError(connect.CodeUnavailable, serrors.Join(serrors.New("Redemption service not reachable"), undoRedemption()))
	}
	respCh := peer.Send(&hummingbird.RedeemAssetFromASRequest{
		Bandwidth: bw,
		IngressId: ingressID,
		EgressId:  egressID,
		StartsAt:  timestamppb.New(startsAt),
		StopsAt:   timestamppb.New(stopsAt),
	})
	select {
	case resp := <-respCh:
		if resp == nil {
			return nil, connect.NewError(connect.CodeUnavailable, serrors.Join(serrors.New("Redemption service not available"), undoRedemption()))
		}
		switch r := resp.Result.(type) {
		case *hummingbird.RedeemAssetFromASResponse_Error:
			return nil, connect.NewError(connect.CodeUnavailable, serrors.Join(serrors.New(r.Error), undoRedemption()))
		case *hummingbird.RedeemAssetFromASResponse_ResInfo:
			n, err := s.store.InsertReservation(ctx, &db.DBReservation{
				ReservationID:    r.ResInfo.ReservationId,
				IA:               ia,
				Ingress:          ingressID,
				Egress:           egressID,
				Bandwidth:        r.ResInfo.BandwithRounded,
				EncodedBandwidth: uint16(r.ResInfo.BwDataplaneEncoding),
				StartsAt:         startsAt,
				StopsAt:          stopsAt,
				AccountId:        user,
				Key:              r.ResInfo.AuthenticationKey,
			})
			if err != nil {
				return nil, connect.NewError(connect.CodeUnavailable, serrors.Join(err, undoRedemption()))
			}
			if n != 1 {
				return nil, connect.NewError(connect.CodeUnavailable, serrors.Join(serrors.New("reservation could not be stored"), undoRedemption()))
			}
			return &connect.Response[hummingbird.RedeemAssetResponse]{
				Msg: &hummingbird.RedeemAssetResponse{
					AuthenticationKey:   r.ResInfo.AuthenticationKey,
					ReservationId:       r.ResInfo.ReservationId,
					BandwidthRounded:    r.ResInfo.BandwithRounded,
					BwDataplaneEncoding: r.ResInfo.BwDataplaneEncoding,
				},
			}, nil
		}
	case <-time.After(30 * time.Second):
		return nil, connect.NewError(connect.CodeUnavailable, serrors.Join(serrors.New("Redemption service not available"), undoRedemption()))
	}
	return nil, connect.NewError(connect.CodeUnavailable, serrors.Join(serrors.New("Redemption service not available"), undoRedemption()))
}

func (s *Service) Statistics(ctx context.Context, req *connect.Request[hummingbird.StatisticsRequest]) (*connect.Response[hummingbird.StatisticsResponse], error) {
	ia, ok := ctx.Value("user").(addr.IA)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, serrors.New("ia not provided"))
	}
	var step time.Duration
	step = hbird.RoundUpDuration(time.Duration(req.Msg.Step)*time.Second, time.Duration(s.info.StatisticsTimeGranularity)*time.Second)
	windowStart := req.Msg.Start.AsTime().UTC().Truncate(time.Duration(s.info.StatisticsTimeGranularity) * time.Second)
	windowEnd := hbird.RoundUpTime(req.Msg.End.AsTime().UTC(), time.Duration(s.info.StatisticsTimeGranularity)*time.Second)
	num_intervals := int(windowEnd.Sub(windowStart) / step)
	if num_intervals > 1024 {
		return nil, connect.NewError(connect.CodeResourceExhausted, serrors.New("too many intervals"))
	}
	income := make([]uint64, num_intervals)
	bwBought := make([]uint64, num_intervals)
	bwPublished := make([]uint64, num_intervals)
	publishedAssets, boughtAssets, err := s.store.Statistics(ctx, &db.StatisticsQuery{
		IA:          ia,
		WindowStart: windowStart.Format(time.RFC3339),
		WindowEnd:   windowEnd.Format(time.RFC3339),
		Ingress:     req.Msg.IfIdIngress,
		Egress:      req.Msg.IfIdEgress,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	for _, asset := range publishedAssets {
		start := asset.StartsAt
		stop := asset.StopsAt
		if asset.StartsAt.Before(windowStart) {
			start = windowStart
		}
		if asset.StopsAt.After(windowEnd) {
			stop = windowEnd
		}
		first := int(start.Sub(windowStart) / step)

		last := int(stop.Sub(windowStart) / step)
		if first < 0 || last > num_intervals {
			continue
		}
		if stop.Equal(windowStart.Add(time.Duration(last) * step)) {
			last--
		}
		for i := first; i <= last && i < num_intervals; i++ {
			intervalStart := windowStart.Add(time.Duration(i) * step)
			intervalEnd := intervalStart.Add(step)
			overlapStart := start
			overlapEnd := stop
			if asset.StartsAt.Before(intervalStart) {
				overlapStart = intervalStart
			}
			if asset.StopsAt.After(intervalEnd) {
				overlapEnd = intervalEnd
			}
			duration := uint64(overlapEnd.Sub(overlapStart).Seconds())
			bwTimesDuration := uint64(asset.Bandwidth) * duration
			bwPublished[i] += bwTimesDuration
		}
	}

	for _, asset := range boughtAssets {
		start := asset.StartsAt
		stop := asset.StopsAt
		if asset.StartsAt.Before(windowStart) {
			start = windowStart
		}
		if asset.StopsAt.After(windowEnd) {
			stop = windowEnd
		}
		first := int(start.Sub(windowStart) / step)

		last := int(stop.Sub(windowStart) / step)
		if first < 0 || last > num_intervals {
			continue
		}
		if stop.Equal(windowStart.Add(time.Duration(last) * step)) {
			last--
		}
		for i := first; i <= last && i < num_intervals; i++ {
			intervalStart := windowStart.Add(time.Duration(i) * step)
			intervalEnd := intervalStart.Add(step)
			overlapStart := start
			overlapEnd := stop
			if asset.StartsAt.Before(intervalStart) {
				overlapStart = intervalStart
			}
			if asset.StopsAt.After(intervalEnd) {
				overlapEnd = intervalEnd
			}
			duration := uint64(overlapEnd.Sub(overlapStart).Seconds())
			bwTimesDuration := uint64(asset.Bandwidth) * duration
			bwBought[i] += bwTimesDuration
			income[i] += bwTimesDuration * uint64(asset.Price)
		}
	}
	respEntries := make([]*hummingbird.StatisticsResponseEntry, num_intervals)
	for i := 0; i < num_intervals; i++ {
		respEntries[i] = &hummingbird.StatisticsResponseEntry{
			Revenue:              income[i],
			BandwidthUtilization: float64(bwBought[i]) / float64(bwPublished[i]),
		}
	}
	return &connect.Response[hummingbird.StatisticsResponse]{
		Msg: &hummingbird.StatisticsResponse{
			Statistics: respEntries,
		},
	}, nil
}

func (s *Service) SearchAssets(ctx context.Context, req *connect.Request[hummingbird.SearchAssetsRequest]) (*connect.Response[hummingbird.SearchAssetsResponse], error) {
	var owner_id *int64
	switch user := ctx.Value("user").(type) {
	case int64:
		if req.Msg.Owned {
			owner_id = &user
		}
	case addr.IA:
	default:
		return nil, connect.NewError(connect.CodePermissionDenied, serrors.New("user_id not provided"))
	}
	var startsAt *string
	var stopsAt *string
	if req.Msg.StartsAtLatest != nil {
		start := req.Msg.StartsAtLatest.AsTime().UTC().Format(time.RFC3339)
		startsAt = &start
	}
	if req.Msg.StopsAtEarliest != nil {
		stop := req.Msg.StopsAtEarliest.AsTime().UTC().Format(time.RFC3339)
		stopsAt = &stop
	}
	var ia *addr.IA
	if req.Msg.Ia != nil {
		tmp := addr.IA(*req.Msg.Ia)
		ia = &tmp
	}
	pageSize := s.info.MaxReturnedAssets
	if req.Msg.MaxReturnedAssets != nil {
		pageSize = min(pageSize, *req.Msg.MaxReturnedAssets)
	}

	assets, err := s.store.Search(ctx, &db.AssetQuery{
		AccountId:            owner_id,
		IA:                   ia,
		Ingress:              req.Msg.IfIdIngress,
		Egress:               req.Msg.IfIdEgress,
		MinRequiredBandwidth: req.Msg.MinRequiredBw,
		Price:                req.Msg.Price,
		StartsAt:             startsAt,
		StopsAt:              stopsAt,
		Page:                 req.Msg.Page,
		PageSize:             pageSize,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	repAssets := make([]*hummingbird.SearchAsset, 0, len(assets))
	for _, asset := range assets {
		a := &hummingbird.SearchAsset{
			AssetId:         protoAssetID(asset.ID),
			Ia:              uint64(asset.IA),
			Bandwidth:       asset.Bandwidth,
			BandwidthMin:    asset.BandwidthMin,
			BandwidthMax:    asset.BandwidthMax,
			TimeMinDuration: asset.TimeMinDuration,
			TimeMaxDuration: asset.TimeMaxDuration,
			StartsAt:        timestamppb.New(asset.StartAt),
			StopsAt:         timestamppb.New(asset.StopsAt),
			TimeGranularity: asset.TimeGranularity,
			Price:           asset.Price,
		}
		if asset.IfIdIngress.Valid {
			ingress := uint32(asset.IfIdIngress.Int32)
			a.IfIdIngress = &ingress
		}
		if asset.IfIdEgress.Valid {
			egress := uint32(asset.IfIdEgress.Int32)
			a.IfIdEgress = &egress
		}
		repAssets = append(repAssets, a)
	}
	return &connect.Response[hummingbird.SearchAssetsResponse]{
		Msg: &hummingbird.SearchAssetsResponse{
			Assets: repAssets,
		},
	}, nil
}
