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

package storage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"math"
	"time"

	marketplacedb "github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/pkg/addr"
	hbird "github.com/scionproto/scion/pkg/hummingbird"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	"github.com/scionproto/scion/private/config"
	"github.com/scionproto/scion/private/storage/db"
)

type DBConfig struct {
	config.NoDefaulter
	Connection       string `toml:"connection,omitempty"`
	MaxOpenReadConns int    `toml:"max_open_read_conns,omitempty"`
	MaxIdleReadConns int    `toml:"max_idle_read_conns,omitempty"`
	allowEmptyConn   bool
}

type MarketplaceStorage struct {
	db                      marketplacedb.MarketplaceDB
	transactionFeeRelative  float32
	transactionFeeAbsolute  uint64
	splitCombineFeeAbsolute uint64
	delegationHourlyFee     uint64
}

func NewStorage(
	c DBConfig,
	transactionFeeRelative float32,
	transactionFeeAbsolute uint64,
	splitCombineFeeAbsolute uint64,
	delegationHourlyFee uint64,
) (*MarketplaceStorage, error) {
	db, err := marketplacedb.New(c.Connection, &db.SqliteConfig{
		MaxOpenReadConns: c.MaxOpenReadConns,
		MaxIdleReadConns: c.MaxIdleReadConns,
	})
	if err != nil {
		return nil, err
	}
	return &MarketplaceStorage{
		db:                      db,
		transactionFeeRelative:  transactionFeeRelative,
		transactionFeeAbsolute:  transactionFeeAbsolute,
		splitCombineFeeAbsolute: splitCombineFeeAbsolute,
		delegationHourlyFee:     delegationHourlyFee,
	}, nil
}

func (s *MarketplaceStorage) SearchListedAssets(
	ctx context.Context,
	params *marketplacedb.ListedAssetQuery,
) ([]*marketplacedb.DBAsset, error) {
	return s.db.SearchListedAssets(ctx, params)
}

func (s *MarketplaceStorage) SearchOwnedAssets(
	ctx context.Context,
	params *marketplacedb.OwnedAssetQuery,
) ([]*marketplacedb.DBAsset, error) {
	return s.db.SearchOwnedAssets(ctx, params)
}

func (s *MarketplaceStorage) FetchReservations(
	ctx context.Context,
	params *marketplacedb.ReservationQuery,
) ([]*marketplacedb.DBReservation, error) {
	return s.db.FetchReservations(ctx, params)
}

