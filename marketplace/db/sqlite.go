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
	SearchOwnedAssets(ctx context.Context, params *OwnedAssetQuery) ([]*DBAsset, error)
	SearchListedAssets(ctx context.Context, params *ListedAssetQuery) ([]*DBAsset, error)
	LookupAsset(ctx context.Context, id int64, ia addr.IA) (*DBAsset, error)
	LookupAssets(ctx context.Context, ia addr.IA) ([]*DBAsset, error)
	LookupAllAssetSegments(ctx context.Context, id int64) ([]*DBAssetSegment, error)
	FetchReservations(ctx context.Context, params *ReservationQuery) ([]*DBReservation, error)
	GetUser(ctx context.Context, id int64) (*DBUser, error)
	GetUserByName(ctx context.Context, name string) (*DBUser, error)
	GetAccountsByUser(ctx context.Context, id int64) ([]*DBAccount, error)
	GetAccountByAccountID(ctx context.Context, id int64) (*DBAccount, error)
	CreateAccount(ctx context.Context, account *DBAccount) (int64, error)
	CreateASUser(ctx context.Context, user *DBASUser) (int64, error)
	UpdateAccountMoney(ctx context.Context, id int64, amount int64) (int64, error)
	UpdateASMoney(ctx context.Context, ia addr.IA, amount int64) (int64, error)
	SearchAssetsForStatistics(ctx context.Context, params *StatisticsQuery) ([]*DBStat, []*DBStat, error)
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
	RegisterAssetEvent(ctx context.Context, a *DBAsset, eventType AssetEventType) (int64, error)
	TransitionAsset(ctx context.Context, assetID int64, accountID *int64, from AssetState, to AssetState) (*DBAsset, error)
	InsertAssetSegment(ctx context.Context, asset_id int64, from int64, to int64, bw uint32) (int64, error)
	CheckAssetAvailability(ctx context.Context, assetId int64, startsAt int64, stopsAt int64, bw uint32) (*DBAsset, error)
	SplitAssetAt(ctx context.Context, assetId int64, point int64) error
	MergeAssetAt(ctx context.Context, assetId int64, point int64) error
	IncreaseAssetSegmentsBandwidth(ctx context.Context, assetId int64, delta int64) error
	UpdateAsset(ctx context.Context, a *DBAsset) (int64, error)
	BuyAsset(ctx context.Context, startsAt int64, stopsAt int64, bw uint32, accountId int64, dbAsset *DBAsset) (*DBAsset, error)
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
	time_granularity, time_min_duration, time_max_duration, starts_at, stops_at, ingress, egress`
	assetColumnsWithAlias = `a.id, a.account_id, a.isd_id, a.as_id, a.bandwidth, a.bandwidth_min,
	a.bandwidth_max, a.price, a.time_granularity, a.time_min_duration, a.time_max_duration, a.starts_at, a.stops_at,
	a.ingress, a.egress`
)

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAssetWithAvailableBandwidthAndUsability(row rowScanner) (*DBAsset, error) {
	asset := &DBAsset{}
	var isd uint16
	var as uint64
	var usableFrom int64
	var usableTo int64
	var availableBW uint32
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
		&asset.TimeMaxDuration,
		&asset.StartsAt,
		&asset.StopsAt,
		&asset.IfIdIngress,
		&asset.IfIdEgress,
		&usableFrom,
		&usableTo,
		&availableBW,
	); err != nil {
		return nil, serrors.Wrap("Error reading DB response", err)
	}
	var err error
	asset.IA, err = addr.IAFrom(addr.ISD(isd), addr.AS(as))
	if err != nil {
		return nil, err
	}
	asset.Bandwidth = availableBW
	asset.StartsAt = usableFrom
	asset.StopsAt = usableTo
	return asset, nil
}

func scanAssetWithAvailableBandwidth(row rowScanner) (*DBAsset, error) {
	asset := &DBAsset{}
	var isd uint16
	var as uint64
	var availableBW uint32
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
		&asset.TimeMaxDuration,
		&asset.StartsAt,
		&asset.StopsAt,
		&asset.IfIdIngress,
		&asset.IfIdEgress,
		&availableBW,
	); err != nil {
		return nil, serrors.Wrap("Error reading DB response", err)
	}
	var err error
	asset.IA, err = addr.IAFrom(addr.ISD(isd), addr.AS(as))
	if err != nil {
		return nil, err
	}
	asset.Bandwidth = availableBW
	return asset, nil
}

func scanAsset(row rowScanner) (*DBAsset, error) {
	asset := &DBAsset{}
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
		&asset.TimeMaxDuration,
		&asset.StartsAt,
		&asset.StopsAt,
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
	return asset, nil
}

func scanReservation(row rowScanner) (*DBReservation, error) {
	reservation := &DBReservation{}
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
		&reservation.StartsAt,
		&reservation.StopsAt,
		&reservation.Key,
	); err != nil {
		return nil, serrors.Wrap("Error reading DB response", err)
	}
	var err error
	reservation.IA, err = addr.IAFrom(addr.ISD(isd), addr.AS(as))
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
	args := []any{time.Now().Unix()}
	rows, err := e.read.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, serrors.New("Error looking up assets", "err", err, "q", stmt)
	}
	defer rows.Close()
	var res []*RedemptionDelegation
	for rows.Next() {
		a := &RedemptionDelegation{}
		var isd uint16
		var as uint64
		err = rows.Scan(
			&isd,
			&as,
			&a.ResIdLow,
			&a.ResIdHigh,
			&a.Expiration,
			&a.PaidUntil,
			&a.Key,
			&a.Encodings)
		if err != nil {
			return nil, serrors.Wrap("Error reading DB response", err)
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
	rows, err := e.read.QueryContext(ctx, stmt, time.Now().Unix(), ia.ISD(), ia.AS())
	if err != nil {
		return nil, serrors.New("Error looking up assets", "err", err, "q", stmt)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	a := &RedemptionDelegation{}
	var isd uint16
	var as uint64
	err = rows.Scan(
		&isd,
		&as,
		&a.ResIdLow,
		&a.ResIdHigh,
		&a.Expiration,
		&a.PaidUntil,
		&a.Key,
		&a.Encodings)
	if err != nil {
		return nil, serrors.Wrap("Error reading DB response", err)
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
		res_id_limit_low = excluded.res_id_limit_low,
		res_id_limit_high = excluded.res_id_limit_high;`
	res, err := e.write.ExecContext(ctx, q,
		r.IA.ISD(),
		r.IA.AS(),
		r.ResIdLow,
		r.ResIdHigh,
		r.Expiration,
		r.PaidUntil,
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
		err = rows.Scan(&a.Id, &a.StartsAt, &a.StopsAt)
		if err != nil {
			return nil, serrors.Wrap("Error reading DB response", err)
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
		time.Now().Unix(),
	}
	return query, args
}

