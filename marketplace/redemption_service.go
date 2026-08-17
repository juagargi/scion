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
	"container/heap"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/marketplace/storage"
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	hbird "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
)

const (
	AkBufferSize = 16
)

type RedemptionService struct {
	SendChannel chan *hummingbird.RedeemAssetFromASRequest
	Pending     map[uint64]chan *hummingbird.RedeemAssetFromASResponse
	// The update channel is used to propagate updates to the redemption service
	// like different secret key or updated expiration date
	// The result of the update is send over the UpdateResultChannel, which MUST
	// be consumed by the entity who send the update over the UpdateChannel.
	UpdateChannel       chan *RedemptionDelegationUpdate
	UpdateResultChannel chan error
	client              *RedemptionServerPeer
	store               *storage.MarketplaceStorage
	cipher              cipher.Block
	resStart            uint32
	resLimit            uint32
	resIdStore          ReservationIdStore
	encodingPoints      []uint32
	expiration          time.Time
}

type RedemptionDelegationUpdate struct {
	ExpirationTime time.Time
	IdLimitLow     uint32
	IdLimitHigh    uint32
	Key            []byte
	EncodingPoints []uint32
}

type RedemptionDelegationUpdateResult struct {
	ExpirationTime time.Time
	OK             bool
}

type ReservationIdStore interface {
	Init(limit_low uint32, limit_high uint32, r []*db.UsedReservation) error
	Next(now int64, start int64, end int64) (uint32, error)
	Migrate(new_limit_low uint32, new_limit_hight uint32, r []*db.UsedReservation) error
	Close() error
}

func NewRedemptionService(ctx context.Context, client *RedemptionServerPeer, store *storage.MarketplaceStorage, ia addr.IA, initState *RedemptionDelegationUpdate, sendCh chan *hummingbird.RedeemAssetFromASRequest,
	pending map[uint64]chan *hummingbird.RedeemAssetFromASResponse) (*RedemptionService, error) {
	slices.Sort(initState.EncodingPoints)
	blockCipher, err := aes.NewCipher(initState.Key)
	if err != nil {
		return nil, err
	}
	s := &RedemptionService{
		SendChannel:         sendCh,
		Pending:             pending,
		UpdateChannel:       make(chan *RedemptionDelegationUpdate),
		UpdateResultChannel: make(chan error),
		store:               store,
		cipher:              blockCipher,
		resStart:            initState.IdLimitLow,
		resLimit:            initState.IdLimitHigh,
		encodingPoints:      initState.EncodingPoints,
		client:              client,
		expiration:          initState.ExpirationTime,
		resIdStore:          &UsedIDStore{},
	}
	res, err := s.store.FindUsedReservations(ctx, &db.UsedReservationsQuery{
		IA:         ia,
		Limit_low:  initState.IdLimitLow,
		Limit_high: initState.IdLimitHigh,
	})
	if err != nil {
		return nil, err
	}
	err = s.resIdStore.Init(initState.IdLimitLow, initState.IdLimitHigh, res)
	if err != nil {
		return nil, err
	}
	go s.readRoutine()
	return s, nil
}

func (s *RedemptionService) readRoutine() {
	var err error
	for {
		select {
		case u := <-s.UpdateChannel:
			if u.ExpirationTime.Before(time.Now()) {
				s.UpdateResultChannel <- nil
				return
			}
			s.UpdateResultChannel <- s.handleUpdate(u)
		case r := <-s.SendChannel:
			now := time.Now()
			if s.expiration.Before(now) {
				select {
				case s.SendChannel <- r:
				default:
				}
				return
			}
			if err = s.handleRequest(now, r); err != nil {
				fmt.Println(err)
				return
			}
		}
	}
}

func (s *RedemptionService) handleUpdate(u *RedemptionDelegationUpdate) error {
	var res []*db.UsedReservation
	var err error
	if u.ExpirationTime != s.expiration {
		res, err = s.store.FindUsedReservations(context.TODO(), &db.UsedReservationsQuery{
			IA:         s.client.ia,
			Limit_low:  u.IdLimitLow,
			Limit_high: u.IdLimitHigh,
		})
		if err != nil {
			return err
		}
		s.expiration = u.ExpirationTime
	}
	err = s.resIdStore.Migrate(u.IdLimitLow, u.IdLimitHigh, res)
	if err != nil {
		return err
	}
	slices.Sort(u.EncodingPoints)
	s.encodingPoints = u.EncodingPoints
	s.cipher, err = aes.NewCipher(u.Key)
	return err
}

