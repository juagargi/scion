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

package db

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/scionproto/scion/pkg/addr"
	storagedb "github.com/scionproto/scion/private/storage/db"
	"github.com/stretchr/testify/require"
)

func newTestBackend(t *testing.T) *Backend {
	t.Helper()
	backend, err := New("file:marketplace-db-"+t.Name(), &storagedb.SqliteConfig{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, backend.Close())
	})
	return backend
}

func testAsset() *DBAsset {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	return &DBAsset{
		IA:              addr.MustParseIA("1-ff00:0:110"),
		Bandwidth:       100,
		BandwidthMin:    10,
		BandwidthMax:    200,
		Price:           5,
		TimeGranularity: 60,
		TimeMinDuration: 60,
		StartAt:         start,
		StopsAt:         start.Add(time.Hour),
	}
}

// TestAssetIDInt64 rejects identifiers that cannot be represented by SQLite.
func TestAssetIDInt64(t *testing.T) {
	id, err := AssetID(42).Int64()
	require.NoError(t, err)
	require.Equal(t, int64(42), id)

	_, err = AssetID(math.MaxInt64 + 1).Int64()
	require.Error(t, err)
}

// TestWithTxCommitsAndRollsBack verifies that WithTx persists successful work
// and rolls back when its callback returns an error.
func TestWithTxCommitsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	// A successful callback commits its insert.
	err := backend.WithTx(ctx, func(repository Repository) error {
		_, err := repository.InsertAsset(ctx, testAsset())
		return err
	})
	require.NoError(t, err)
	assets, err := backend.Search(ctx, &AssetQuery{PageSize: 64})
	require.NoError(t, err)
	require.Len(t, assets, 1)

	// Rolls back the insert and preserves the error.
	rollbackErr := errors.New("rollback")
	err = backend.WithTx(ctx, func(repository Repository) error {
		_, err := repository.InsertAsset(ctx, testAsset())
		if err != nil {
			return err
		}
		return rollbackErr
	})
	require.ErrorIs(t, err, rollbackErr)

	// Only the asset written by the committed transaction is visible.
	assets, err = backend.Search(ctx, &AssetQuery{PageSize: 64})
	require.NoError(t, err)
	require.Len(t, assets, 1)
}

// TestAssetTransitionAndRowScanners verifies asset state transitions and that
// asset and reservation query results are decoded into their domain types.
func TestAssetTransitionAndRowScanners(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	asset := testAsset()
	assetID, err := backend.InsertAsset(ctx, asset)
	require.NoError(t, err)
	// Listing an asset without an owner lets the marketplace check it out.
	transitioned, err := backend.TransitionAsset(ctx, assetID, nil,
		AssetStateAvailable, AssetStateCheckedOut)
	require.NoError(t, err)
	require.Equal(t, assetID, transitioned.ID)
	require.Equal(t, asset.IA, transitioned.IA)
	require.False(t, transitioned.AccountId.Valid)

	// Checked-out assets are no longer returned by the available-asset search.
	assets, err := backend.Search(ctx, &AssetQuery{PageSize: 64})
	require.NoError(t, err)
	require.Empty(t, assets)

	// Create an account-owned reservation and read it back through the shared scanner.
	userID, err := backend.CreateUser(ctx, &DBUser{Name: "alice", PasswordHash: "hash"})
	require.NoError(t, err)
	accountID, err := backend.CreateAccount(ctx, &DBAccount{UserID: userID})
	require.NoError(t, err)
	reservation := &DBReservation{
		ReservationID:    7,
		AccountId:        accountID,
		IA:               asset.IA,
		Ingress:          1,
		Egress:           2,
		Bandwidth:        100,
		EncodedBandwidth: 5,
		StartsAt:         asset.StartAt,
		StopsAt:          asset.StopsAt,
		Key:              []byte("key"),
	}
	_, err = backend.InsertReservation(ctx, reservation)
	require.NoError(t, err)

	reservations, err := backend.FetchReservations(ctx, &ReservationQuery{AccountId: accountID})
	require.NoError(t, err)
	require.Len(t, reservations, 1)
	got := reservations[0]
	require.Equal(t, reservation.ReservationID, got.ReservationID)
	require.Equal(t, reservation.IA, got.IA)
	require.Equal(t, reservation.StartsAt, got.StartsAt)
	require.Equal(t, reservation.StopsAt, got.StopsAt)
}

