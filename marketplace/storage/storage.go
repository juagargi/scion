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
	"math"
	"strconv"
	"time"

	marketplacedb "github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/pkg/addr"
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

func (s *MarketplaceStorage) Search(
	ctx context.Context,
	params *marketplacedb.AssetQuery,
) ([]*marketplacedb.DBAsset, error) {
	return s.db.Search(ctx, params)
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
	return s.db.InsertAsset(ctx, a)
}
func (s *MarketplaceStorage) UpdateListedAsset(
	ctx context.Context,
	a *marketplacedb.DBAsset,
) (int64, error) {
	if err := validateAsset(a); err != nil {
		return 0, err
	}
	var newID int64
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		x, err := tx.DeleteListedAsset(ctx, a.IA, a.ID)
		if err != nil {
			return err
		}
		if x != 1 {
			return serrors.New("no modifiable asset with that ID found")
		}
		newID, err = tx.InsertAsset(ctx, a)
		return err
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
			} else if account.Scope == nil {
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

func ceilDuration(
	base time.Duration,
	multiple time.Duration,
) time.Duration {
	truncated := base.Truncate(multiple)
	if truncated == base {
		return base
	}
	return truncated + multiple
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
		paidUntil := time.Now()
		if delegation != nil {
			paidUntil = delegation.PaidUntil
		}
		if s.delegationHourlyFee == 0 {
			if r.Expiration.After(paidUntil) {
				r.PaidUntil = r.Expiration
			}
		} else if r.Expiration.After(paidUntil) {
			paymentDuration := ceilDuration(r.Expiration.Sub(paidUntil), time.Hour)
			numHours := uint64(paymentDuration / time.Hour)
			if _, err := tx.UpdateASMoney(
				ctx, r.IA, -int64(numHours*s.delegationHourlyFee)); err != nil {
				return err
			}
			r.PaidUntil = paidUntil.Add(paymentDuration)
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

func mulInt64(
	a int64,
	b int64,
) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a == math.MinInt64 && b == -1 {
		return 0, false
	}
	if b == math.MinInt64 && a == -1 {
		return 0, false
	}
	if a > math.MaxInt64/b || a < math.MinInt64/b {
		return 0, false
	}
	return a * b, true
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

func (s *MarketplaceStorage) totalPrice(
	price uint32,
	bw uint32,
	startsAt time.Time,
	stopsAt time.Time,
) (int64, int64, bool) {
	splitDuration := int64(stopsAt.Sub(startsAt).Seconds())
	a, safe := mulInt64(int64(price), splitDuration)
	if !safe {
		return 0, 0, false
	}
	costWithoutFee, safe := mulInt64(a, int64(bw))
	if !safe {
		return 0, 0, false
	}
	fee := int64(float64(costWithoutFee)*float64(s.transactionFeeRelative)) +
		int64(s.transactionFeeAbsolute)
	return costWithoutFee, fee, true
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

func (s *MarketplaceStorage) CombineAssets(
	ctx context.Context,
	accountID int64,
	assetId1 int64,
	assetId2 int64,
) (int64, error) {
	var newID int64
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		if _, err := tx.UpdateAccountMoney(
			ctx, accountID, -int64(s.splitCombineFeeAbsolute)); err != nil {
			return err
		}
		a1, err := tx.TransitionAsset(ctx, assetId1, &accountID,
			marketplacedb.AssetStateAvailable, marketplacedb.AssetStateCombinePending)
		if err != nil {
			return err
		}
		a2, err := tx.TransitionAsset(ctx, assetId2, &accountID,
			marketplacedb.AssetStateAvailable, marketplacedb.AssetStateCombinePending)
		if err != nil {
			return err
		}
		if a1.IA != a2.IA {
			return serrors.New("asset must have same IA")
		}
		if a1.IfIdIngress.Valid && a1.IfIdIngress.Int32 != a2.IfIdIngress.Int32 {
			return serrors.New("asset must have same ingress")
		}
		if a1.IfIdEgress.Valid && a1.IfIdEgress.Int32 != a2.IfIdEgress.Int32 {
			return serrors.New("asset must have same egress")
		}
		var combinedAsset *marketplacedb.DBAsset
		if a1.StartAt.Equal(a2.StartAt) && a1.StopsAt.Equal(a2.StopsAt) {
			combinedAsset = &marketplacedb.DBAsset{
				AccountId:       a1.AccountId,
				IA:              a1.IA,
				Bandwidth:       a1.Bandwidth + a2.Bandwidth,
				BandwidthMin:    max(a1.BandwidthMin, a2.BandwidthMin),
				BandwidthMax:    min(a1.BandwidthMax, a2.BandwidthMax),
				StartAt:         a1.StartAt,
				StopsAt:         a1.StopsAt,
				Price:           0,
				TimeGranularity: lcm(a1.TimeGranularity, a2.TimeGranularity),
				TimeMinDuration: max(a1.TimeMinDuration, a2.TimeMinDuration),
				IfIdIngress:     a1.IfIdIngress,
				IfIdEgress:      a1.IfIdEgress,
			}
		} else if a1.Bandwidth == a2.Bandwidth {
			if a1.StartAt.Equal(a2.StopsAt) {
				combinedAsset = &marketplacedb.DBAsset{
					AccountId:       a1.AccountId,
					IA:              a1.IA,
					Bandwidth:       a1.Bandwidth,
					BandwidthMin:    max(a1.BandwidthMin, a2.BandwidthMin),
					BandwidthMax:    min(a1.BandwidthMax, a2.BandwidthMax),
					StartAt:         a2.StartAt,
					StopsAt:         a1.StopsAt,
					Price:           0,
					TimeGranularity: lcm(a1.TimeGranularity, a2.TimeGranularity),
					TimeMinDuration: max(a1.TimeMinDuration, a2.TimeMinDuration),
					IfIdIngress:     a1.IfIdIngress,
					IfIdEgress:      a1.IfIdEgress,
				}
			} else if a2.StartAt.Equal(a1.StopsAt) {
				combinedAsset = &marketplacedb.DBAsset{
					AccountId:       a1.AccountId,
					IA:              a1.IA,
					Bandwidth:       a1.Bandwidth,
					BandwidthMin:    max(a1.BandwidthMin, a2.BandwidthMin),
					BandwidthMax:    min(a1.BandwidthMax, a2.BandwidthMax),
					StartAt:         a1.StartAt,
					StopsAt:         a2.StopsAt,
					Price:           0,
					TimeGranularity: lcm(a1.TimeGranularity, a2.TimeGranularity),
					TimeMinDuration: max(a1.TimeMinDuration, a2.TimeMinDuration),
					IfIdIngress:     a1.IfIdIngress,
					IfIdEgress:      a1.IfIdEgress,
				}
			}
		}
		if combinedAsset == nil {
			return serrors.New("asset cannot be combined")
		}
		if err := tx.RemoveAsset(ctx, assetId1); err != nil {
			return err
		}
		if err := tx.RemoveAsset(ctx, assetId2); err != nil {
			return err
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
	bwSplit *uint32,
	timeSplit *time.Time,
) (int64, int64, error) {
	if bwSplit == nil && timeSplit == nil {
		return 0, 0, serrors.New("invalid split request")
	}
	var assetID1, assetID2 int64
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		if _, err := tx.UpdateAccountMoney(
			ctx, accountID, -int64(s.splitCombineFeeAbsolute)); err != nil {
			return err
		}
		dbAsset, err := tx.TransitionAsset(ctx, assetId, &accountID,
			marketplacedb.AssetStateAvailable, marketplacedb.AssetStateSplitPending)
		if err != nil {
			return err
		}
		requestedSplit := RequestedSplit{
			ExactBandwidth: dbAsset.Bandwidth,
			ExactFrom:      dbAsset.StartAt,
			ExactTo:        dbAsset.StopsAt,
		}
		if bwSplit != nil {
			requestedSplit.ExactBandwidth = *bwSplit
		} else {
			requestedSplit.ExactTo = *timeSplit
		}
		splitResult, err := SplitAsset(dbAsset, requestedSplit)
		if err != nil {
			return err
		}
		if len(splitResult.Remainders) != 1 {
			return serrors.New("invalid split result")
		}
		asset1 := &marketplacedb.DBAsset{
			AccountId:       dbAsset.AccountId,
			IA:              dbAsset.IA,
			BandwidthMin:    dbAsset.BandwidthMin,
			BandwidthMax:    dbAsset.BandwidthMax,
			Price:           dbAsset.Price,
			TimeGranularity: dbAsset.TimeGranularity,
			TimeMinDuration: dbAsset.TimeMinDuration,
			IfIdIngress:     dbAsset.IfIdIngress,
			IfIdEgress:      dbAsset.IfIdEgress,
			Bandwidth:       splitResult.Split.Bandwidth,
			StartAt:         splitResult.Split.StartsAt,
			StopsAt:         splitResult.Split.StopsAt,
		}
		asset2 := &marketplacedb.DBAsset{
			AccountId:       dbAsset.AccountId,
			IA:              dbAsset.IA,
			BandwidthMin:    dbAsset.BandwidthMin,
			BandwidthMax:    dbAsset.BandwidthMax,
			Price:           dbAsset.Price,
			TimeGranularity: dbAsset.TimeGranularity,
			TimeMinDuration: dbAsset.TimeMinDuration,
			IfIdIngress:     dbAsset.IfIdIngress,
			IfIdEgress:      dbAsset.IfIdEgress,
			Bandwidth:       splitResult.Remainders[0].Bandwidth,
			StartAt:         splitResult.Remainders[0].StartsAt,
			StopsAt:         splitResult.Remainders[0].StopsAt,
		}
		if err := tx.RemoveAsset(ctx, assetId); err != nil {
			return err
		}
		assetID1, err = tx.InsertAsset(ctx, asset1)
		if err != nil {
			return err
		}
		assetID2, err = tx.InsertAsset(ctx, asset2)
		return err
	})
	return assetID1, assetID2, err
}

func (s *MarketplaceStorage) Statistics(
	ctx context.Context,
	params *marketplacedb.StatisticsQuery,
) ([]*marketplacedb.DBStat, error) {
	return s.db.SearchAssetsForStatistics(ctx, params)
}

func (s *MarketplaceStorage) FindUsedReservations(
	ctx context.Context,
	params *marketplacedb.UsedReservationsQuery,
) ([]*marketplacedb.UsedReservation, error) {
	return s.db.FindUsedReservations(ctx, params)
}

func (s *MarketplaceStorage) BuyAssets(
	ctx context.Context,
	accountID int64,
	assets []*hummingbird.BuyAsset,
	maxPrice uint64,
) ([]int64, int64, error) {
	uniqueCheck := make(map[string]bool)
	for _, asset := range assets {
		if uniqueCheck[asset.AssetId] {
			return nil, 0, serrors.New("Only a single split per asset per buy request allowed")
		}
		if asset.StopsAtExactly.AsTime().Before(asset.StartsAtExactly.AsTime()) {
			return nil, 0, serrors.New("End of validity must come after start of validity")
		}
		uniqueCheck[asset.AssetId] = true
	}
	boughtAssets := make([]int64, 0, 1)
	costAcc := int64(0)
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		for _, asset := range assets {
			assetID, err := strconv.ParseInt(asset.AssetId, 10, 64)
			if err != nil {
				return err
			}
			dbAsset, err := tx.TransitionAsset(ctx, assetID, nil,
				marketplacedb.AssetStateAvailable, marketplacedb.AssetStateCheckedOut)
			if err != nil {
				return err
			}
			split, err := SplitAsset(dbAsset, RequestedSplit{
				ExactFrom:      asset.StartsAtExactly.AsTime(),
				ExactTo:        asset.StopsAtExactly.AsTime(),
				ExactBandwidth: asset.BandwidthExact,
			})
			if err != nil {
				return err
			}
			newAsset := &marketplacedb.DBAsset{
				AccountId:       sql.NullInt64{Int64: accountID, Valid: true},
				IA:              dbAsset.IA,
				BandwidthMin:    dbAsset.BandwidthMin,
				BandwidthMax:    dbAsset.BandwidthMax,
				TimeGranularity: dbAsset.TimeGranularity,
				TimeMinDuration: dbAsset.TimeMinDuration,
				IfIdIngress:     dbAsset.IfIdIngress,
				IfIdEgress:      dbAsset.IfIdEgress,
				Bandwidth:       split.Split.Bandwidth,
				StartAt:         split.Split.StartsAt,
				StopsAt:         split.Split.StopsAt,
				Price:           dbAsset.Price,
			}
			id, err := tx.InsertAsset(ctx, newAsset)
			if err != nil {
				return err
			}
			totalAssetPrice, fee, safe := s.totalPrice(
				dbAsset.Price,
				split.Split.Bandwidth,
				split.Split.StartsAt,
				split.Split.StopsAt)
			if !safe {
				return serrors.New("total asset price would lead to integer overflow")
			}
			pricePlusFee, safe := addInt64(totalAssetPrice, fee)
			if !safe {
				return serrors.New("total asset price would lead to integer overflow")
			}
			if _, err := tx.UpdateASMoney(ctx, dbAsset.IA, totalAssetPrice); err != nil {
				return err
			}
			costAcc, safe = addInt64(costAcc, pricePlusFee)
			if !safe {
				return serrors.New("total asset price would lead to integer overflow")
			}
			boughtAssets = append(boughtAssets, id)
			for _, segment := range split.Remainders {
				_, err := tx.InsertAsset(ctx, &marketplacedb.DBAsset{
					IA:              dbAsset.IA,
					BandwidthMin:    dbAsset.BandwidthMin,
					BandwidthMax:    dbAsset.BandwidthMax,
					TimeGranularity: dbAsset.TimeGranularity,
					TimeMinDuration: dbAsset.TimeMinDuration,
					IfIdIngress:     dbAsset.IfIdIngress,
					IfIdEgress:      dbAsset.IfIdEgress,
					Bandwidth:       segment.Bandwidth,
					StartAt:         segment.StartsAt,
					StopsAt:         segment.StopsAt,
					Price:           dbAsset.Price,
				})
				if err != nil {
					return err
				}
			}
			if err := tx.RemoveAsset(ctx, dbAsset.ID); err != nil {
				return err
			}
		}
		if uint64(costAcc) > maxPrice {
			return serrors.New("cost higher than max price")
		}
		_, err := tx.UpdateAccountMoney(ctx, accountID, -costAcc)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return boughtAssets, costAcc, nil
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
	ingressIDString *string,
	egressIDString *string,
	pairIDString *string,
) error {
	return s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		// TODO(juagargi) deleteme IDs should be integers not strings.
		transition := func(id string) error {
			assetID, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return err
			}
			_, err = tx.TransitionAsset(ctx, assetID, &user_id,
				marketplacedb.AssetStateRedemptionPending, marketplacedb.AssetStateAvailable)
			return err
		}
		switch {
		case ingressIDString != nil && egressIDString != nil:
			if err := transition(*ingressIDString); err != nil {
				return err
			}
			return transition(*egressIDString)
		case ingressIDString == nil && egressIDString == nil && pairIDString != nil:
			return transition(*pairIDString)
		default:
			return serrors.New("invalid asset IDs")
		}
	})
}
func validateAsset(
	a *marketplacedb.DBAsset,
) error {
	duration := a.StopsAt.Sub(a.StartAt)
	if a.Bandwidth == 0 {
		return serrors.New("bandwidth is 0")
	}
	if duration == 0 {
		return serrors.New("duration is 0")
	}
	if a.Bandwidth < a.BandwidthMin {
		return serrors.New("bandwidth < min_bandwidth")
	}
	if a.Bandwidth > a.BandwidthMax {
		return serrors.New("bandwith > max_bandiwdth")
	}
	if duration < time.Duration(a.TimeMinDuration)*time.Second {
		return serrors.New("duration < time_min_duration")
	}
	if duration%(time.Duration(a.TimeGranularity)*time.Second) != 0 {
		return serrors.New("duration not multiple of time granularity")
	}
	return nil
}
func (s *MarketplaceStorage) PrepareRedemption(
	ctx context.Context,
	user_id int64,
	ingressIDString *string,
	egressIDString *string,
	pairIDString *string,
) ([]*marketplacedb.DBAsset, error) {
	var assets []*marketplacedb.DBAsset
	err := s.db.WithTx(ctx, func(tx marketplacedb.Repository) error {
		prepare := func(id string) (*marketplacedb.DBAsset, error) {
			assetID, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return nil, err
			}
			return tx.TransitionAsset(ctx, assetID, &user_id,
				marketplacedb.AssetStateAvailable, marketplacedb.AssetStateRedemptionPending)
		}
		switch {
		case ingressIDString != nil && egressIDString != nil:
			ingressAsset, err := prepare(*ingressIDString)
			if err != nil {
				return err
			}
			if !ingressAsset.IfIdIngress.Valid || ingressAsset.IfIdEgress.Valid {
				return serrors.New("ingress asset is not an ingress asset")
			}
			if err := validateAsset(ingressAsset); err != nil {
				return err
			}
			egressAsset, err := prepare(*egressIDString)
			if err != nil {
				return err
			}
			if !egressAsset.IfIdEgress.Valid || egressAsset.IfIdIngress.Valid {
				return serrors.New("egress asset is not an egress asset")
			}
			if err := validateAsset(egressAsset); err != nil {
				return err
			}
			assets = []*marketplacedb.DBAsset{ingressAsset, egressAsset}
			return nil
		case ingressIDString == nil && egressIDString == nil && pairIDString != nil:
			pairAsset, err := prepare(*pairIDString)
			if err != nil {
				return err
			}
			if !pairAsset.IfIdIngress.Valid || !pairAsset.IfIdEgress.Valid {
				return serrors.New("pair asset is not a pair asset")
			}
			if err := validateAsset(pairAsset); err != nil {
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