func (s *RedemptionService) handleRequest(now time.Time, r *hummingbird.RedeemAssetFromASRequest) error {
	resId, err := s.resIdStore.Next(now.Unix(), r.StartsAt.Seconds, r.StopsAt.Seconds)
	if err != nil {
		return err
	}

	unixStart := uint32(r.StartsAt.Seconds)
	unixEnd := uint32(r.StopsAt.Seconds)
	durSeconds := unixEnd - unixStart
	encoded_bw := s.encodeBandwidth(r.Bandwidth)

	var buff [16]byte
	ak := hbird.DeriveAuthKey(s.cipher, resId, encoded_bw, uint16(r.IngressId), uint16(r.EgressId), unixStart, uint16(durSeconds), buff[:])
	s.client.mtx.Lock()
	defer s.client.mtx.Unlock()
	ch, ok := s.Pending[r.RequestId]
	if ok {
		ch <- &hummingbird.RedeemAssetFromASResponse{
			Result: &hummingbird.RedeemAssetFromASResponse_ResInfo{
				ResInfo: &hummingbird.ReservationInfo{
					ReservationId:       resId,
					BandwithRounded:     s.encodingPoints[encoded_bw],
					BwDataplaneEncoding: uint32(encoded_bw),
					AuthenticationKey:   ak,
				},
			},
			RequestId: r.RequestId,
		}
		close(ch)
		delete(s.client.pending, r.RequestId)
	}
	return nil
}

func (s *RedemptionService) encodeBandwidth(bw uint32) uint16 {
	return min(uint16(len(s.encodingPoints)-1), uint16(sort.Search(len(s.encodingPoints), func(i int) bool {
		return s.encodingPoints[i] >= bw
	})))
}

type entry struct {
	id         uint32
	expiration int64
}

type ExpiryHeap []*entry

func (h ExpiryHeap) Len() int {
	return len(h)
}

func (h ExpiryHeap) Less(i, j int) bool {
	return h[i].expiration < h[j].expiration
}

func (h ExpiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *ExpiryHeap) Push(x any) {
	*h = append(*h, x.(*entry))
}

func (h *ExpiryHeap) Pop() any {
	old := *h
	n := len(old)

	e := old[n-1]
	*h = old[:n-1]

	return e
}

type UsedIDStore struct {
	usedIds     map[uint32]struct{}
	expirations ExpiryHeap
	next        uint32
	limit       uint32
	base        uint32
}

func (s *UsedIDStore) Init(limit_low uint32, limit_high uint32, r []*db.UsedReservation) error {
	s.usedIds = make(map[uint32]struct{})
	s.base = limit_low
	s.limit = limit_high
	s.next = s.base
	for _, res := range r {
		s.usedIds[res.Id] = struct{}{}
		s.expirations = append(s.expirations, &entry{
			id:         res.Id,
			expiration: res.StopsAt.Unix(),
		})
	}
	heap.Init(&s.expirations)
	return nil
}

func (s *UsedIDStore) Next(now int64, start int64, end int64) (uint32, error) {
	if len(s.expirations) != 0 && s.expirations[0].expiration <= now {
		e := heap.Pop(&s.expirations).(*entry)
		heap.Push(&s.expirations, &entry{
			id:         e.id,
			expiration: end,
		})
		return e.id, nil
	}
	for ; s.next < s.limit; s.next++ {
		_, found := s.usedIds[s.next]
		if !found {
			s.usedIds[s.next] = struct{}{}
			heap.Push(&s.expirations, &entry{
				id:         s.next,
				expiration: end,
			})
			tmp := s.next
			s.next++
			return tmp, nil
		}
	}
	return 0, serrors.New("no free reservation id")
}

func (s *UsedIDStore) Migrate(new_limit_low uint32, new_limit_high uint32, r []*db.UsedReservation) error {
	if s.next > new_limit_high {
		return serrors.New("newLimit too small")
	}
	s.base = new_limit_low
	s.limit = new_limit_high

	if r != nil {
		clear(s.usedIds)
		clear(s.expirations)
		s.next = s.base
		for _, res := range r {
			s.usedIds[res.Id] = struct{}{}
			s.expirations = append(s.expirations, &entry{
				id:         res.Id,
				expiration: res.StopsAt.Unix(),
			})
		}
		heap.Init(&s.expirations)
	}
	return nil
}

func (s *UsedIDStore) Close() error {
	clear(s.usedIds)
	return nil
}
