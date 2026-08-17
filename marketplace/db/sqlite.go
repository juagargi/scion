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
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/private/storage/db"
)

// Repository contains the database operations that can run both directly and
// inside a transaction.
type Repository interface {
	InsertAsset(ctx context.Context, a *DBAsset) (int64, error)
	InsertReservation(ctx context.Context, r *DBReservation) (int64, error)
	Search(ctx context.Context, params *AssetQuery) ([]*DBAsset, error)
	FetchReservations(ctx context.Context, params *ReservationQuery) ([]*DBReservation, error)
	GetUser(ctx context.Context, id int64) (*DBUser, error)
	GetUserByName(ctx context.Context, name string) (*DBUser, error)
	GetAccountsByUser(ctx context.Context, id int64) ([]*DBAccount, error)
	GetAccountByAccountID(ctx context.Context, id int64) (*DBAccount, error)
	CreateAccount(ctx context.Context, account *DBAccount) (int64, error)
	CreateASUser(ctx context.Context, user *DBASUser) (int64, error)
	UpdateAccountMoney(ctx context.Context, id int64, amount int64) (int64, error)
	UpdateASMoney(ctx context.Context, ia addr.IA, amount int64) (int64, error)
	SearchAssetsForStatistics(ctx context.Context, params *StatisticsQuery) ([]*DBStat, error)
	FindUsedReservations(ctx context.Context, params *UsedReservationsQuery) ([]*UsedReservation, error)
	CreateOrUpdateRedemptionDelegations(ctx context.Context, r *RedemptionDelegation) (int64, error)
	FindRedemptionDelegations(ctx context.Context) ([]*RedemptionDelegation, error)
	IncrementASJWTVersion(ctx context.Context, ia addr.IA, current int64) (int64, error)
	IncrementUserJWTVersion(ctx context.Context, userId int64, accountId int64) (int64, error)
	IncrementAccountJWTVersion(ctx context.Context, userid int64, current int64) (int64, error)
	GetASUser(ctx context.Context, ia addr.IA) (*DBASUser, error)
	DeleteListedAsset(ctx context.Context, ia addr.IA, assetID int64) (int64, error)
	SetASAuthenticationToken(ctx context.Context, ia addr.IA, auth string) (int64, error)
	FindRedemptionDelegation(ctx context.Context, ia addr.IA) (*RedemptionDelegation, error)
	CreateUser(ctx context.Context, user *DBUser) (int64, error)
	UpdateAccountMoneyWithUser(ctx context.Context, userID int64, accountID int64, amount int64) (int64, error)
	TransferAllAssetsToAccount(ctx context.Context, accountIDFrom int64, accountIDTo int64) (int64, error)
	TransferAllReservations(ctx context.Context, accountIDFrom int64, accountIDTo int64) (int64, error)
	DeleteAccount(ctx context.Context, accountID int64) (int64, error)
	AssignAsset(ctx context.Context, assetID int64, accountIDFrom int64, accountIDTo int64) (int64, error)
	AssignReservation(ctx context.Context, id int64, accountIDFrom int64, accountIDTo int64) (int64, error)
	RemoveAsset(ctx context.Context, assetID int64) error
	TransitionAsset(ctx context.Context, assetID int64, accountID *int64, from AssetState, to AssetState) (*DBAsset, error)
}

type MarketplaceDB interface {
	io.Closer
	Repository
	WithTx(ctx context.Context, fn func(Repository) error) error
}

type Backend struct {
	db *db.Sqlite
	*executor
}

type executor struct {
	write db.Sqler
	read  interface {
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	}
}

func New(path string, cfg *db.SqliteConfig) (*Backend, error) {
	db, err := db.NewSqlite(path, cfg)
	if err != nil {
		return nil, err
	}
	if err := db.Setup(Schema, SchemaVersion); err != nil {
		return nil, err
	}
	return &Backend{
		executor: &executor{
			write: db.Full,
			read:  db.ReadOnly,
		},
		db: db,
	}, nil
}

