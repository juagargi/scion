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
	"math"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/private/serrors"
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

type ValidityMode int

const (
	FullOverlap    ValidityMode = 0
	PartialOverlap ValidityMode = 1
)

type AssetEventType int

const (
	AssetPublished AssetEventType = iota
	AssetBought
)

// AssetID is the unsigned representation of an asset identifier used by the
// marketplace API.
type AssetID uint64

// Int64 converts an API asset identifier to the signed range supported by
// SQLite row IDs.
func (id AssetID) Int64() (int64, error) {
	if id > math.MaxInt64 {
		return 0, serrors.New("asset ID exceeds SQLite range", "id", id)
	}
	return int64(id), nil
}

type ListedAssetQuery struct {
	// ISD-AS of the asset
	IA addr.IA
	// ingress ID of the asset
	Ingress *uint32
	// egress ID of the asset
	Egress *uint32
	// the minimal required bandwidth of the asset
	MinRequiredBandwidth uint32
	// starts at in unix seconds
	StartsAt int64
	// stops at in unix seconds
	StopsAt int64
	// the maximum price in kbps per second
	Price        *uint32
	ValidityMode ValidityMode
}

type OwnedAssetQuery struct {
	// the account ID of the owner
	AccountId *int64
	// ISD-AS of the asset
	IA *addr.IA
	// ingress ID of the asset
	Ingress *uint32
	// egress ID of the asset
	Egress *uint32
	// the minimal required bandwidth of the asset
	MinRequiredBandwidth *uint32
	// starts at in unix seconds
	StartsAt *int64
	// stops at in unix seconds
	StopsAt *int64
	// the maximum price in kbps per second
	Price        *uint32
	ValidityMode ValidityMode
}

type UsedReservationsQuery struct {
	// ISD-AS
	IA addr.IA
	// the inclusive lower bound reservation ID to query
	Limit_low uint32
	// the exclusive upper bound reservations ID to query
	Limit_high uint32
}

type UsedReservation struct {
	// The reservation ID
	Id uint32
	// inclusive start time of the reservation
	StartsAt int64
	// exclusive end time of the reservation
	StopsAt int64
}

type RedemptionDelegation struct {
	// ISD-AS
	IA addr.IA
	// expiration time of redemption delegation
	Expiration int64
	// until when redemption delegation is paid
	PaidUntil int64
	// the inclusive lower bound reservation ID used for redemption
	ResIdLow uint32
	// the exclusive upper bound reservation ID used for redemption
	ResIdHigh uint32
	// the secret value to derive the authentication keys
	Key []byte
	// the bandwidth dataplane encoding
	Encodings []byte
}

type ReservationQuery struct {
	// the account ID
	AccountId int64
	// ISD-AS of the reservation
	IA *addr.IA
	// ingress ID of the reservation
	Ingress *uint32
	// ingress ID of the reservation
	Egress *uint32
	// reservation starts at latest in unix seconds
	StartsAt *int64
	// reservation stops at earliest in unix seconds
	StopsAt *int64
	// minimum bandwith the reservation holds
	Bandwidth *uint32
}
type StatisticsQuery struct {
	// ISD-AS for which statistics should be queried
	IA addr.IA
	// time window start in unix seconds
	WindowStart int64
	// time window end in unix seconds
	WindowEnd int64
	// ingress ID of the assets
	Ingress *uint32
	// egress ID of the assets
	Egress *uint32
}
type DBStat struct {
	// the price of the asset
	Price int64
	// the bandwidth of the asset
	Bandwidth int64
	// the inclusive start time of the asset
	StartsAt int64
	// the exclusive end time of the asset
	StopsAt int64
}

type DBReservation struct {
	// marketplace-wide unique database reservation ID
	ID int64
	// the actual reservation ID
	ReservationID uint32
	// ISD-AS of the reservation
	IA addr.IA
	// ingress ID of the asset
	Ingress uint32
	// egress ID of the asset
	Egress uint32
	// the bandwidth stored in the reservation
	Bandwidth uint32
	// the dataplane encoded bandwidth of the reservation
	EncodedBandwidth uint16
	// inclusive start time of the reservation
	StartsAt int64
	// exclusive end time of the reservation
	StopsAt int64
	// the account ID of the owner
	AccountId int64
	// the cryptographic key
	Key []byte
}

type DBAsset struct {
	// marketplace-wide unique asset ID
	ID int64
	// the account ID of the owner
	AccountId sql.NullInt64
	// ISD-AS of the asset
	IA addr.IA
	// the bandwidth stored in the asset
	Bandwidth uint32
	// the minimal bandwidth required at redemption
	BandwidthMin uint32
	// the maximum bandwidth allowed at redemption
	BandwidthMax uint32
	// inclusive start time of the asset
	StartsAt int64
	// exclusive end time of the asset
	StopsAt int64
	// price per kbps per second of the asset
	Price uint32
	// asset duration must be divisible by time granularity at redemption
	TimeGranularity uint32
	// minimum asset duration required at redemption
	TimeMinDuration uint32
	// maximum asset duration allowed at redemption
	TimeMaxDuration uint32
	// ingress ID of the asset
	IfIdIngress sql.NullInt32
	// egress ID of the asset
	IfIdEgress sql.NullInt32
}

type DBAssetSegment struct {
	StartsAt  int64 `json:"starts_at"`
	StopsAt   int64 `json:"stops_at"`
	Available int64 `json:"available"`
}

type DBUser struct {
	// marketplace-wide unique user ID
	ID int64
	// the user name
	Name string
	// the password hash
	PasswordHash string
}

type DBAccount struct {
	// marketplace-wide unique account ID
	ID int64
	// the userID to which this account belongs
	UserID int64
	// Name of scoped account, empty for main account
	Scope string
	// account balance
	Balance int64
	// the currently valid token version
	TokenVersion int64
}

type DBASUser struct {
	// ISD-AS
	IA addr.IA
	// the password hash
	PasswordHash string
	// the currently valid token version
	TokenVersion int64
	// AS account balance
	Balance int64
}

// EncodingsToInts decodes the stored encoding points. The blob holds one little
// endian uint32 per point. A length that is not a multiple of four can only come from a corrupt
// row rather than from a delegation request, since the caller would have prevented it via a
// check to the delegation parameters.
func (r *RedemptionDelegation) EncodingsToInts() ([]uint32, error) {
	if len(r.Encodings)%4 != 0 {
		return nil, serrors.New("corrupt encoding points in the redemption delegation",
			"length", len(r.Encodings))
	}
	nums := make([]uint32, len(r.Encodings)/4)
	for i := range nums {
		nums[i] = binary.LittleEndian.Uint32(r.Encodings[i*4:])
	}
	return nums, nil
}

func (r *RedemptionDelegation) EncodeInts(nums []uint32) {
	buf := make([]byte, len(nums)*4)
	for i, n := range nums {
		binary.LittleEndian.PutUint32(buf[i*4:], n)
	}
	r.Encodings = buf
}
