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
	"errors"
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
	assets, err := backend.Search(ctx, &AssetQuery{})
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
	assets, err = backend.Search(ctx, &AssetQuery{})
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
	assets, err := backend.Search(ctx, &AssetQuery{})
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
