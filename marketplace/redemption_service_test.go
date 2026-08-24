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

package marketplace_test

import (
	"math"
	"testing"
	"time"

	"github.com/scionproto/scion/marketplace"
	"github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/pkg/hummingbird/bwencoding"
	"github.com/stretchr/testify/assert"
)

func TestIDStore(t *testing.T) {
	base, err := time.Parse(time.RFC3339, "2026-07-16T00:00:00Z")
	b := base.Unix()
	assert.NoError(t, err)
	store := marketplace.UsedIDStore{}
	err = store.Init(0, 3, []*db.UsedReservation{
		{
			Id:       0,
			StartsAt: base,
			StopsAt:  base.Add(time.Second * 3),
		},
	})
	assert.NoError(t, err)
	nextId, err := store.Next(b, b, b+1)
	assert.NoError(t, err)
	assert.Equal(t, uint32(1), nextId)
	nextId, err = store.Next(b, b, b+2)
	assert.NoError(t, err)
	assert.Equal(t, uint32(2), nextId)
	nextId, err = store.Next(b+1, b+1, b+2)
	assert.NoError(t, err)
	assert.Equal(t, uint32(1), nextId)
	nextId, err = store.Next(b+1, b+1, b+2)
	assert.Error(t, err)
}

func TestIDStoreMigrate(t *testing.T) {
	base, err := time.Parse(time.RFC3339, "2026-07-16T00:00:00Z")
	assert.NoError(t, err)
	b := base.Unix()
	store := marketplace.UsedIDStore{}
	err = store.Init(0, 3, []*db.UsedReservation{
		{
			Id:       0,
			StartsAt: base,
			StopsAt:  base.Add(time.Second * 10),
		},
		{
			Id:       1,
			StartsAt: base,
			StopsAt:  base.Add(time.Second * 10),
		},
	})
	assert.NoError(t, err)
	err = store.Migrate(1, 4, []*db.UsedReservation{
		{
			Id:       1,
			StartsAt: base,
			StopsAt:  base.Add(time.Second * 10),
		},
	})
	assert.NoError(t, err)
	nextId, err := store.Next(b, b, b+10)
	assert.NoError(t, err)
	assert.Equal(t, uint32(2), nextId)
	nextId, err = store.Next(b, b, b+10)
	assert.NoError(t, err)
	assert.Equal(t, uint32(3), nextId)
	nextId, err = store.Next(b, b, b+10)
	assert.Error(t, err)
}

func TestEncoding(t *testing.T) {
	// The points an AS publishes are the bandwidths of the codepoints the
	// dataplane carries, so a delegated redemption service rounds a request to
	// one of them.
	encodings := make([]uint32, bwencoding.Codepoints)
	for codepoint := range encodings {
		encodings[codepoint] = bwencoding.EncodeBandwidth(uint16(codepoint))
	}
	s := &marketplace.RedemptionService{}
	s.SetEncodingPoints(encodings)

	assert.Equal(t, uint16(0), s.EncodeBandwidth(bwencoding.MinBwKbps))
	assert.Equal(t, uint16(1), s.EncodeBandwidth(bwencoding.MinBwKbps+1))
	assert.Equal(t, uint16(59), s.EncodeBandwidth(bwencoding.MinBwKbps+59))
	assert.Equal(t, uint16(60), s.EncodeBandwidth(bwencoding.MinBwKbps+60))
	assert.Equal(t, uint16(bwencoding.Codepoints-1), s.EncodeBandwidth(bwencoding.MaxBwKbps))
	// More than the largest reservation is capped at the largest codepoint.
	assert.Equal(t, uint16(bwencoding.Codepoints-1), s.EncodeBandwidth(math.MaxUint32))
}
