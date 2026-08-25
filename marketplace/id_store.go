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

	"github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/pkg/private/serrors"
)

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

func (s *UsedIDStore) Close() error {
	clear(s.usedIds)
	clear(s.expirations)
	s.expirations = nil
	return nil
}