func (s *MarketplaceStorage) PublishAsset(
	ctx context.Context,
	a *marketplacedb.DBAsset,
) (int64, error) {
	err := validateAsset(a)
	if err != nil {
		return 0, err
	}
	var assetId int64
	err = s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		assetId, err = tx.InsertAsset(ctx, a)
		if err != nil {
			return err
		}
		_, err = tx.InsertAssetSegment(ctx, assetId, a.StartsAt, a.StopsAt, a.Bandwidth)
		if err != nil {
			return err
		}
		_, err = tx.RegisterAssetEvent(ctx, a, marketplacedb.AssetPublished)
		if err != nil {
			return err
		}
		return nil
	})
	return assetId, err
}
func (s *MarketplaceStorage) UpdateListedAsset(
	ctx context.Context,
	id int64,
	ia addr.IA,
	a *hummingbird.UpdateAsset,
) (int64, error) {
	var newID int64
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		// get current dbAsset
		dbAsset, err := tx.LookupAsset(ctx, id, ia)
		if err != nil {
			return err
		}
		// check for illegal modifications
		if (a.Bandwidth != nil && *a.Bandwidth < dbAsset.Bandwidth) ||
			(a.StartsAt != nil && *a.StartsAt > dbAsset.StartsAt) ||
			(a.StopsAt != nil && *a.StopsAt < dbAsset.StopsAt) ||
			(a.TimeMinDuration != nil && *a.TimeMinDuration > dbAsset.TimeMinDuration) ||
			(a.TimeMaxDuration != nil && *a.TimeMaxDuration < dbAsset.TimeMaxDuration) ||
			(a.BandwidthMin != nil && *a.BandwidthMin > dbAsset.BandwidthMin) ||
			(a.BandwidthMax != nil && *a.BandwidthMax < dbAsset.BandwidthMax) {
			return serrors.New("invalid asset modifications")
		}
		if a.Bandwidth != nil && *a.Bandwidth > dbAsset.Bandwidth {
			if err = tx.IncreaseAssetSegmentsBandwidth(ctx, id, int64(*a.Bandwidth-dbAsset.Bandwidth)); err != nil {
				return err
			}
			dbAsset.Bandwidth = *a.Bandwidth
		}
		if a.StartsAt != nil && *a.StartsAt < dbAsset.StartsAt {
			if _, err = tx.InsertAssetSegment(ctx, id, *a.StartsAt, dbAsset.StartsAt, dbAsset.Bandwidth); err != nil {
				return err
			}
			if err = tx.MergeAssetAt(ctx, id, dbAsset.StartsAt); err != nil {
				return err
			}
			dbAsset.StartsAt = *a.StartsAt
		}
		if a.StopsAt != nil && *a.StopsAt > dbAsset.StopsAt {
			if _, err = tx.InsertAssetSegment(ctx, id, dbAsset.StopsAt, *a.StopsAt, dbAsset.Bandwidth); err != nil {
				return err
			}
			if err = tx.MergeAssetAt(ctx, id, dbAsset.StopsAt); err != nil {
				return err
			}
			dbAsset.StopsAt = *a.StopsAt
		}
		if a.BandwidthMin != nil {
			dbAsset.BandwidthMin = *a.BandwidthMin
		}
		if a.BandwidthMax != nil {
			dbAsset.BandwidthMax = *a.BandwidthMax
		}
		if a.TimeMinDuration != nil {
			dbAsset.TimeMinDuration = *a.TimeMinDuration
		}
		if a.TimeMaxDuration != nil {
			dbAsset.TimeMaxDuration = *a.TimeMaxDuration
		}
		if a.Price != nil {
			dbAsset.Price = *a.Price
		}
		x, err := tx.UpdateAsset(ctx, dbAsset)
		if err != nil {
			return err
		}
		if x != 1 {
			return serrors.New("invalid assets")
		}
		return nil
	})
	return newID, err
}
func (s *MarketplaceStorage) DeleteListedAsset(
	ctx context.Context,
	ia addr.IA,
	assetID int64,
) (int64, error) {
	return s.db.DeleteListedAsset(ctx, ia, assetID)
}

func (s *MarketplaceStorage) GetAccountsByUser(
	ctx context.Context,
	id int64,
) ([]*marketplacedb.DBAccount, error) {
	return s.db.GetAccountsByUser(ctx, id)
}

func (s *MarketplaceStorage) GetUser(
	ctx context.Context,
	id int64,
) (*marketplacedb.DBUser, error) {
	return s.db.GetUser(ctx, id)
}

func (s *MarketplaceStorage) GetASUser(
	ctx context.Context,
	ia addr.IA,
) (*marketplacedb.DBASUser, error) {
	return s.db.GetASUser(ctx, ia)
}

func (s *MarketplaceStorage) SetASAuthenticationToken(
	ctx context.Context,
	ia addr.IA,
	auth string,
) (int64, error) {
	return s.db.SetASAuthenticationToken(ctx, ia, auth)
}

func (s *MarketplaceStorage) GetUserByName(
	ctx context.Context,
	name string,
) (*marketplacedb.DBUser, error) {
	return s.db.GetUserByName(ctx, name)
}

func (s *MarketplaceStorage) CreateAccount(
	ctx context.Context,
	account *marketplacedb.DBAccount,
) (int64, error) {
	return s.db.CreateAccount(ctx, account)
}

func (s *MarketplaceStorage) GetAccountByAccountID(
	ctx context.Context,
	id int64,
) (*marketplacedb.DBAccount, error) {
	return s.db.GetAccountByAccountID(ctx, id)
}

