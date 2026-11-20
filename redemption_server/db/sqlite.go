package db

import (
	"context"
	"database/sql"
	"io"
	"time"

	"github.com/scionproto/scion/pkg/hummingbird/id_stores"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/private/storage/db"
)

type Repository interface {
	InsertReservation(ctx context.Context, r *DBReservation) (int64, error)
	FetchReservations(ctx context.Context, params ReservationQuery) ([]id_stores.Reservation, error)
}

type DB interface {
	io.Closer
	Repository
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

var _ Repository = (*executor)(nil)
var _ DB = (*Backend)(nil)

func (e *executor) InsertReservation(ctx context.Context, r *DBReservation) (int64, error) {
	if e.write == nil {
		return 0, serrors.New("No database open")
	}
	q := `INSERT INTO Reservations
	      (reservation_id, ingress, egress,
		  starts_at, stops_at)
		  VALUES(?,?,?,?,?)`
	res, err := e.write.ExecContext(ctx, q,
		r.ReservationID,
		r.Ingress,
		r.Egress,
		r.StartsAt.UTC().Unix(),
		r.StopsAt.UTC().Unix(),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (e *executor) FetchReservations(ctx context.Context, params ReservationQuery) ([]id_stores.Reservation, error) {
	if e.read == nil {
		return nil, serrors.New("No database open")
	}
	q := `SELECT reservation_id, starts_at, stops_at FROM Reservations
			  WHERE (stops_at > ?)`
	rows, err := e.read.QueryContext(ctx, q, time.Now().UTC().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []id_stores.Reservation{}
	for rows.Next() {
		r := id_stores.Reservation{}
		err = rows.Scan(&r.Id, &r.StartsAt, &r.StopsAt)
		if err != nil {
			return nil, serrors.Wrap("Error reading DB response", err)
		}
		result = append(result, r)
	}
	return result, nil
}
