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
	"database/sql"
	"encoding/binary"
	"time"

	"github.com/scionproto/scion/pkg/addr"
)

// AssetState describes the transient lifecycle state of an asset.
type AssetState int

const (
	AssetStateAvailable AssetState = iota
	AssetStateCheckedOut
	AssetStateRedemptionPending
	AssetStateSplitPending
	AssetStateCombinePending
)

type AssetQuery struct {
	AccountId            *int64
	IA                   *addr.IA
	Ingress              *uint32
	Egress               *uint32
	MinRequiredBandwidth *uint32
	StartsAt             *string
	StopsAt              *string
	Price                *uint32
}

type UsedReservationsQuery struct {
	IA    addr.IA
	Limit uint32
}

type UsedReservation struct {
	Id       uint32
	StartsAt time.Time
	StopsAt  time.Time
}

type RedemptionDelegation struct {
	IA                 addr.IA
	Expiration         time.Time
	PaidUntil          time.Time
	ReservationIdLimit uint32
	Key                []byte
	Encodings          []byte
}

type ReservationQuery struct {
	AccountId int64
	IA        *addr.IA
	Ingress   *uint32
	Egress    *uint32
	StartsAt  *string
	StopsAt   *string
	Bandwidth *uint32
}
type StatisticsQuery struct {
	IA          addr.IA
	WindowStart string
	WindowEnd   string
	Ingress     *uint32
	Egress      *uint32
}
type DBStat struct {
	Price     int64
	Bandwidth int64
	StartsAt  time.Time
	StopsAt   time.Time
	OwnerId   sql.NullInt64
}

type DBReservation struct {
	ID               int64
	ReservationID    uint32
	IA               addr.IA
	Ingress          uint32
	Egress           uint32
	Bandwidth        uint32
	EncodedBandwidth uint16
	StartsAt         time.Time
	StopsAt          time.Time
	AccountId        int64
	Key              []byte
}

type DBAsset struct {
	ID              int64
	AccountId       sql.NullInt64
	IA              addr.IA
	Bandwidth       uint32
	BandwidthMin    uint32
	BandwidthMax    uint32
	StartAt         time.Time
	StopsAt         time.Time
	Price           uint32
	TimeGranularity uint32
	TimeMinDuration uint32
	IfIdIngress     sql.NullInt32
	IfIdEgress      sql.NullInt32
}

type DBUser struct {
	ID           int64
	Name         string
	PasswordHash string
}

type DBAccount struct {
	ID           int64
	UserID       int64
	Scope        *string
	Balance      int64
	TokenVersion int64
}

type DBASUser struct {
	IA           addr.IA
	PasswordHash string
	TokenVersion int64
	Balance      int64
}

func (r *RedemptionDelegation) EncodingsToInts() []uint32 {
	if len(r.Encodings)%4 != 0 {
		panic("invalid data length")
	}
	nums := make([]uint32, len(r.Encodings)/4)
	for i := range nums {
		nums[i] = binary.LittleEndian.Uint32(r.Encodings[i*4:])
	}
	return nums
}

func (r *RedemptionDelegation) EncodeInts(nums []uint32) {
	buf := make([]byte, len(nums)*4)
	for i, n := range nums {
		binary.LittleEndian.PutUint32(buf[i*4:], n)
	}
	r.Encodings = buf
}
