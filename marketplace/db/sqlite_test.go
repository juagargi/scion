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