func (b *Backend) Close() error {
	return b.db.Close()
}

func (b *Backend) WithTx(
	ctx context.Context,
	fn func(Repository) error,
) (err error) {
	tx, err := b.db.Full.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	repository := &executor{
		write: tx,
		read:  tx,
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = serrors.Join(err, rollbackErr)
		}
	}()
	if err = fn(repository); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

var _ Repository = (*executor)(nil)
var _ MarketplaceDB = (*Backend)(nil)

const (
	assetColumns = `id, account_id, isd_id, as_id, bandwidth, bandwidth_min, bandwidth_max, price,
	time_granularity, time_min_duration, starts_at, stops_at, ingress, egress`
	assetColumnsWithAlias = `a.id, a.account_id, a.isd_id, a.as_id, a.bandwidth, a.bandwidth_min,
	a.bandwidth_max, a.price, a.time_granularity, a.time_min_duration, a.starts_at, a.stops_at,
	a.ingress, a.egress`
)

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAsset(row rowScanner) (*DBAsset, error) {
	asset := &DBAsset{}
	var startsAt string
	var stopsAt string
	var isd uint16
	var as uint64
	if err := row.Scan(
		&asset.ID,
		&asset.AccountId,
		&isd,
		&as,
		&asset.Bandwidth,
		&asset.BandwidthMin,
		&asset.BandwidthMax,
		&asset.Price,
		&asset.TimeGranularity,
		&asset.TimeMinDuration,
		&startsAt,
		&stopsAt,
		&asset.IfIdIngress,
		&asset.IfIdEgress,
	); err != nil {
		return nil, serrors.Wrap("Error reading DB response", err)
	}
	var err error
	asset.IA, err = addr.IAFrom(addr.ISD(isd), addr.AS(as))
	if err != nil {
		return nil, err
	}
	asset.StartAt, err = time.Parse(time.RFC3339, startsAt)
	if err != nil {
		return nil, err
	}
	asset.StopsAt, err = time.Parse(time.RFC3339, stopsAt)
	if err != nil {
		return nil, err
	}
	return asset, nil
}

func scanReservation(row rowScanner) (*DBReservation, error) {
	reservation := &DBReservation{}
	var startsAt string
	var stopsAt string
	var isd uint16
	var as uint64
	if err := row.Scan(
		&reservation.ID,
		&reservation.AccountId,
		&reservation.ReservationID,
		&isd,
		&as,
		&reservation.Ingress,
		&reservation.Egress,
		&reservation.Bandwidth,
		&reservation.EncodedBandwidth,
		&startsAt,
		&stopsAt,
		&reservation.Key,
	); err != nil {
		return nil, serrors.Wrap("Error reading DB response", err)
	}
	var err error
	reservation.IA, err = addr.IAFrom(addr.ISD(isd), addr.AS(as))
	if err != nil {
		return nil, err
	}
	reservation.StartsAt, err = time.Parse(time.RFC3339, startsAt)
	if err != nil {
		return nil, err
	}
	reservation.StopsAt, err = time.Parse(time.RFC3339, stopsAt)
	if err != nil {
		return nil, err
	}
	return reservation, nil
}

func (e *executor) FetchReservations(
	ctx context.Context,
	params *ReservationQuery,
) ([]*DBReservation, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	stmt, args := e.buildReservationQuery(params)
	rows, err := e.read.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, serrors.New("Error looking up assets", "err", err, "q", stmt)
	}
	defer rows.Close()
	var res []*DBReservation
	for rows.Next() {
		a, err := scanReservation(rows)
		if err != nil {
			return nil, err
		}
		res = append(res, a)
	}
	return res, nil
}

