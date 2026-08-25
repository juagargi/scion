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

package marketplace

import (
	"crypto/aes"
	"crypto/cipher"
	"slices"
	"sort"
	"time"

	"github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	hbird "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
)

const (
	AkBufferSize = 16
)

type RedemptionService struct {
	encodingPoints []uint32
	cipher         cipher.Block
	resIdStore     ReservationIdStore
	expiration     time.Time
}
type RedemptionDelegationUpdate struct {
	ExpirationTime time.Time
	IdLimitLow     uint32
	IdLimitHigh    uint32
	Key            []byte
	EncodingPoints []uint32
}
type ReservationIdStore interface {
	Init(limit_low uint32, limit_high uint32, r []*db.UsedReservation) error
	Next(now int64, start int64, end int64) (uint32, error)
	Close() error
}

func NewRedemptionService(initState *RedemptionDelegationUpdate, r []*db.UsedReservation) (*RedemptionService, error) {
	slices.Sort(initState.EncodingPoints)
	blockCipher, err := aes.NewCipher(initState.Key)
	if err != nil {
		return nil, err
	}
	idStore := &UsedIDStore{}
	err = idStore.Init(initState.IdLimitLow, initState.IdLimitHigh, r)
	if err != nil {
		return nil, err
	}
	return &RedemptionService{
		cipher:         blockCipher,
		resIdStore:     idStore,
		expiration:     initState.ExpirationTime,
		encodingPoints: initState.EncodingPoints,
	}, nil
}

func (r *RedemptionService) Redeem(req *hummingbird.RedeemAssetFromASRequest) *hummingbird.RedeemAssetFromASResponse {
	now := time.Now()
	resId, err := r.resIdStore.Next(now.Unix(), req.StartsAt.Seconds, req.StartsAt.Seconds)
	if err != nil {
		return &hummingbird.RedeemAssetFromASResponse{
			Result: &hummingbird.RedeemAssetFromASResponse_Error{
				Error: err.Error(),
			},
		}
	}

	unixStart := uint32(req.StartsAt.Seconds)
	unixEnd := uint32(req.StopsAt.Seconds)
	durSeconds := unixEnd - unixStart
	encoded_bw := r.encodeBandwidth(req.Bandwidth)

	var buff [16]byte
	ak := hbird.DeriveAuthKey(r.cipher, resId, encoded_bw, uint16(req.IngressId), uint16(req.EgressId), unixStart, uint16(durSeconds), buff[:])
	return &hummingbird.RedeemAssetFromASResponse{
		Result: &hummingbird.RedeemAssetFromASResponse_ResInfo{
			ResInfo: &hummingbird.ReservationInfo{
				ReservationId:       resId,
				BandwithRounded:     r.encodingPoints[encoded_bw],
				BwDataplaneEncoding: uint32(encoded_bw),
				AuthenticationKey:   ak,
			},
		},
	}
}

func (s *RedemptionService) encodeBandwidth(bw uint32) uint16 {
	return min(uint16(len(s.encodingPoints)-1), uint16(sort.Search(len(s.encodingPoints), func(i int) bool {
		return s.encodingPoints[i] >= bw
	})))
}