func (s *MarketplaceStorage) DeleteAccount(
	ctx context.Context,
	user_id int64,
	accountId int64,
) (int64, error) {
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		allUserAccounts, err := tx.GetAccountsByUser(ctx, user_id)
		if err != nil {
			return err
		}
		var targetAccount *marketplacedb.DBAccount
		var mainAccount *marketplacedb.DBAccount
		for _, account := range allUserAccounts {
			if account.ID == accountId {
				targetAccount = account
			} else if account.Scope == "" {
				mainAccount = account
			}
		}
		if targetAccount == nil || mainAccount == nil {
			return serrors.New("invalid account")
		}
		if _, err := tx.TransferAllAssetsToAccount(ctx, targetAccount.ID, mainAccount.ID); err != nil {
			return err
		}
		if _, err := tx.TransferAllReservations(ctx, targetAccount.ID, mainAccount.ID); err != nil {
			return err
		}
		if _, err := tx.UpdateAccountMoney(ctx, mainAccount.ID, targetAccount.Balance); err != nil {
			return err
		}
		x, err := tx.DeleteAccount(ctx, targetAccount.ID)
		if err != nil {
			return err
		}
		if x != 1 {
			return serrors.New("account not found")
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *MarketplaceStorage) TransferMoneyBetweenAccounts(
	ctx context.Context,
	user_id int64,
	accountFrom int64,
	accountTo int64,
	balance int64,
) (int64, error) {
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		x, err := tx.UpdateAccountMoneyWithUser(ctx, user_id, accountFrom, -balance)
		if err != nil {
			return err
		}
		if x != 1 {
			return serrors.New("account not found")
		}
		x, err = tx.UpdateAccountMoneyWithUser(ctx, user_id, accountTo, balance)
		if err != nil {
			return err
		}
		if x != 1 {
			return serrors.New("account not found")
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *MarketplaceStorage) CreateUser(
	ctx context.Context,
	user *marketplacedb.DBUser,
) (int64, error) {
	var userID int64
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		var err error
		userID, err = tx.CreateUser(ctx, user)
		if err != nil {
			return err
		}
		_, err = tx.CreateAccount(ctx, &marketplacedb.DBAccount{UserID: userID})
		return err
	})
	return userID, err
}

func (s *MarketplaceStorage) CreateASUser(
	ctx context.Context,
	user *marketplacedb.DBASUser,
) (int64, error) {
	return s.db.CreateASUser(ctx, user)
}

func (s *MarketplaceStorage) CreateOrUpdateRedemptionDelegations(
	ctx context.Context,
	r *marketplacedb.RedemptionDelegation,
) (int64, error) {
	var id int64
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		delegation, err := tx.FindRedemptionDelegation(ctx, r.IA)
		if err != nil {
			return err
		}
		paidUntil := time.Now().Unix()
		if delegation != nil {
			paidUntil = delegation.PaidUntil
		}
		if s.delegationHourlyFee == 0 {
			if r.Expiration > paidUntil {
				r.PaidUntil = r.Expiration
			} else {
				r.PaidUntil = paidUntil
			}
		} else if r.Expiration > paidUntil {
			paymentDuration := hbird.RoundUpDuration(time.Duration(r.Expiration-paidUntil)*time.Second, time.Hour)
			numHours := uint64(paymentDuration / time.Hour)
			if _, err := tx.UpdateASMoney(
				ctx, r.IA, -int64(numHours*s.delegationHourlyFee)); err != nil {
				return err
			}
			r.PaidUntil = paidUntil + int64(numHours)*60*60
		} else {
			r.PaidUntil = paidUntil
		}
		id, err = tx.CreateOrUpdateRedemptionDelegations(ctx, r)
		return err
	})
	return id, err
}
func (s *MarketplaceStorage) FindRedemptionDelegations(
	ctx context.Context,
) ([]*marketplacedb.RedemptionDelegation, error) {
	return s.db.FindRedemptionDelegations(ctx)
}

func addInt64(
	a int64,
	b int64,
) (int64, bool) {
	if (b > 0 && a > math.MaxInt64-b) ||
		(b < 0 && a < math.MinInt64-b) {
		return 0, false // overflow
	}
	return a + b, true
}

