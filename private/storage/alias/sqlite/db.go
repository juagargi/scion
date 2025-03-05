// Copyright 2025 ETH Zurich
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
	"database/sql"
	"sync"
	"time"

	"github.com/scionproto/scion/pkg/alias"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/private/util"
	"github.com/scionproto/scion/private/aliasdb"
	"github.com/scionproto/scion/private/storage/db"
)

var _ aliasdb.DB = (*Backend)(nil)

type Backend struct {
	db *sql.DB
	*executor
}

func New(path string) (*Backend, error) {
	db, err := db.NewSqlite(path, Schema, SchemaVersion)
	if err != nil {
		return nil, err
	}
	return &Backend{
		db: db,
		executor: &executor{
			db: db,
		},
	}, nil
}

func (b *Backend) Close() error {
	return b.db.Close()
}

func (b *Backend) SetMaxOpenConns(maxOpenConns int) {
	b.db.SetMaxOpenConns(maxOpenConns)
}
func (b *Backend) SetMaxIdleConns(maxIdleConns int) {
	b.db.SetMaxIdleConns(maxIdleConns)
}

func (b *Backend) BeginTransaction(ctx context.Context,
	opts *sql.TxOptions) (aliasdb.Transaction, error) {

	b.Lock()
	defer b.Unlock()
	tx, err := b.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, serrors.Wrap("Failed to create transaction", err)
	}
	return &transaction{
		executor: &executor{
			db: tx,
		},
		tx: tx,
	}, nil
}

var _ aliasdb.Transaction = (*transaction)(nil)

type transaction struct {
	*executor
	tx *sql.Tx
}

func (tx *transaction) Commit() error {
	tx.Lock()
	defer tx.Unlock()
	return tx.tx.Commit()
}

func (tx *transaction) Rollback() error {
	tx.Lock()
	defer tx.Unlock()
	return tx.tx.Rollback()
}

var _ aliasdb.ReadWrite = (*executor)(nil)

type executor struct {
	sync.RWMutex
	db db.Sqler
}

func (e *executor) Get(ctx context.Context, hostname string) ([]alias.Replica, error) {
	return nil, nil
}

// AddReplicas adds aliases to a given hostname, valid until notAfter.
func (e *executor) AddReplicas(
	ctx context.Context,
	hostname string,
	notAfter time.Time,
	replicas []alias.Replica,
) error {
	e.Lock()
	defer e.Unlock()

	return db.DoInTx(ctx, e.db, func(ctx context.Context, tx *sql.Tx) error {
		return addReplicas(ctx, tx, hostname, notAfter, replicas)
	})
}

func (e *executor) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	e.Lock()
	defer e.Unlock()

	var count int
	err := db.DoInTx(ctx, e.db, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		count, err = deleteExpired(ctx, tx, now)
		return err
	})

	return count, err
}

// Delete removes the specified aliases of the hostname.
func (e *executor) Delete(ctx context.Context, hostname string, replicas []alias.Replica) (int, error) {
	e.Lock()
	defer e.Unlock()

	var count int
	err := db.DoInTx(ctx, e.db, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		count, err = delete(ctx, tx, hostname, replicas)
		return err
	})

	return count, err
}

// DeleteAll removes all aliases for a given hostname.
func (e *executor) DeleteAll(ctx context.Context, hostname string) (int, error) {
	e.Lock()
	defer e.Unlock()

	var count int
	err := db.DoInTx(ctx, e.db, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		count, err = deleteAll(ctx, tx, hostname)
		return err
	})

	return count, err
}

func addReplicas(
	ctx context.Context,
	tx *sql.Tx,
	hostname string,
	notAfter time.Time,
	replicas []alias.Replica,
) error {
	str := "INSERT INTO Aliases (Hostname, AliasIA, AliasHostname, NotAfter) VALUES (?,?,?,?)"
	stmt, err := tx.PrepareContext(ctx, str)
	if err != nil {
		return err
	}

	notAfterSecs := util.TimeToSecs(notAfter)
	for _, rep := range replicas {
		_, err := stmt.ExecContext(ctx, hostname, rep.IA, rep.Hostname, notAfterSecs)
		if err != nil {
			return serrors.Wrap("inserting alias", err, "host", hostname,
				"ia", rep.IA.String(), "address", rep.Hostname)
		}
	}
	return nil
}

func deleteExpired(
	ctx context.Context,
	tx *sql.Tx,
	now time.Time,
) (int, error) {
	str := "DELETE FROM Aliases WHERE NotAfter >= ?"
	res, err := tx.ExecContext(ctx, str, now.Unix())
	if err != nil {
		return 0, err
	}
	c, err := res.RowsAffected()
	return int(c), err
}

func delete(
	ctx context.Context,
	tx *sql.Tx,
	hostname string,
	replicas []alias.Replica,
) (int, error) {
	str := "DELETE FROM Aliases WHERE Hostname = ? AND AliasIA = ? AND AliasHostname = ?"
	stmt, err := tx.PrepareContext(ctx, str)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, rep := range replicas {
		res, err := stmt.ExecContext(ctx, hostname, rep.IA, rep.Hostname)
		if err != nil {
			return 0, err
		}
		c, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		count += int(c)
	}
	return count, nil
}

func deleteAll(
	ctx context.Context,
	tx *sql.Tx,
	hostname string,
) (int, error) {
	str := "DELETE FROM Aliases WHERE Hostname = ?"
	res, err := tx.ExecContext(ctx, str, hostname)
	if err != nil {
		return 0, err
	}
	c, err := res.RowsAffected()
	return int(c), err
}