func (e *executor) buildReservationQuery(params *ReservationQuery) (string, []any) {
	var args []any
	where := []string{}
	query := []string{
		"SELECT r.id, r.account_id, r.reservation_id, r.isd_id, r.as_id, r.ingress, r.egress, " +
			"r.bandwidth, r.bw_encoded, r.starts_at, r.stops_at, r.key FROM Reservations r",
	}
	query = append(query, "JOIN Accounts owner ON r.account_id = owner.id JOIN "+
		"Accounts current ON current.id = ?")
	where = append(where, "( (current.scope = '' AND owner.user_id = current.user_id) OR "+
		"(current.scope != '' AND owner.id = current.id) )")
	args = append(args, params.AccountId)
	if params.IA != nil {
		where = append(where, "(r.isd_id=?) AND (r.as_id=?)")
		args = append(args, int64(params.IA.ISD()), int64(params.IA.AS()))
	}
	if params.StartsAt != nil {
		where = append(where, "(r.starts_at<=?)")
		args = append(args, *params.StartsAt)
	}
	if params.StopsAt != nil {
		where = append(where, "(r.stops_at>=?)")
		args = append(args, *params.StopsAt)
	}
	if params.Ingress != nil {
		where = append(where, "(r.ingress=?)")
		args = append(args, *params.Ingress)
	}
	if params.Egress != nil {
		where = append(where, "(r.egress=?)")
		args = append(args, *params.Egress)
	}
	if params.Bandwidth != nil {
		where = append(where, "(r.bandwidth>=?)")
		args = append(args, *params.Bandwidth)
	}
	query = append(query, fmt.Sprintf("WHERE %s", strings.Join(where, "AND\n")))
	return strings.Join(query, "\n"), args
}

func (e *executor) FindRedemptionDelegations(
	ctx context.Context,
) ([]*RedemptionDelegation, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	stmt := `SELECT isd_id, as_id, res_id_limit_low, res_id_limit_high, expiration, paid_until, key, encodings
			 FROM Redemption_Delegations WHERE expiration >= ?`
	args := []any{time.Now().UTC().Format(time.RFC3339)}
	rows, err := e.read.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, serrors.New("Error looking up assets", "err", err, "q", stmt)
	}
	defer rows.Close()
	var res []*RedemptionDelegation
	for rows.Next() {
		a := &RedemptionDelegation{}
		var expirationString string
		var paidUntilString string
		var isd uint16
		var as uint64
		err = rows.Scan(
			&isd,
			&as,
			&a.ResIdLow,
			&a.ResIdHigh,
			&expirationString,
			&paidUntilString,
			&a.Key,
			&a.Encodings)
		if err != nil {
			return nil, serrors.Wrap("Error reading DB response", err)
		}
		a.Expiration, err = time.Parse(time.RFC3339, expirationString)
		if err != nil {
			return nil, err
		}
		a.PaidUntil, err = time.Parse(time.RFC3339, paidUntilString)
		if err != nil {
			return nil, err
		}
		a.IA, err = addr.IAFrom(addr.ISD(isd), addr.AS(as))
		res = append(res, a)
	}
	return res, nil
}