func multiplyInts(xs ...int64) (int64, bool) {
	result := int64(1)

	for _, x := range xs {
		if x == 0 {
			return 0, true
		}
		if result > 0 {
			if x > 0 && result > math.MaxInt64/x {
				return 0, false
			}
			if x < 0 && x < math.MinInt64/result {
				return 0, false
			}
		} else {
			if x > 0 && result < math.MinInt64/x {
				return 0, false
			}
			if x < 0 && result < math.MaxInt64/x {
				return 0, false
			}
		}
		result *= x
	}
	return result, true
}

func (s *MarketplaceStorage) IncrementASJWTVersion(
	ctx context.Context,
	ia addr.IA,
	current int64,
) (int64, error) {
	return s.db.IncrementASJWTVersion(ctx, ia, current)
}

func (s *MarketplaceStorage) IncrementAccountJWTVersion(
	ctx context.Context,
	accountId int64,
	current int64,
) (int64, error) {
	return s.db.IncrementAccountJWTVersion(ctx, accountId, current)
}

func (s *MarketplaceStorage) IncrementUserJWTVersion(
	ctx context.Context,
	userId int64,
	accountId int64,
) (int64, error) {
	return s.db.IncrementUserJWTVersion(ctx, userId, accountId)
}

func gcd(
	a uint32,
	b uint32,
) uint32 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func lcm(
	a uint32,
	b uint32,
) uint32 {
	return a / gcd(a, b) * b
}

// Combines multiple compatible assets into a single assset.
// Requires len(assetIds) >= 2.
// Requires assetIDs sorted by asset validity.
func (s *MarketplaceStorage) CombineAssets(
	ctx context.Context,
	accountID int64,
	assetIds []int64,
) (int64, error) {
	if len(assetIds) < 2 {
		return 0, serrors.New("at least 2 asset IDs are required when combining assets")
	}
	var newID int64
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		var err error
		if _, err := tx.UpdateAccountMoney(
			ctx, accountID, -int64(s.splitCombineFeeAbsolute)*int64(len(assetIds)-1)); err != nil {
			return err
		}
		assets := make([]*marketplacedb.DBAsset, 0, len(assetIds))
		for _, assetId := range assetIds {
			a, err := tx.TransitionAsset(ctx, assetId, &accountID,
				marketplacedb.AssetStateAvailable, marketplacedb.AssetStateCombinePending)
			if err != nil {
				return err
			}
			assets = append(assets, a)
		}
		ia := assets[0].IA
		ingress := assets[0].IfIdIngress
		egress := assets[0].IfIdEgress
		combinedStop := assets[0].StopsAt
		useTimeAxis := assets[0].StopsAt == assets[1].StartsAt
		combinedMinBW := assets[0].BandwidthMin
		combinedMaxBW := assets[0].BandwidthMax
		combinedTimeMinDuration := assets[0].TimeMinDuration
		combinedTimeMaxDuration := assets[0].TimeMaxDuration
		combinedTimeGranularity := assets[0].TimeGranularity
		combinedBandwidth := assets[0].Bandwidth

		for _, asset := range assets[1:] {
			if asset.IA != ia {
				return serrors.New("assets must have same IA")
			}
			if asset.IfIdIngress != ingress {
				return serrors.New("asset must have same ingress")
			}
			if asset.IfIdEgress != egress {
				return serrors.New("asset must have same egress")
			}
			combinedMinBW = max(combinedMinBW, asset.BandwidthMin)
			combinedMaxBW = min(combinedMaxBW, asset.BandwidthMax)
			combinedTimeMinDuration = max(combinedTimeMinDuration, asset.TimeMinDuration)
			combinedTimeMaxDuration = min(combinedTimeMaxDuration, asset.TimeMaxDuration)
			combinedTimeGranularity = lcm(combinedTimeGranularity, asset.TimeGranularity)
			if useTimeAxis {
				if assets[0].Bandwidth != asset.Bandwidth {
					return serrors.New("assets cannot be combined")
				}
				if combinedStop != asset.StartsAt {
					return serrors.New("assets cannot be combined")
				}
				combinedStop = asset.StopsAt
			} else {
				if asset.StartsAt != assets[0].StartsAt || asset.StopsAt != assets[0].StopsAt {
					return serrors.New("assets cannot be combined")
				}
				combinedBandwidth += asset.Bandwidth
			}
		}
		combinedAsset := &marketplacedb.DBAsset{
			AccountId: sql.NullInt64{
				Int64: accountID,
				Valid: true,
			},
			IA:              ia,
			IfIdIngress:     ingress,
			IfIdEgress:      egress,
			BandwidthMin:    combinedMinBW,
			BandwidthMax:    combinedMaxBW,
			TimeMinDuration: combinedTimeMinDuration,
			TimeMaxDuration: combinedTimeMaxDuration,
			TimeGranularity: combinedTimeGranularity,
			Bandwidth:       combinedBandwidth,
			StartsAt:        assets[0].StartsAt,
			StopsAt:         combinedStop,
		}
		for _, assetId := range assetIds {
			if err := tx.RemoveAsset(ctx, assetId); err != nil {
				return err
			}
		}
		newID, err = tx.InsertAsset(ctx, combinedAsset)
		return err
	})
	return newID, err
}

