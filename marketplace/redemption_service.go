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

	"github.com/scionproto/scion/pkg/hummingbird/bwencoding"
	"github.com/scionproto/scion/pkg/hummingbird/id_stores"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	hbird "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
)

// aesKeySizes are the key lengths in bytes that aes.NewCipher accepts, i.e. the ones
// that NewRedemptionService accepts.
var aesKeySizes = []int{16, 24, 32}

type RedemptionService struct {
	encodingPoints []uint32
	cipher         cipher.Block
	resIdStore     *id_stores.UsedIDStore
	expiration     time.Time
}
type RedemptionDelegationUpdate struct {
	ExpirationTime time.Time
	IdLimitLow     uint32
	IdLimitHigh    uint32
	Key            []byte
	EncodingPoints []uint32
}

// validateDelegationParams checks the parameters of a redemption delegation that the marketplace
// cannot work around, so that a delegation it could not serve is refused before it is stored.
//
// - The encoding table has to be complete, one bandwidth per codepoint of the flyover
// bandwidth field. A shorter one makes encodeBandwidth index out of range, and even
// where it does not, the border router decodes the codepoint of a flyover with its own
// complete table, so a partial table would report a bw_rounded that the router does not enforce.
//
// - The key has to be of a length AES accepts, since NewRedemptionService derives every
// reservation key from it. Only the length is checked here, which couples this to the
// aes.NewCipher call there.
func validateDelegationParams(state *RedemptionDelegationUpdate) error {
	if len(state.EncodingPoints) != bwencoding.Codepoints {
		return serrors.New("wrong number of encoding points in the redemption delegation",
			"expected", bwencoding.Codepoints, "actual", len(state.EncodingPoints))
	}
	if !slices.Contains(aesKeySizes, len(state.Key)) {
		return serrors.New("unusable key length in the redemption delegation",
			"expected", aesKeySizes, "actual", len(state.Key))
	}
	return nil
}

func NewRedemptionService(initState *RedemptionDelegationUpdate, r []id_stores.Reservation) (*RedemptionService, error) {
	if err := validateDelegationParams(initState); err != nil {
		return nil, err
	}
	slices.Sort(initState.EncodingPoints)
	blockCipher, err := aes.NewCipher(initState.Key)
	if err != nil {
		return nil, err
	}
	idStore := &id_stores.UsedIDStore{}
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

func (r *RedemptionService) Redeem(
	req *hummingbird.RedeemAssetFromASRequest,
) *hummingbird.RedeemAssetFromASResponse {
	now := time.Now()
	resId, err := r.resIdStore.Next(now.Unix(), req.StartsAt.Seconds, req.StopsAt.Seconds)
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
