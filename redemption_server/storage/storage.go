package storage

import (
	"context"

	"github.com/scionproto/scion/pkg/hummingbird/id_stores"
	"github.com/scionproto/scion/private/config"
	"github.com/scionproto/scion/private/storage/db"
	redemptiondb "github.com/scionproto/scion/redemption_server/db"
)

type DBConfig struct {
	config.NoDefaulter
	Connection       string `toml:"connection,omitempty"`
	MaxOpenReadConns int    `toml:"max_open_read_conns,omitempty"`
	MaxIdleReadConns int    `toml:"max_idle_read_conns,omitempty"`
	allowEmptyConn   bool
}

type RedemptionStorage struct {
	db redemptiondb.DB
}

func NewStorage(
	c DBConfig,
) (*RedemptionStorage, error) {
	db, err := redemptiondb.New(c.Connection, &db.SqliteConfig{
		MaxOpenReadConns: c.MaxOpenReadConns,
		MaxIdleReadConns: c.MaxIdleReadConns,
	})
	if err != nil {
		return nil, err
	}
	return &RedemptionStorage{
		db: db,
	}, nil
}

func (s *RedemptionStorage) InsertReservation(ctx context.Context, r *redemptiondb.DBReservation) (int64, error) {
	return s.db.InsertReservation(ctx, r)
}

func (s *RedemptionStorage) FetchReservations(ctx context.Context, params redemptiondb.ReservationQuery,
) ([]id_stores.Reservation, error) {
	return s.db.FetchReservations(ctx, params)
}
