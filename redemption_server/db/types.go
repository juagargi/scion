package db

import (
	"database/sql"
	"time"
)

type DBReservation struct {
	ReservationID uint32
	Ingress       uint16
	Egress        uint16
	StartsAt      time.Time
	StopsAt       time.Time
}

type ReservationQuery struct {
	Ingress sql.NullInt32
	Egress  sql.NullInt32
}