// TestCreateAccountRejectsSecondMainAccount verifies that a user has at most one
// main account. The main account is the one with an empty scope, so that
// UNIQUE(user_id, scope) rejects the duplicate: a NULL scope would not, because
// SQLite considers every NULL distinct.
func TestCreateAccountRejectsSecondMainAccount(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	userID, err := backend.CreateUser(ctx, &DBUser{Name: "alice", PasswordHash: "hash"})
	require.NoError(t, err)

	_, err = backend.CreateAccount(ctx, &DBAccount{UserID: userID})
	require.NoError(t, err)
	_, err = backend.CreateAccount(ctx, &DBAccount{UserID: userID})
	require.Error(t, err, "a user must not get a second main account")

	accounts, err := backend.GetAccountsByUser(ctx, userID)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, "", accounts[0].Scope)

	// Sub accounts are still one per name, and do not collide with the main one.
	_, err = backend.CreateAccount(ctx, &DBAccount{UserID: userID, Scope: "publisher"})
	require.NoError(t, err)
	_, err = backend.CreateAccount(ctx, &DBAccount{UserID: userID, Scope: "publisher"})
	require.Error(t, err, "a user must not get two sub accounts with the same name")

	accounts, err = backend.GetAccountsByUser(ctx, userID)
	require.NoError(t, err)
	require.Len(t, accounts, 2)
}

// TestSearchOwnedAssets covers searching the assets of an account: the account
// is bound to a JOIN that precedes the WHERE clause in the statement, and the
// ownership check must not swallow the remaining filters.
func TestSearchOwnedAssets(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	userID, err := backend.CreateUser(ctx, &DBUser{Name: "alice", PasswordHash: "hash"})
	require.NoError(t, err)
	alicesAccountID, err := backend.CreateAccount(ctx, &DBAccount{UserID: userID})
	require.NoError(t, err)

	owned := testAsset()
	owned.AccountId = sql.NullInt64{Int64: alicesAccountID, Valid: true}
	ownedID, err := backend.InsertAsset(ctx, owned)
	require.NoError(t, err)

	// An asset nobody owns, and one owned by somebody else.
	notOwnedAssetID, err := backend.InsertAsset(ctx, testAsset())
	require.NoError(t, err)
	bobUserID, err := backend.CreateUser(ctx, &DBUser{Name: "bob", PasswordHash: "hash"})
	require.NoError(t, err)
	bobsAccountID, err := backend.CreateAccount(ctx, &DBAccount{UserID: bobUserID})
	require.NoError(t, err)
	otherAsset := testAsset()
	otherAsset.AccountId = sql.NullInt64{Int64: bobsAccountID, Valid: true}
	_, err = backend.InsertAsset(ctx, otherAsset)
	require.NoError(t, err)

	assets, err := backend.Search(ctx, &AssetQuery{AccountId: &alicesAccountID, PageSize: 64})
	require.NoError(t, err)
	require.Len(t, assets, 1)
	require.Equal(t, ownedID, assets[0].ID)

	// The validity filters still apply to the assets of the account.
	startsAt := owned.StartAt.UTC().Format(time.RFC3339)
	stopsAt := owned.StopsAt.UTC().Format(time.RFC3339)
	assets, err = backend.Search(ctx, &AssetQuery{
		AccountId: &alicesAccountID,
		StartsAt:  &startsAt,
		StopsAt:   &stopsAt,
		PageSize:  64,
	})
	require.NoError(t, err)
	require.Len(t, assets, 1)

	tooLate := owned.StopsAt.Add(time.Hour).UTC().Format(time.RFC3339)
	assets, err = backend.Search(ctx, &AssetQuery{
		AccountId: &alicesAccountID,
		StartsAt:  &startsAt,
		StopsAt:   &tooLate,
		PageSize:  64,
	})
	require.NoError(t, err)
	require.Empty(t, assets, "an asset that stops too early must not be returned")

	// Without an account, only the assets nobody owns are listed.
	assets, err = backend.Search(ctx, &AssetQuery{PageSize: 64})
	require.NoError(t, err)
	require.Len(t, assets, 1)
	require.Equal(t, notOwnedAssetID, assets[0].ID)
	require.False(t, assets[0].AccountId.Valid)
}

