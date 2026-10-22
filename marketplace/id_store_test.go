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
	"testing"
	"time"

	"github.com/scionproto/scion/marketplace"
	"github.com/scionproto/scion/marketplace/db"
	"github.com/stretchr/testify/assert"
)

func TestIDStoreNext(t *testing.T) {
	base, err := time.Parse(time.RFC3339, "2026-07-16T00:00:00Z")
	b := base.Unix()
	assert.NoError(t, err)
	store := marketplace.UsedIDStore{}
	err = store.Init(0, 3, []*db.UsedReservation{
		{
			Id:       0,
			StartsAt: b,
			StopsAt:  b + 3,
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