func (s *MarketplaceStorage) SplitAsset(
	ctx context.Context,
	accountID int64,
	assetId int64,
	bwSplit []uint32,
	timeSplit []int64,
) ([]int64, error) {
	if len(bwSplit) == 0 && len(timeSplit) == 0 {
		return nil, serrors.New("invalid split request")
	}
	var ids []int64
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		if _, err := tx.UpdateAccountMoney(
			ctx, accountID, -int64(s.splitCombineFeeAbsolute)*int64(max(len(bwSplit), len(timeSplit)))); err != nil {
			return err
		}
		dbAsset, err := tx.TransitionAsset(ctx, assetId, &accountID,
			marketplacedb.AssetStateAvailable, marketplacedb.AssetStateSplitPending)
		if err != nil {
			return err
		}
		var splitSegments []AssetSegment
		baseSegment := AssetSegment{
			StartsAt:  dbAsset.StartsAt,
			StopsAt:   dbAsset.StopsAt,
			Bandwidth: dbAsset.Bandwidth,
		}
		if len(bwSplit) != 0 {
			for _, split := range bwSplit {
				res, err := SplitAsset(baseSegment, RequestedSplit{
					ExactFrom:      baseSegment.StartsAt,
					ExactTo:        baseSegment.StopsAt,
					ExactBandwidth: split,
				})
				if err != nil {
					return err
				}
				splitSegments = append(splitSegments, res.Split)
				if len(res.Remainders) == 0 {
					baseSegment = AssetSegment{}
				} else if len(res.Remainders) == 1 {
					baseSegment = res.Remainders[0]
				} else {
					return serrors.New("invalid split")
				}
			}
		} else {
			for _, split := range timeSplit {
				res, err := SplitAsset(baseSegment, RequestedSplit{
					ExactFrom:      baseSegment.StartsAt,
					ExactTo:        split,
					ExactBandwidth: baseSegment.Bandwidth,
				})
				if err != nil {
					return err
				}
				splitSegments = append(splitSegments, res.Split)
				if len(res.Remainders) == 0 {
					baseSegment = AssetSegment{}
				} else if len(res.Remainders) == 1 {
					baseSegment = res.Remainders[0]
				} else {
					return serrors.New("invalid split")
				}
			}
		}
		if baseSegment.Bandwidth != 0 && baseSegment.StartsAt != baseSegment.StopsAt {
			splitSegments = append(splitSegments, baseSegment)
		}
		if err := tx.RemoveAsset(ctx, assetId); err != nil {
			return err
		}
		for _, segment := range splitSegments {
			asset := &marketplacedb.DBAsset{
				AccountId:       dbAsset.AccountId,
				IA:              dbAsset.IA,
				BandwidthMin:    dbAsset.BandwidthMin,
				BandwidthMax:    dbAsset.BandwidthMax,
				Price:           dbAsset.Price,
				TimeGranularity: dbAsset.TimeGranularity,
				TimeMinDuration: dbAsset.TimeMinDuration,
				TimeMaxDuration: dbAsset.TimeMaxDuration,
				IfIdIngress:     dbAsset.IfIdIngress,
				IfIdEgress:      dbAsset.IfIdEgress,
				Bandwidth:       segment.Bandwidth,
				StartsAt:        segment.StartsAt,
				StopsAt:         segment.StopsAt,
			}
			splitAssetId, err := tx.InsertAsset(ctx, asset)
			if err != nil {
				return err
			}
			ids = append(ids, splitAssetId)
		}
		return err
	})
	return ids, err
}