func TestCreateOrUpdateRedemptionDelegations(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)
	ia := addr.MustParseIA("1-ff00:0:110")
	// first try creating a redemption delegation
	delegationToInsert := &RedemptionDelegation{
		IA:         ia,
		Expiration: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		PaidUntil:  time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		ResIdLow:   0,
		ResIdHigh:  1024,
		Key:        []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		Encodings:  []byte{0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0, 4},
	}
	_, err := backend.CreateOrUpdateRedemptionDelegations(ctx, delegationToInsert)
	require.NoError(t, err)
	// now compare whether the redemption delegation is stored and returned correctly
	delegation, err := backend.FindRedemptionDelegation(ctx, ia)
	require.NoError(t, err)
	require.Equal(t, *delegationToInsert, *delegation)
	// now check whether finding all redemption delegations also returned the correct one
	allDelegations, err := backend.FindRedemptionDelegations(ctx)
	require.NoError(t, err)
	require.Len(t, allDelegations, 1)
	require.Equal(t, *delegationToInsert, *allDelegations[0])
	// now try to update a redemption delegation
	delegationToUpdate := &RedemptionDelegation{
		IA:         ia,
		Expiration: time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC),
		PaidUntil:  time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC),
		ResIdLow:   1024,
		ResIdHigh:  2048,
		Key:        []byte{16, 15, 14, 16, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		Encodings:  []byte{0, 0, 0, 5, 0, 0, 0, 9, 0, 0, 0, 7, 0, 0, 0, 8},
	}
	_, err = backend.CreateOrUpdateRedemptionDelegations(ctx, delegationToUpdate)
	require.NoError(t, err)
	delegation, err = backend.FindRedemptionDelegation(ctx, ia)
	require.NoError(t, err)
	require.Equal(t, *delegationToUpdate, *delegation)
}

func TestRegisterAssetEvent(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)
	ia := addr.MustParseIA("1-ff00:0:110")
	// register a publish asset event
	publishAsset := &DBAsset{
		ID:              1,
		IA:              ia,
		Bandwidth:       1000,
		BandwidthMin:    10,
		BandwidthMax:    1000,
		StartAt:         time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		StopsAt:         time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC),
		TimeGranularity: 10,
		TimeMinDuration: 10,
		TimeMaxDuration: 3600,
		Price:           2,
		IfIdIngress:     sql.NullInt32{Valid: true, Int32: 1},
		IfIdEgress:      sql.NullInt32{Valid: true, Int32: 2},
	}
	_, err := backend.RegisterAssetEvent(ctx, publishAsset, AssetPublished)
	require.NoError(t, err)
	// register a buy asset event
	buyAsset := &DBAsset{
		AccountId:       sql.NullInt64{Valid: true, Int64: 7},
		ID:              2,
		IA:              ia,
		Bandwidth:       1000,
		BandwidthMin:    10,
		BandwidthMax:    1000,
		StartAt:         time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		StopsAt:         time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC),
		TimeGranularity: 10,
		TimeMinDuration: 10,
		TimeMaxDuration: 3600,
		Price:           2,
		IfIdIngress:     sql.NullInt32{Valid: true, Int32: 1},
		IfIdEgress:      sql.NullInt32{Valid: true, Int32: 2},
	}

	_, err = backend.RegisterAssetEvent(ctx, buyAsset, AssetBought)
	require.NoError(t, err)
	// now verify whether both registered events return the correct values.
	publishStats, buyStats, err := backend.SearchAssetsForStatistics(ctx, &StatisticsQuery{
		IA:          ia,
		WindowStart: "2027-01-01T00:00:00Z",
		WindowEnd:   "2028-01-01T00:00:00Z",
	})
	require.NoError(t, err)
	require.Len(t, publishStats, 1)
	require.Len(t, buyStats, 1)
	require.Equal(t, publishAsset.Bandwidth, uint32(publishStats[0].Bandwidth))
	require.Equal(t, publishAsset.Price, uint32(publishStats[0].Price))
	require.Equal(t, publishAsset.StartAt, publishStats[0].StartsAt)
	require.Equal(t, publishAsset.StopsAt, publishStats[0].StopsAt)

	require.Equal(t, buyAsset.Bandwidth, uint32(buyStats[0].Bandwidth))
	require.Equal(t, buyAsset.Price, uint32(buyStats[0].Price))
	require.Equal(t, buyAsset.StartAt, buyStats[0].StartsAt)
	require.Equal(t, buyAsset.StopsAt, buyStats[0].StopsAt)
}