func (e *executor) FindRedemptionDelegation(
	ctx context.Context,
	ia addr.IA,
) (*RedemptionDelegation, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	stmt := `SELECT isd_id, as_id, res_id_limit_low, res_id_limit_high, expiration, paid_until, key, encodings
			 FROM Redemption_Delegations
			 WHERE expiration >= ? AND isd_id = ? AND as_id = ?`
	rows, err := e.read.QueryContext(ctx, stmt, time.Now().Format(time.RFC3339), ia.ISD(), ia.AS())
	if err != nil {
		return nil, serrors.New("Error looking up assets", "err", err, "q", stmt)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	a := &RedemptionDelegation{}
	var expirationString string
	var paidUntilString string
	var isd uint16
	var as uint64
	err = rows.Scan(
		&isd,
		&as,
		&a.ResIdLow,
		&a.ResIdHigh,
		&expirationString,
		&paidUntilString,
		&a.Key,
		&a.Encodings)
	if err != nil {
		return nil, serrors.Wrap("Error reading DB response", err)
	}
	a.Expiration, err = time.Parse(time.RFC3339, expirationString)
	if err != nil {
		return nil, err
	}
	a.PaidUntil, err = time.Parse(time.RFC3339, paidUntilString)
	if err != nil {
		return nil, err
	}
	a.IA, err = addr.IAFrom(addr.ISD(isd), addr.AS(as))
	return a, nil
}

func (e *executor) IncrementASJWTVersion(
	ctx context.Context,
	ia addr.IA,
	current int64,
) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	q := `UPDATE Ases SET jwt_version = jwt_version + 1
		  WHERE isd_id = ? AND as_id = ? AND jwt_version = ? RETURNING jwt_version`
	rows, err := e.write.QueryContext(ctx, q, ia.ISD(), ia.AS(), current)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, serrors.New("AS not found", "ia", ia)
	}
	var newVersion int64
	err = rows.Scan(&newVersion)
	if err != nil {
		return 0, serrors.Wrap("Error reading DB response", err)
	}
	return newVersion, nil
}

func (e *executor) IncrementUserJWTVersion(
	ctx context.Context,
	userId int64,
	accountId int64,
) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	q := `UPDATE Accounts SET jwt_version = jwt_version + 1
		  WHERE id = ? AND user_id = ? RETURNING jwt_version`
	rows, err := e.write.QueryContext(ctx, q, accountId, userId)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, serrors.New("Account not found", "accountid", accountId)
	}
	var newVersion int64
	err = rows.Scan(&newVersion)
	if err != nil {
		return 0, serrors.Wrap("Error reading DB response", err)
	}
	return newVersion, nil
}

func (e *executor) IncrementAccountJWTVersion(
	ctx context.Context,
	accountId int64,
	current int64,
) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	q := `UPDATE Accounts SET jwt_version = jwt_version + 1
		  WHERE id = ? AND jwt_version = ? RETURNING jwt_version`
	rows, err := e.write.QueryContext(ctx, q, accountId, current)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, serrors.New("Account not found", "accountid", accountId)
	}
	var newVersion int64
	err = rows.Scan(&newVersion)
	if err != nil {
		return 0, serrors.Wrap("Error reading DB response", err)
	}
	return newVersion, nil
}

func (e *executor) CreateOrUpdateRedemptionDelegations(
	ctx context.Context,
	r *RedemptionDelegation,
) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	q := `INSERT INTO Redemption_Delegations
		  (isd_id, as_id, res_id_limit_low, res_id_limit_high, expiration, paid_until, key, encodings)
		  VALUES (?,?,?,?,?,?,?,?)
	ON CONFLICT(isd_id, as_id)
	DO UPDATE SET
		expiration = excluded.expiration,
		paid_until = excluded.paid_until,
		key = excluded.key,
		encodings = excluded.encodings,
		res_id_limit_low = excluded.res_id_limit_low
		res_id_limit_high = excluded.res_id_limit_high;`
	res, err := e.write.ExecContext(ctx, q,
		r.IA.ISD(),
		r.IA.AS(),
		r.ResIdLow,
		r.ResIdHigh,
		r.Expiration.UTC().Format(time.RFC3339),
		r.PaidUntil.UTC().Format(time.RFC3339),
		r.Key,
		r.Encodings)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (e *executor) FindUsedReservations(
	ctx context.Context,
	params *UsedReservationsQuery,
) ([]*UsedReservation, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	stmt, args := e.buildUsedReservationsQuery(params)
	rows, err := e.read.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, serrors.New("Error looking up assets", "err", err, "q", stmt)
	}
	defer rows.Close()
	var res []*UsedReservation
	for rows.Next() {
		a := &UsedReservation{}
		var startsAtString string
		var stopsAtString string
		err = rows.Scan(&a.Id, &startsAtString, &stopsAtString)
		if err != nil {
			return nil, serrors.Wrap("Error reading DB response", err)
		}
		a.StartsAt, err = time.Parse(time.RFC3339, startsAtString)
		if err != nil {
			return nil, err
		}
		a.StopsAt, err = time.Parse(time.RFC3339, stopsAtString)
		if err != nil {
			return nil, err
		}
		res = append(res, a)
	}
	return res, nil
}