func (s *MarketplaceStorage) Statistics(
	ctx context.Context,
	params *marketplacedb.StatisticsQuery,
) ([]*marketplacedb.DBStat, []*marketplacedb.DBStat, error) {
	return s.db.SearchAssetsForStatistics(ctx, params)
}

func (s *MarketplaceStorage) FindUsedReservations(
	ctx context.Context,
	params *marketplacedb.UsedReservationsQuery,
) ([]*marketplacedb.UsedReservation, error) {
	return s.db.FindUsedReservations(ctx, params)
}

func DatabaseAssetID(id []byte) (int64, error) {
	if len(id) != 8 {
		return 0, serrors.New("invalid asset ID")
	}
	idInt := binary.BigEndian.Uint64(id)
	return marketplacedb.AssetID(idInt).Int64()
}

func (s *MarketplaceStorage) BuyAssets(
	ctx context.Context,
	accountID int64,
	assets []*hummingbird.BuyAsset,
	maxPrice uint64,
) ([]int64, int64, error) {
	if len(assets) == 0 {
		return nil, 0, serrors.New("no assets specified")
	}
	for _, asset := range assets {
		if asset.BandwidthExact <= 0 || asset.StartsAtExactly >= asset.StopsAtExactly {
			return nil, 0, serrors.New("invalid asset")
		}
	}
	boughtIDs := []int64{}
	costAcc := int64(0)

	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		calcCost := func(a *marketplacedb.DBAsset) (int64, bool) {
			totalAssetPrice, ok := multiplyInts(int64(a.StopsAt-a.StartsAt),
				int64(a.Bandwidth), int64(a.Price))
			if !ok {
				return 0, false
			}
			fee := int64(float64(totalAssetPrice)*float64(s.transactionFeeRelative)) +
				int64(s.transactionFeeAbsolute)
			return addInt64(int64(totalAssetPrice), fee)
		}
		buyAsset := func(
			assetId int64,
			startsAt int64,
			stopsAt int64,
			bw uint32,
		) (*marketplacedb.DBAsset, int64, error) {
			asset, err := tx.CheckAssetAvailability(ctx, assetId, startsAt, stopsAt, bw)
			if err != nil {
				return nil, 0, err
			}
			if err = tx.SplitAssetAt(ctx, assetId, startsAt); err != nil {
				return nil, 0, err
			}
			if err = tx.SplitAssetAt(ctx, assetId, stopsAt); err != nil {
				return nil, 0, err
			}
			userAsset, err := tx.BuyAsset(ctx, startsAt, stopsAt, bw, accountID, asset)
			if err != nil {
				return nil, 0, err
			}
			if err = tx.MergeAssetAt(ctx, assetId, startsAt); err != nil {
				return nil, 0, err
			}
			if err = tx.MergeAssetAt(ctx, assetId, stopsAt); err != nil {
				return nil, 0, err
			}
			_, err = tx.RegisterAssetEvent(ctx, userAsset, marketplacedb.AssetBought)
			if err != nil {
				return nil, 0, err
			}
			cost, ok := calcCost(userAsset)
			if !ok {
				return nil, 0, serrors.New("asset parameters would lead to integer overflow")
			}
			_, err = tx.UpdateASMoney(ctx, asset.IA, cost)
			if err != nil {
				return nil, 0, err
			}
			return userAsset, cost, nil
		}
		for _, asset := range assets {
			assetId, err := DatabaseAssetID(asset.AssetId)
			if err != nil {
				return err
			}
			newAsset, cost, err := buyAsset(assetId, asset.StartsAtExactly, asset.StopsAtExactly, asset.BandwidthExact)
			if err != nil {
				return err
			}
			var ok bool
			costAcc, ok = addInt64(costAcc, cost)
			if !ok {
				return serrors.New("asset parameters would lead to integer overflow")
			}
			if uint64(costAcc) > maxPrice {
				// we perform this check per asset to be able to terminate early
				return serrors.New("assets too expensive")
			}
			boughtIDs = append(boughtIDs, newAsset.ID)
		}
		_, err := tx.UpdateAccountMoney(ctx, accountID, -costAcc)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return boughtIDs, costAcc, nil
}