func (e *executor) SearchAssetsForStatistics(ctx context.Context, params *StatisticsQuery) ([]*DBStat, []*DBStat, error) {
	if e.read == nil {
		return nil, nil, serrors.New("No database open")
	}
	publishStmt, args := e.buildAssetEventsQuery(params, AssetPublished)
	publishRows, err := e.read.QueryContext(ctx, publishStmt, args...)
	if err != nil {
		return nil, nil, serrors.New("Error looking up assets", "err", err, "q", publishStmt)
	}
	defer publishRows.Close()
	var publishedAssets []*DBStat
	for publishRows.Next() {
		a := &DBStat{}
		err = publishRows.Scan(&a.Bandwidth, &a.Price, &a.StartsAt, &a.StopsAt)
		if err != nil {
			return nil, nil, serrors.Wrap("Error reading DB response", err)
		}
		publishedAssets = append(publishedAssets, a)
	}
	boughtStmt, args := e.buildAssetEventsQuery(params, AssetBought)
	boughtRows, err := e.read.QueryContext(ctx, boughtStmt, args...)
	if err != nil {
		return nil, nil, serrors.New("Error looking up assets", "err", err, "q", boughtStmt)
	}
	defer boughtRows.Close()
	var boughtAssets []*DBStat
	for boughtRows.Next() {
		a := &DBStat{}
		err = boughtRows.Scan(&a.Bandwidth, &a.Price, &a.StartsAt, &a.StopsAt)
		if err != nil {
			return nil, nil, serrors.Wrap("Error reading DB response", err)
		}
		boughtAssets = append(boughtAssets, a)
	}
	return publishedAssets, boughtAssets, nil
}