func (e *executor) buildUsedReservationsQuery(params *UsedReservationsQuery) (string, []any) {
	query := `SELECT id, starts_at, stops_at FROM Reservations
			  WHERE (isd_id = ?) AND (as_id = ?) AND (id >= ?) AND (id < ?) AND (stops_at > ?)`

	args := []any{
		params.IA.ISD(),
		params.IA.AS(),
		params.Limit_low,
		params.Limit_high,
		time.Now().UTC().Format(time.RFC3339),
	}
	return query, args
}

func (e *executor) SearchAssetsForStatistics(
	ctx context.Context,
	params *StatisticsQuery,
) ([]*DBStat, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	stmt, args := e.buildStatisticsQuery(params)
	rows, err := e.read.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, serrors.New("Error looking up assets", "err", err, "q", stmt)
	}
	defer rows.Close()
	var res []*DBStat
	for rows.Next() {
		a := &DBStat{}
		var startsAtString string
		var stopsAtString string
		err = rows.Scan(&a.OwnerId, &a.Bandwidth, &a.Price, &startsAtString, &stopsAtString)
		if err != nil {
			return nil, serrors.Wrap("Error reading DB response", err)
		}
		a.StartsAt, err = time.Parse(time.RFC3339, startsAtString)
		if err != nil {
			return nil, err
		}
		a.StopsAt, err = time.Parse(time.RFC3339, stopsAtString)
		if err != nil {
			return nil, err
		}
		res = append(res, a)
	}
	return res, nil
}

func (e *executor) buildStatisticsQuery(params *StatisticsQuery) (string, []any) {
	var args []any
	where := []string{}
	query := []string{
		"SELECT account_id, bandwidth, price, starts_at, stops_at FROM Assets",
	}
	where = append(where, "(isd_id=?) AND (as_id=?) AND (stops_at > ?) AND (starts_at <= ?)")
	args = append(args,
		int64(params.IA.ISD()),
		int64(params.IA.AS()),
		params.WindowStart,
		params.WindowEnd)
	if params.Ingress != nil {
		where = append(where, "(ingress=?)")
		args = append(args, *params.Ingress)
	}
	if params.Egress != nil {
		where = append(where, "(egress=?)")
		args = append(args, *params.Egress)
	}
	query = append(query, fmt.Sprintf("WHERE %s", strings.Join(where, "AND\n")))
	return strings.Join(query, "\n"), args
}

func (e *executor) Search(ctx context.Context, params *AssetQuery) ([]*DBAsset, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	stmt, args := e.buildSearchQuery(params)
	rows, err := e.read.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, serrors.New("Error looking up assets", "err", err, "q", stmt)
	}
	defer rows.Close()
	var res []*DBAsset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		res = append(res, a)
	}
	return res, nil
}