func (s *MarketplaceStorage) InsertReservation(
	ctx context.Context,
	r *marketplacedb.DBReservation,
) (int64, error) {
	return s.db.InsertReservation(ctx, r)
}

func (s *MarketplaceStorage) UndoRedemption(
	ctx context.Context,
	user_id int64,
	ingressID *int64,
	egressID *int64,
	pairID *int64,
) error {
	return s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		transition := func(id int64) error {
			_, err := tx.TransitionAsset(ctx, id, &user_id,
				marketplacedb.AssetStateRedemptionPending, marketplacedb.AssetStateAvailable)
			return err
		}
		switch {
		case ingressID != nil && egressID != nil:
			if err := transition(*ingressID); err != nil {
				return err
			}
			return transition(*egressID)
		case ingressID == nil && egressID == nil && pairID != nil:
			return transition(*pairID)
		default:
			return serrors.New("invalid asset IDs")
		}
	})
}
func validateAsset(
	a *marketplacedb.DBAsset,
) error {
	duration := a.StopsAt - a.StartsAt
	if a.Bandwidth == 0 {
		return serrors.New("bandwidth is 0")
	}
	if duration <= 0 {
		return serrors.New("duration of the asset is not positive")
	}
	return nil
}
func validateAssetForRedemption(
	a *marketplacedb.DBAsset,
) error {
	duration := a.StopsAt - a.StartsAt
	if err := validateAsset(a); err != nil {
		return err
	}
	if a.Bandwidth < a.BandwidthMin {
		return serrors.New("bandwidth < min_bandwidth")
	}
	if a.Bandwidth > a.BandwidthMax {
		return serrors.New("bandwith > max_bandiwdth")
	}
	if duration < int64(a.TimeMinDuration) {
		return serrors.New("duration < time_min_duration")
	}
	if duration > int64(a.TimeMaxDuration) {
		return serrors.New("duration > time_max_duration")
	}
	if duration%int64(a.TimeGranularity) != 0 {
		return serrors.New("duration not multiple of time granularity")
	}
	return nil
}
func (s *MarketplaceStorage) PrepareRedemption(
	ctx context.Context,
	user_id int64,
	ingressAssetID *int64,
	egressAssetID *int64,
	pairAssetID *int64,
) ([]*marketplacedb.DBAsset, error) {
	var assets []*marketplacedb.DBAsset
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		prepare := func(id int64) (*marketplacedb.DBAsset, error) {
			return tx.TransitionAsset(ctx, id, &user_id,
				marketplacedb.AssetStateAvailable, marketplacedb.AssetStateRedemptionPending)
		}
		switch {
		case ingressAssetID != nil && egressAssetID != nil:
			ingressAsset, err := prepare(*ingressAssetID)
			if err != nil {
				return err
			}
			if !ingressAsset.IfIdIngress.Valid || ingressAsset.IfIdEgress.Valid {
				return serrors.New("ingress asset is not an ingress asset")
			}
			if err := validateAssetForRedemption(ingressAsset); err != nil {
				return err
			}
			egressAsset, err := prepare(*egressAssetID)
			if err != nil {
				return err
			}
			if !egressAsset.IfIdEgress.Valid || egressAsset.IfIdIngress.Valid {
				return serrors.New("egress asset is not an egress asset")
			}
			if err := validateAssetForRedemption(egressAsset); err != nil {
				return err
			}
			assets = []*marketplacedb.DBAsset{ingressAsset, egressAsset}
			return nil
		case ingressAssetID == nil && egressAssetID == nil && pairAssetID != nil:
			pairAsset, err := prepare(*pairAssetID)
			if err != nil {
				return err
			}
			if !pairAsset.IfIdIngress.Valid || !pairAsset.IfIdEgress.Valid {
				return serrors.New("pair asset is not a pair asset")
			}
			if err := validateAssetForRedemption(pairAsset); err != nil {
				return err
			}
			assets = []*marketplacedb.DBAsset{pairAsset}
			return nil
		default:
			return serrors.New("invalid asset IDs")
		}
	})
	return assets, err
}

