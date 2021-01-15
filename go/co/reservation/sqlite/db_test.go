// Copyright 2020 ETH Zurich, Anapaya Systems
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

package sqlite

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/go/co/reservation/reservationdbtest"
	"github.com/scionproto/scion/go/co/reservation/segment"
	"github.com/scionproto/scion/go/co/reservationstorage/backend"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/xtest"
)

func TestReservationDBSuite(t *testing.T) {
	reservationdbtest.TestDB(t, func() backend.DB { return newDB(t) })
}

func TestNewSegSuffix(t *testing.T) {
	ctx := context.Background()
	asid := xtest.MustParseAS("ff00:0:1")
	db := newDB(t)
	suffix, err := newSegSuffix(ctx, db.db, asid)
	require.NoError(t, err)
	require.Equal(t, uint32(1), suffix)
	// add reservations
	addSegRsvRows(t, db, asid, 3, 5)
	suffix, err = newSegSuffix(ctx, db.db, asid)
	require.NoError(t, err)
	require.False(t, isSuffixInDB(t, db, asid, suffix))
	addSegRsvRows(t, db, asid, 1, 2)
	suffix, err = newSegSuffix(ctx, db.db, asid)
	require.NoError(t, err)
	require.False(t, isSuffixInDB(t, db, asid, suffix))
}

// TestRaceForSuffix checks that there are no problems trying to obtain a suffix for the same
// AS ID from different goroutines, even if using transactions.
// As we use sqlite3 per default, the default behavior is to have only 1 open connection.
// This makes impossible to have more than one running transaction at a time. In this case
// the DB access is serialized by the sqlite3 driver itself.
// If we manually allow more than 1 open connection, we can create more than 1 transaction
// at a time. The problem arises if we try to modify the same table from more than one
// transaction, because the driver will return with a "table locked" or "database locked".
func TestRaceForSuffix(t *testing.T) {
	ctx := context.Background()
	asid := xtest.MustParseAS("ff00:0:1")
	db := newDB(t)

	wg := sync.WaitGroup{}
	wg.Add(2)

	fcn := func(t *testing.T, rsv *segment.Reservation, m1, m2 *sync.Mutex) {
		tx, err := db.BeginTransaction(ctx, nil)
		require.NoError(t, err, "failed for rsv %s", rsv.ID.String())
		defer tx.Rollback()

		t.Logf("[%s] waiting on 1...", &rsv.ID)
		m1.Lock()
		defer m1.Unlock()
		t.Logf("[%s] woke up on 1...", &rsv.ID)

		err = tx.NewSegmentRsv(ctx, rsv)
		require.NoError(t, err, "failed for rsv %s", rsv.ID.String())

		t.Logf("[%s] waiting on 2...", &rsv.ID)
		m2.Lock()
		defer m2.Unlock()
		t.Logf("[%s] woke up on 2...", &rsv.ID)

		err = tx.Commit()
		require.NoError(t, err, "failed for rsv %s", rsv.ID.String())
		wg.Done()
	}

	mut1_1, mut1_2 := sync.Mutex{}, sync.Mutex{}
	mut2_1, mut2_2 := sync.Mutex{}, sync.Mutex{}
	lockAllMutexes := func() {
		mut1_1.Lock()
		mut1_2.Lock()
		mut2_1.Lock()
		mut2_2.Lock()
	}

	rsv1 := segment.Reservation{
		ID:      reservation.ID{ASID: asid, Suffix: []byte{1, 1, 1, 1}},
		Indices: segment.Indices{segment.Index{}}}
	rsv2 := segment.Reservation{
		ID:      reservation.ID{ASID: asid, Suffix: []byte{2, 2, 2, 2}},
		Indices: segment.Indices{segment.Index{}}}
	lockAllMutexes()

	go fcn(t, &rsv1, &mut1_1, &mut1_2)
	go fcn(t, &rsv2, &mut2_1, &mut2_2)

	t.Logf("rsv1: %v", rsv1.ID.String())
	t.Logf("rsv2: %v", rsv2.ID.String())
	mut1_1.Unlock()
	mut2_1.Unlock()
	mut2_2.Unlock()
	mut1_2.Unlock()
	wg.Wait()
	t.Logf("rsv1: %v", rsv1.ID.String())
	t.Logf("rsv2: %v", rsv2.ID.String())
	require.NotEqual(t, rsv1.ID.Suffix, rsv2.ID.Suffix)
}

func BenchmarkNewSuffix10K(b *testing.B)  { benchmarkNewSuffix(b, 10000) }
func BenchmarkNewSuffix100K(b *testing.B) { benchmarkNewSuffix(b, 100000) }
func BenchmarkNewSuffix1M(b *testing.B)   { benchmarkNewSuffix(b, 1000000) }

func newDB(t testing.TB) *Backend {
	t.Helper()
	db, err := New("file::memory:")
	require.NoError(t, err)
	return db
}

func addSegRsvRows(t testing.TB, b *Backend, asid addr.AS, firstSuffix, lastSuffix uint32) {
	t.Helper()
	ctx := context.Background()
	query := `INSERT INTO seg_reservation (id_as, id_suffix, ingress, egress, path_type, path,
		end_props, traffic_split, src_ia, dst_ia, active_index)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, -1)`
	for suffix := firstSuffix; suffix <= lastSuffix; suffix++ {
		_, err := b.db.ExecContext(ctx, query, asid, suffix, 0, 0, reservation.CorePath, nil,
			0, 0, nil, nil)
		require.NoError(t, err)
	}
}

func isSuffixInDB(t *testing.T, b *Backend, asid addr.AS, suffix uint32) bool {
	t.Helper()
	ctx := context.Background()
	query := `SELECT COUNT(*) FROM seg_reservation
	WHERE id_as = ? AND id_suffix = ?`
	var count int
	err := b.db.QueryRowContext(ctx, query, asid, suffix).Scan(&count)
	require.NoError(t, err)
	return count > 0
}

func benchmarkNewSuffix(b *testing.B, entries uint32) {
	db := newDB(b)
	ctx := context.Background()
	asid := xtest.MustParseAS("ff00:0:1")
	addSegRsvRows(b, db, asid, 1, entries)
	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		suffix, err := newSegSuffix(ctx, db.db, asid)
		require.NoError(b, err)
		require.Equal(b, entries+1, suffix)
	}
}