func (e *executor) buildSearchQuery(params *AssetQuery) (string, []any) {
	var args []any
	where := []string{}
	query := []string{
		"SELECT " + assetColumnsWithAlias + " FROM Assets a",
	}
	if params.AccountId == nil {
		where = append(where, "(a.account_id IS NULL)")
	} else {
		// The JOIN clause will be the first parametrized entry. Fill up the arguments for it.
		query = append(query, "JOIN Accounts owner ON a.account_id = owner.id JOIN "+
			"Accounts current ON current.id = ?")
		args = append(args, *params.AccountId)
		// Parenthesized as a whole: AND to all where[i] binds tighter than OR.
		where = append(where, "( (current.scope = '' AND owner.user_id = current.user_id) OR "+
			"(current.scope != '' AND owner.id = current.id) )")
	}
	where = append(where, "(a.state = ?)")
	args = append(args, AssetStateAvailable)
	if params.IA != nil {
		where = append(where, "(a.isd_id=?) AND (a.as_id=?)")
		args = append(args, int64(params.IA.ISD()), int64(params.IA.AS()))
	}
	if params.StartsAt != nil {
		where = append(where, "(a.starts_at<=?)")
		args = append(args, *params.StartsAt)
	}
	if params.StopsAt != nil {
		where = append(where, "(a.stops_at>=?)")
		args = append(args, *params.StopsAt)
	}
	if params.Price != nil {
		where = append(where, "(a.price<=?)")
		args = append(args, *params.Price)
	}
	if params.MinRequiredBandwidth != nil {
		where = append(where, "(a.bandwidth>=?)")
		args = append(args, *params.MinRequiredBandwidth)
	}
	if params.Ingress != nil {
		where = append(where, "(a.ingress=?)")
		args = append(args, *params.Ingress)
	}
	if params.Egress != nil {
		where = append(where, "(a.egress=?)")
		args = append(args, *params.Egress)
	}
	query = append(query, fmt.Sprintf("WHERE %s", strings.Join(where, "AND\n")))
	query = append(query, "ORDER BY LENGTH(a.id) ASC, a.id ASC")
	query = append(query, "LIMIT ? OFFSET ?")
	args = append(args, params.PageSize, params.Page*params.PageSize)
	return strings.Join(query, "\n"), args
}

func (e *executor) TransitionAsset(
	ctx context.Context,
	assetID int64,
	accountID *int64,
	from AssetState,
	to AssetState,
) (*DBAsset, error) {
	if e.write == nil {
		return nil, serrors.New("No database open")
	}
	ownerClause := "account_id IS NULL"
	args := []any{to, assetID, from}
	if accountID != nil {
		ownerClause = "account_id = ?"
		args = append(args, *accountID)
	}
	q := fmt.Sprintf(`
	UPDATE Assets
	SET state = ?
	WHERE id = ?
	AND state = ?
	AND %s
	RETURNING %s`, ownerClause, assetColumns)
	rows, err := e.write.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, serrors.New("asset not found", "id", assetID)
	}
	return scanAsset(rows)
}