func (s *MarketplaceStorage) DepositMoneyAndGet(
	ctx context.Context,
	id int64,
	amount int64,
) (*marketplacedb.DBUser, error) {
	var user *marketplacedb.DBUser
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		if _, err := tx.UpdateAccountMoney(ctx, id, amount); err != nil {
			return err
		}
		var err error
		user, err = tx.GetUser(ctx, id)
		return err
	})
	return user, err
}

func (s *MarketplaceStorage) DepositMoneyAndGetAS(
	ctx context.Context,
	ia addr.IA,
	amount int64,
) (*marketplacedb.DBASUser, error) {
	var user *marketplacedb.DBASUser
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		if _, err := tx.UpdateASMoney(ctx, ia, amount); err != nil {
			return err
		}
		var err error
		user, err = tx.GetASUser(ctx, ia)
		return err
	})
	return user, err
}

func (s *MarketplaceStorage) AssignAsset(
	ctx context.Context,
	assetID int64,
	userID int64,
	accountIDFrom int64,
	accountIDTo int64,
) (int64, error) {
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		accounts, err := tx.GetAccountsByUser(ctx, userID)
		if err != nil {
			return err
		}
		var fromAcc, toAcc *marketplacedb.DBAccount
		for _, acc := range accounts {
			if acc.ID == accountIDFrom {
				fromAcc = acc
			} else if acc.ID == accountIDTo {
				toAcc = acc
			}
		}
		if fromAcc == nil || toAcc == nil {
			return serrors.New("invalid account or asset")
		}
		x, err := tx.AssignAsset(ctx, assetID, fromAcc.ID, toAcc.ID)
		if err != nil {
			return err
		}
		if x != 1 {
			return serrors.New("invalid account or asset")
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *MarketplaceStorage) AssignReservation(
	ctx context.Context,
	id int64,
	userID int64,
	accountIDFrom int64,
	accountIDTo int64,
) (int64, error) {
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		accounts, err := tx.GetAccountsByUser(ctx, userID)
		if err != nil {
			return err
		}
		var fromAcc, toAcc *marketplacedb.DBAccount
		for _, acc := range accounts {
			if acc.ID == accountIDFrom {
				fromAcc = acc
			} else if acc.ID == accountIDTo {
				toAcc = acc
			}
		}
		if fromAcc == nil || toAcc == nil {
			return serrors.New("invalid account or reservation")
		}
		x, err := tx.AssignReservation(ctx, id, fromAcc.ID, toAcc.ID)
		if err != nil {
			return err
		}
		if x != 1 {
			return serrors.New("invalid account or reservation")
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *MarketplaceStorage) LookupAssets(ctx context.Context, ia addr.IA) ([]*marketplacedb.DBAsset, error) {
	return s.db.LookupAssets(ctx, ia)
}

func (s *MarketplaceStorage) LookupAllAssetSegments(ctx context.Context, id int64, ia addr.IA) ([]*marketplacedb.DBAssetSegment, error) {
	var segments []*marketplacedb.DBAssetSegment
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		_, err := tx.LookupAsset(ctx, id, ia)
		if err != nil {
			return err
		}
		segments, err = tx.LookupAllAssetSegments(ctx, id)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return segments, nil
}