func (e *executor) buildAssetEventsQuery(params *StatisticsQuery, eventType AssetEventType) (string, []any) {
	var args []any
	where := []string{}
	query := []string{
		"SELECT bandwidth, price, starts_at, stops_at FROM Asset_Events",
	}
	where = append(where, "(event_type = ?) AND (isd_id=?) AND (as_id=?) AND (stops_at > ?) AND (starts_at <= ?)")
	args = append(args, eventType, int64(params.IA.ISD()), int64(params.IA.AS()), params.WindowStart, params.WindowEnd)
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

func (e *executor) RegisterAssetEvent(ctx context.Context, a *DBAsset, eventType AssetEventType) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	var err error
	inst := `INSERT INTO Asset_Events (event_type, isd_id, as_id, ingress, egress, bandwidth, starts_at, stops_at, price)
	VALUES(?,?,?,?,?,?,?,?,?)`
	res, err := e.write.ExecContext(ctx, inst, eventType, a.IA.ISD(), a.IA.AS(), a.IfIdIngress, a.IfIdEgress, a.Bandwidth,
		a.StartsAt, a.StopsAt, a.Price)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (e *executor) SearchOwnedAssets(ctx context.Context, params *OwnedAssetQuery) ([]*DBAsset, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	var stmt string
	var args []any
	if params.ValidityMode == FullOverlap {
		stmt, args = e.buildSearchOwnedAssetsFullOverlapQuery(params)
	} else if params.ValidityMode == PartialOverlap {
		stmt, args = e.buildSearchOwnedAssetsPartialOverlapQuery(params)
	} else {
		return nil, serrors.New("invalid validity mode")
	}
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

func (e *executor) LookupAsset(ctx context.Context, id int64, ia addr.IA) (*DBAsset, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	q := `SELECT ` + assetColumnsWithAlias + ` FROM Assets a WHERE a.ID = ? AND a.isd_id = ? AND a.as_id = ?`
	rows, err := e.read.QueryContext(ctx, q, id, ia.ISD(), ia.AS())
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, serrors.New("asset not found")
	}
	return scanAsset(rows)
}

func (e *executor) LookupAssets(ctx context.Context, ia addr.IA) ([]*DBAsset, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	q := `SELECT ` + assetColumnsWithAlias + ` FROM Assets a WHERE a.isd_id = ? AND a.as_id = ? AND a.account_id IS NULL`
	rows, err := e.read.QueryContext(ctx, q, ia.ISD(), ia.AS())
	if err != nil {
		return nil, err
	}
	res := []*DBAsset{}
	for rows.Next() {
		asset, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		res = append(res, asset)
	}
	return res, nil
}

func (e *executor) LookupAllAssetSegments(ctx context.Context, id int64) ([]*DBAssetSegment, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	q := `SELECT starts_at, stops_at, available FROM Asset_Segment WHERE asset_id = ?`
	rows, err := e.read.QueryContext(ctx, q, id)
	if err != nil {
		return nil, err
	}
	res := []*DBAssetSegment{}
	for rows.Next() {
		seg := &DBAssetSegment{}
		err = rows.Scan(&seg.StartsAt, &seg.StopsAt, &seg.Available)
		if err != nil {
			return nil, err
		}
		res = append(res, seg)
	}
	return res, nil
}

func (e *executor) SearchListedAssets(ctx context.Context, params *ListedAssetQuery) ([]*DBAsset, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	var stmt string
	var args []any
	var res []*DBAsset
	if params.ValidityMode == FullOverlap {
		stmt, args = e.buildSearchListedAssetsFullOverlapQuery(params)
		rows, err := e.read.QueryContext(ctx, stmt, args...)
		if err != nil {
			return nil, serrors.New("Error looking up assets", "err", err, "q", stmt)
		}
		defer rows.Close()

		for rows.Next() {
			a, err := scanAssetWithAvailableBandwidth(rows)
			if err != nil {
				return nil, err
			}
			res = append(res, a)
		}
	} else if params.ValidityMode == PartialOverlap {
		stmt, args = e.buildSearchListedAssetsPartialOverlapQuery(params)
		rows, err := e.read.QueryContext(ctx, stmt, args...)
		if err != nil {
			return nil, serrors.New("Error looking up assets", "err", err, "q", stmt)
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAssetWithAvailableBandwidthAndUsability(rows)
			if err != nil {
				return nil, err
			}
			res = append(res, a)
		}
	} else {
		return nil, serrors.New("invalid validity mode")
	}

	return res, nil
}

func (e *executor) buildSearchListedAssetsPartialOverlapQuery(params *ListedAssetQuery) (string, []any) {
	var args []any
	where := []string{}
	queryStart := []string{`
	WITH candidates AS (
		SELECT ` + assetColumnsWithAlias + `
		FROM Assets a
		WHERE a.isd_id = ? AND a.as_id = ?
		AND a.stops_at > ? AND a.starts_at < ?
		AND a.account_id IS NULL
		AND a.bandwidth >= ?
	`}
	queryEnd := `
	),
	hits AS (
		SELECT c.id AS asset_id,
			MAX(g.starts_at, ?) AS f,
			MIN(g.stops_at,  ?) AS t,
			g.available
		FROM candidates c
		JOIN Asset_Segment g
		ON g.asset_id = c.id AND g.starts_at < ? AND g.stops_at > ?
		WHERE g.available >= ?
	),
	marked AS (
		SELECT *,
			CASE WHEN f = LAG(t) OVER (PARTITION BY asset_id ORDER BY f)
					THEN 0 ELSE 1 END AS new_island
		FROM hits
	),
	islands AS (
		SELECT *,
			SUM(new_island) OVER (PARTITION BY asset_id ORDER BY f) AS island
		FROM marked
	)
	SELECT c.id, c.account_id, c.isd_id, c.as_id, c.bandwidth, c.bandwidth_min, c.bandwidth_max,
		c.price, c.time_granularity, c.time_min_duration, c.time_max_duration, c.starts_at, c.stops_at, 
		c.ingress, c.egress,
		MIN(i.f)         AS usable_from,
		MAX(i.t)         AS usable_to,
		MIN(i.available) AS max_buyable_bandwidth
	FROM islands i
	JOIN candidates c ON c.id = i.asset_id
	GROUP BY i.asset_id, i.island
	ORDER BY c.isd_id, c.as_id, c.ingress, c.egress, c.id, usable_from`
	args = append(args, params.IA.ISD(), params.IA.AS(), params.StartsAt, params.StopsAt, params.MinRequiredBandwidth)
	where = append(where, "(a.state = ?)")
	args = append(args, AssetStateAvailable)
	if params.Price != nil {
		where = append(where, "(a.price<=?)")
		args = append(args, *params.Price)
	}
	if params.Ingress != nil {
		where = append(where, "(a.ingress=?)")
		args = append(args, *params.Ingress)
	}
	if params.Egress != nil {
		where = append(where, "(a.egress=?)")
		args = append(args, *params.Egress)
	}
	args = append(args, params.StartsAt, params.StopsAt, params.StopsAt, params.StartsAt, params.MinRequiredBandwidth)
	query := append(queryStart, fmt.Sprintf("AND %s", strings.Join(where, "AND\n")), queryEnd)
	return strings.Join(query, "\n"), args
}

func (e *executor) buildSearchListedAssetsFullOverlapQuery(params *ListedAssetQuery) (string, []any) {
	var args []any
	where := []string{}
	queryStart := []string{`
	
	SELECT ` + assetColumnsWithAlias + `, MIN(g.available) AS max_buyable_bandwidth
	FROM Assets a
	JOIN Asset_Segment g
	ON g.asset_id = a.id AND g.starts_at < ? AND g.stops_at > ?
	WHERE a.isd_id = ? AND a.as_id = ?
	AND a.starts_at <= ? AND a.stops_at >= ?
	AND a.account_id IS NULL
	AND a.bandwidth >= ?
	`,
	}
	queryEnd := `
	GROUP BY a.id
	HAVING MIN(g.available) >= ?
	ORDER BY a.id
	`
	args = append(args, params.StopsAt, params.StartsAt, params.IA.ISD(), params.IA.AS(), params.StartsAt,
		params.StopsAt, params.MinRequiredBandwidth)

	where = append(where, "(a.state = ?)")
	args = append(args, AssetStateAvailable)
	if params.Price != nil {
		where = append(where, "(a.price<=?)")
		args = append(args, *params.Price)
	}
	if params.Ingress != nil {
		where = append(where, "(a.ingress=?)")
		args = append(args, *params.Ingress)
	}
	if params.Egress != nil {
		where = append(where, "(a.egress=?)")
		args = append(args, *params.Egress)
	}
	args = append(args, params.MinRequiredBandwidth)
	query := append(queryStart, fmt.Sprintf("AND %s", strings.Join(where, "AND\n")), queryEnd)
	return strings.Join(query, "\n"), args
}

func (e *executor) buildSearchOwnedAssetsPartialOverlapQuery(params *OwnedAssetQuery) (string, []any) {
	var args []any
	where := []string{}
	query := []string{
		"SELECT " + assetColumnsWithAlias + " FROM Assets a",
	}
	query = append(query, "JOIN Accounts owner ON a.account_id = owner.id JOIN "+
		"Accounts current ON current.id = ?")
	args = append(args, *params.AccountId)
	where = append(where, "((current.scope = '' AND owner.user_id = current.user_id) OR "+
		"(current.scope != '' AND owner.id = current.id))")
	where = append(where, "(a.state = ?)")
	args = append(args, AssetStateAvailable)
	if params.IA != nil {
		where = append(where, "(a.isd_id=?) AND (a.as_id=?)")
		args = append(args, int64(params.IA.ISD()), int64(params.IA.AS()))
	}
	if params.StartsAt != nil {
		where = append(where, "(a.starts_at>=?)")
		args = append(args, *params.StartsAt)
	}
	if params.StopsAt != nil {
		where = append(where, "(a.stops_at<=?)")
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
	return strings.Join(query, "\n"), args
}

func (e *executor) buildSearchOwnedAssetsFullOverlapQuery(params *OwnedAssetQuery) (string, []any) {
	var args []any
	where := []string{}
	query := []string{
		"SELECT " + assetColumnsWithAlias + " FROM Assets a",
	}
	query = append(query, "JOIN Accounts owner ON a.account_id = owner.id JOIN "+
		"Accounts current ON current.id = ?")
	args = append(args, *params.AccountId)
	where = append(where, "((current.scope = '' AND owner.user_id = current.user_id) OR "+
		"(current.scope != '' AND owner.id = current.id))")
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
		r.StartsAt,
		r.StopsAt,
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
				 price, time_granularity, time_min_duration, time_max_duration, starts_at, stops_at,
				 ingress, egress, account_id)
				 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
		res, err = e.write.ExecContext(ctx, inst,
			a.IA.ISD(),
			a.IA.AS(),
			a.Bandwidth,
			a.BandwidthMin,
			a.BandwidthMax,
			a.Price,
			a.TimeGranularity,
			a.TimeMinDuration,
			a.TimeMaxDuration,
			a.StartsAt,
			a.StopsAt,
			a.IfIdIngress,
			a.IfIdEgress,
			a.AccountId)
	} else {
		inst := `INSERT INTO Assets (isd_id, as_id, bandwidth, bandwidth_min, bandwidth_max,
				 price, time_granularity, time_min_duration, time_max_duration, starts_at, stops_at, ingress, egress)
				 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`
		res, err = e.write.ExecContext(ctx, inst,
			a.IA.ISD(), a.IA.AS(),
			a.Bandwidth,
			a.BandwidthMin,
			a.BandwidthMax,
			a.Price,
			a.TimeGranularity,
			a.TimeMinDuration,
			a.TimeMaxDuration,
			a.StartsAt,
			a.StopsAt,
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

func (e *executor) InsertAssetSegment(ctx context.Context, asset_id int64, from int64, to int64, bw uint32) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `INSERT INTO Asset_Segment(asset_id, starts_at, stops_at, available) VALUES (?,?,?,?)`
	res, err := e.write.ExecContext(ctx, inst, asset_id, from, to, bw)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (e *executor) IncreaseAssetSegmentsBandwidth(ctx context.Context, assetId int64, delta int64) error {
	if e.write == nil {
		return serrors.New("No database open")
	}
	inst := `
	UPDATE Asset_Segment SET available = available + ?
	WHERE asset_id = ?`
	_, err := e.write.ExecContext(ctx, inst, delta, assetId)
	if err != nil {
		return err
	}
	return nil
}

func (e *executor) UpdateAsset(ctx context.Context, a *DBAsset) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	inst := `
	UPDATE Assets
	SET bandwidth = ?, bandwidth_min = ?, bandwidth_max = ?, price = ?, time_granularity = ?,
	time_min_duration = ?, time_max_duration = ?, starts_at = ?, stops_at = ?
	WHERE id = ? AND state = ? AND account_ID IS NULL`
	res, err := e.write.ExecContext(ctx, inst, a.Bandwidth, a.BandwidthMin, a.BandwidthMax,
		a.Price, a.TimeGranularity, a.TimeMinDuration, a.TimeMaxDuration, a.StartsAt,
		a.StopsAt, a.ID, AssetStateAvailable)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (e *executor) MergeAssetAt(ctx context.Context, assetId int64, point int64) error {
	if e.write == nil {
		return serrors.New("No database open")
	}
	updateInst := `
		UPDATE Asset_Segment AS l
		SET stops_at = r.stops_at
		FROM Asset_Segment AS r
		WHERE l.asset_id = ? AND l.stops_at = ?
		  AND r.asset_id = ? AND r.starts_at = ?
		  AND r.available = l.available
		`
	res, err := e.write.ExecContext(ctx, updateInst, assetId, point, assetId, point)
	if err != nil {
		return fmt.Errorf("merge at %d (update): %w", point, err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		deleteInst := `DELETE FROM Asset_Segment WHERE asset_id = ? AND starts_at = ?`
		if _, err := e.write.ExecContext(ctx, deleteInst, assetId, point); err != nil {
			return fmt.Errorf("merge at %d (delete): %w", point, err)
		}
	}
	return nil
}

func (e *executor) SplitAssetAt(ctx context.Context, assetId int64, point int64) error {
	if e.write == nil {
		return serrors.New("No database open")
	}
	insertInst := `
		INSERT INTO Asset_segment (asset_id, starts_at, stops_at, available)
		SELECT asset_id, ?, stops_at, available
		FROM Asset_segment
		WHERE asset_id = ? AND starts_at < ? AND stops_at > ?`
	_, err := e.write.ExecContext(ctx, insertInst, point, assetId, point, point)
	if err != nil {
		return err
	}
	updateInst := `
		UPDATE Asset_segment
		SET stops_at = ?
		WHERE asset_id = ? AND starts_at < ? AND stops_at > ?
		`
	_, err = e.write.ExecContext(ctx, updateInst, point, assetId, point, point)
	if err != nil {
		return err
	}
	return nil
}

func (e *executor) CheckAssetAvailability(
	ctx context.Context,
	assetId int64,
	startsAt int64,
	stopsAt int64,
	bw uint32,
) (*DBAsset, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	inst := `
	SELECT ` + assetColumnsWithAlias + ` FROM Assets a
	WHERE a.id = ?
	AND a.account_ID IS NULL
	AND a.state = ?
	AND a.starts_at <= ? AND a.stops_at >= ?
	AND NOT EXISTS (
		SELECT 1 FROM Asset_Segment g
		WHERE g.asset_id = a.id
		AND g.starts_at < ? AND g.stops_at > ?
		AND g.available < ?);
	`
	rows, err := e.read.QueryContext(ctx, inst, assetId, AssetStateAvailable, startsAt, stopsAt, stopsAt, startsAt, bw)
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, serrors.New("asset not found")
	}
	asset, err := scanAsset(rows)
	if err != nil {
		return nil, err
	}
	return asset, nil
}

func (e *executor) BuyFromAssetSegment(
	ctx context.Context,
	assetId int64,
	startsAt int64,
	stopsAt int64,
	bw uint32,
	accountId int64,
	asset *DBAsset,
) (*DBAsset, error) {
	decreaseAvailabilityInst := `
	UPDATE asset_segment
	SET available = available - ?
	WHERE asset_id = ? AND starts_at >= ? AND stops_at <= ?`
	_, err := e.write.ExecContext(ctx, decreaseAvailabilityInst, bw, assetId, startsAt, stopsAt)
	if err != nil {
		return nil, err
	}
	userAsset := &DBAsset{
		AccountId: sql.NullInt64{
			Int64: accountId,
			Valid: true,
		},
		IA:              asset.IA,
		Bandwidth:       bw,
		StartsAt:        startsAt,
		StopsAt:         stopsAt,
		BandwidthMin:    asset.BandwidthMin,
		BandwidthMax:    asset.BandwidthMax,
		Price:           asset.Price,
		TimeGranularity: asset.TimeGranularity,
		TimeMinDuration: asset.TimeMinDuration,
		TimeMaxDuration: asset.TimeMaxDuration,
		IfIdIngress:     asset.IfIdIngress,
		IfIdEgress:      asset.IfIdEgress,
	}
	newId, err := e.InsertAsset(ctx, userAsset)
	if err != nil {
		return nil, err
	}
	userAsset.ID = newId
	return userAsset, nil
}

// Buys a single asset. Availability must be checked in advance in same transaction.
func (e *executor) BuyAsset(
	ctx context.Context,
	startsAt int64,
	stopsAt int64,
	bw uint32,
	accountId int64,
	dbAsset *DBAsset,
) (*DBAsset, error) {
	if e.write == nil {
		return nil, serrors.New("No database open")
	}
	decreaseAvailabilityInst := `
	UPDATE asset_segment
	SET available = available - ?
	WHERE asset_id = ? AND starts_at >= ? AND stops_at <= ?`
	_, err := e.write.ExecContext(ctx, decreaseAvailabilityInst, bw, dbAsset.ID, startsAt, stopsAt)
	if err != nil {
		return nil, err
	}
	userAsset := &DBAsset{
		AccountId: sql.NullInt64{
			Int64: accountId,
			Valid: true,
		},
		IA:              dbAsset.IA,
		Bandwidth:       bw,
		StartsAt:        startsAt,
		StopsAt:         stopsAt,
		BandwidthMin:    dbAsset.BandwidthMin,
		BandwidthMax:    dbAsset.BandwidthMax,
		Price:           dbAsset.Price,
		TimeGranularity: dbAsset.TimeGranularity,
		TimeMinDuration: dbAsset.TimeMinDuration,
		TimeMaxDuration: dbAsset.TimeMaxDuration,
		IfIdIngress:     dbAsset.IfIdIngress,
		IfIdEgress:      dbAsset.IfIdEgress,
	}
	newId, err := e.InsertAsset(ctx, userAsset)
	if err != nil {
		return nil, err
	}
	userAsset.ID = newId
	return userAsset, nil
}