func (e *executor) InsertReservation(ctx context.Context, r *DBReservation) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	q := `INSERT INTO Reservations
	      (reservation_id, isd_id, as_id, ingress, egress, bandwidth, bw_encoded,
		  starts_at, stops_at, key, account_id)
		  VALUES(?,?,?,?,?,?,?,?,?,?,?)`
	res, err := e.write.ExecContext(ctx, q,
		r.ReservationID,
		r.IA.ISD(),
		r.IA.AS(),
		r.Ingress,
		r.Egress,
		r.Bandwidth,
		r.EncodedBandwidth,
		r.StartsAt.UTC().Format(time.RFC3339),
		r.StopsAt.UTC().Format(time.RFC3339),
		r.Key,
		r.AccountId)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (e *executor) InsertAsset(ctx context.Context, a *DBAsset) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	var res sql.Result
	var err error
	if a.AccountId.Valid {
		inst := `INSERT INTO Assets (isd_id, as_id, bandwidth, bandwidth_min, bandwidth_max,
				 price, time_granularity, time_min_duration, starts_at, stops_at,
				 ingress, egress, account_id)
				 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`
		res, err = e.write.ExecContext(ctx, inst,
			a.IA.ISD(),
			a.IA.AS(),
			a.Bandwidth,
			a.BandwidthMin,
			a.BandwidthMax,
			a.Price,
			a.TimeGranularity,
			a.TimeMinDuration,
			a.StartAt.UTC().Format(time.RFC3339),
			a.StopsAt.UTC().Format(time.RFC3339),
			a.IfIdIngress,
			a.IfIdEgress,
			a.AccountId)
	} else {
		inst := `INSERT INTO Assets (isd_id, as_id, bandwidth, bandwidth_min, bandwidth_max,
				 price, time_granularity, time_min_duration, starts_at, stops_at, ingress, egress)
				 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`
		res, err = e.write.ExecContext(ctx, inst,
			a.IA.ISD(), a.IA.AS(),
			a.Bandwidth,
			a.BandwidthMin,
			a.BandwidthMax,
			a.Price,
			a.TimeGranularity,
			a.TimeMinDuration,
			a.StartAt.UTC().Format(time.RFC3339),
			a.StopsAt.UTC().Format(time.RFC3339),
			a.IfIdIngress,
			a.IfIdEgress)
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (e *executor) DeleteListedAsset(
	ctx context.Context,
	ia addr.IA,
	assetID int64,
) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `DELETE FROM Assets
			 WHERE id = ? AND isd_id = ? AND as_id = ? AND account_id IS NULL AND state = ?`
	res, err := e.write.ExecContext(ctx, inst, assetID, ia.ISD(), ia.AS(), AssetStateAvailable)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (e *executor) RemoveAsset(ctx context.Context, assetID int64) error {
	if e.write == nil {
		return serrors.New("No database open")
	}
	inst := `DELETE FROM Assets WHERE id = ?`
	_, err := e.write.ExecContext(ctx, inst, assetID)
	return err
}

func (e *executor) GetUser(ctx context.Context, id int64) (*DBUser, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	q := `SELECT id, name, pw_hash FROM Users WHERE id=?`
	rows, err := e.read.QueryContext(ctx, q, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	user := &DBUser{}
	err = rows.Scan(&user.ID, &user.Name, &user.PasswordHash)
	if err != nil {
		return nil, serrors.Wrap("Error reading DB response", err)
	}
	return user, nil
}
func (e *executor) GetUserByName(ctx context.Context, name string) (*DBUser, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	q := `SELECT id, name, pw_hash FROM Users WHERE name=?`
	rows, err := e.read.QueryContext(ctx, q, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	user := &DBUser{}
	err = rows.Scan(&user.ID, &user.Name, &user.PasswordHash)
	if err != nil {
		return nil, serrors.Wrap("Error reading DB response", err)
	}
	return user, nil
}

func (e *executor) GetAccountByAccountID(ctx context.Context, id int64) (*DBAccount, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	q := `SELECT id, user_id, scope, balance, jwt_version FROM Accounts WHERE id = ?`
	rows, err := e.read.QueryContext(ctx, q, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, serrors.New("account not found")
	}
	account := &DBAccount{}
	err = rows.Scan(
		&account.ID,
		&account.UserID,
		&account.Scope,
		&account.Balance,
		&account.TokenVersion)
	if err != nil {
		return nil, serrors.Wrap("Error reading DB response", err)
	}
	return account, nil
}

func (e *executor) GetAccountsByUser(ctx context.Context, id int64) ([]*DBAccount, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	q := `SELECT id, scope, balance, jwt_version FROM Accounts WHERE user_id = ?`
	rows, err := e.read.QueryContext(ctx, q, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ret := []*DBAccount{}
	for rows.Next() {
		account := &DBAccount{}
		err = rows.Scan(&account.ID, &account.Scope, &account.Balance, &account.TokenVersion)
		if err != nil {
			return nil, serrors.Wrap("Error reading DB response", err)
		}
		ret = append(ret, account)
	}
	return ret, nil
}

func (e *executor) CreateAccount(ctx context.Context, account *DBAccount) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `INSERT INTO Accounts (user_id, scope, balance) VALUES(?,?, ?)`
	res, err := e.write.ExecContext(ctx, inst, account.UserID, account.Scope, account.Balance)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (e *executor) CreateUser(ctx context.Context, user *DBUser) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `INSERT INTO Users (name, pw_hash)
	VALUES(?,?) ON CONFLICT(name) DO NOTHING`
	res, err := e.write.ExecContext(ctx, inst, user.Name, user.PasswordHash)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (e *executor) CreateASUser(ctx context.Context, user *DBASUser) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `INSERT INTO Ases (isd_id, as_id) VALUES(?,?) ON CONFLICT DO NOTHING`
	res, err := e.write.ExecContext(ctx, inst, user.IA.ISD(), user.IA.AS())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (e *executor) GetASUser(ctx context.Context, ia addr.IA) (*DBASUser, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	q := `SELECT isd_id, as_id, pw_hash, jwt_version, balance
		  FROM Ases WHERE isd_id=? AND as_id=?`
	rows, err := e.read.QueryContext(ctx, q, ia.ISD(), ia.AS())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	user := &DBASUser{}
	var isd uint16
	var as uint64
	err = rows.Scan(&isd, &as, &user.PasswordHash, &user.TokenVersion, &user.Balance)
	if err != nil {
		return nil, serrors.Wrap("Error reading DB response", err)
	}
	user.IA, err = addr.IAFrom(ia.ISD(), addr.AS(as))
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (e *executor) UpdateAccountMoneyWithUser(
	ctx context.Context,
	userId int64,
	accountId int64,
	amount int64,
) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `UPDATE Accounts SET balance = balance + ? WHERE id = ? AND user_id = ?`
	res, err := e.write.ExecContext(ctx, inst, amount, accountId, userId)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (e *executor) UpdateAccountMoney(
	ctx context.Context,
	id int64,
	amount int64,
) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `UPDATE Accounts SET balance = balance + ? WHERE id = ?`
	res, err := e.write.ExecContext(ctx, inst, amount, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (e *executor) UpdateASMoney(
	ctx context.Context,
	ia addr.IA,
	amount int64,
) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `UPDATE Ases SET balance = balance + ? WHERE isd_id = ? AND as_id = ?`
	res, err := e.write.ExecContext(ctx, inst, amount, ia.ISD(), ia.AS())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
func (e *executor) SetASAuthenticationToken(
	ctx context.Context,
	ia addr.IA,
	auth string,
) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `UPDATE Ases SET pw_hash = ? WHERE isd_id = ? AND as_id = ?`
	res, err := e.write.ExecContext(ctx, inst, auth, ia.ISD(), ia.AS())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (e *executor) TransferAllAssetsToAccount(
	ctx context.Context,
	accountIdFrom int64,
	accountIdTo int64,
) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `UPDATE Assets SET account_id = ? WHERE account_id = ?`
	res, err := e.write.ExecContext(ctx, inst, accountIdTo, accountIdFrom)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (e *executor) TransferAllReservations(
	ctx context.Context,
	accountIdFrom int64,
	accountIdTo int64,
) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `UPDATE Reservations SET account_id = ? WHERE account_id = ?`
	res, err := e.write.ExecContext(ctx, inst, accountIdTo, accountIdFrom)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (e *executor) DeleteAccount(ctx context.Context, accountID int64) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `DELETE FROM Accounts WHERE id = ? AND scope != ''`
	res, err := e.write.ExecContext(ctx, inst, accountID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (e *executor) AssignAsset(
	ctx context.Context,
	assetID int64,
	accountIDFrom int64,
	accountIDTo int64,
) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `UPDATE Assets SET account_id = ? WHERE id=? AND account_id = ?`
	res, err := e.write.ExecContext(ctx, inst, accountIDTo, assetID, accountIDFrom)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (e *executor) AssignReservation(
	ctx context.Context,
	id int64,
	accountIDFrom int64,
	accountIDTo int64,
) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `UPDATE Reservations SET account_id = ? WHERE id=? AND account_id = ?`
	res, err := e.write.ExecContext(ctx, inst, accountIDTo, id, accountIDFrom)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
