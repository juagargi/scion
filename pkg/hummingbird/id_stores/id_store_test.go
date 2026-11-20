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

package id_stores_test

import (
	"testing"

	"github.com/scionproto/scion/pkg/hummingbird/id_stores"
	"github.com/stretchr/testify/assert"
)

func TestIDStoreNextAllocatesWithinConfiguredRange(t *testing.T) {
	store := id_stores.UsedIDStore{}
	assert.NoError(t, store.Init(10, 13, nil))

	id, err := store.Next(0, 0, 1)
	assert.NoError(t, err)
	assert.Equal(t, uint32(10), id)
	id, err = store.Next(0, 0, 1)
	assert.NoError(t, err)
	assert.Equal(t, uint32(11), id)
	id, err = store.Next(0, 0, 1)
	assert.NoError(t, err)
	assert.Equal(t, uint32(12), id)
	_, err = store.Next(0, 0, 1)
	assert.Error(t, err)
}

func TestIDStoreNextSkipsReservations(t *testing.T) {
	store := id_stores.UsedIDStore{}
	assert.NoError(t, store.Init(0, 4, []id_stores.Reservation{
		{Id: 0, StopsAt: 100},
		{Id: 2, StopsAt: 100},
	}))

	id, err := store.Next(0, 0, 1)
	assert.NoError(t, err)
	assert.Equal(t, uint32(1), id)
	id, err = store.Next(0, 0, 1)
	assert.NoError(t, err)
	assert.Equal(t, uint32(3), id)
}

func TestIDStoreNextReservationReuseBoundaries(t *testing.T) {
	store := id_stores.UsedIDStore{}
	assert.NoError(t, store.Init(0, 1, []id_stores.Reservation{
		{Id: 0, StopsAt: 10},
	}))

	_, err := store.Next(10, 14, 20)
	assert.Error(t, err)

	id, err := store.Next(10, 15, 20)
	assert.NoError(t, err)
	assert.Equal(t, uint32(0), id)

	_, err = store.Next(20, 24, 30)
	assert.Error(t, err)
	id, err = store.Next(20, 25, 30)
	assert.NoError(t, err)
	assert.Equal(t, uint32(0), id)
}

func TestIDStoreNextReusesReservationsInExpirationOrder(t *testing.T) {
	store := id_stores.UsedIDStore{}
	assert.NoError(t, store.Init(0, 2, []id_stores.Reservation{
		{Id: 0, StopsAt: 20},
		{Id: 1, StopsAt: 10},
	}))

	id, err := store.Next(10, 15, 30)
	assert.NoError(t, err)
	assert.Equal(t, uint32(1), id)
	id, err = store.Next(20, 25, 40)
	assert.NoError(t, err)
	assert.Equal(t, uint32(0), id)

	_, err = store.Next(29, 34, 50)
	assert.Error(t, err)
	id, err = store.Next(30, 35, 50)
	assert.NoError(t, err)
	assert.Equal(t, uint32(1), id)
}

func TestIDStoreInitResetsStore(t *testing.T) {
	store := id_stores.UsedIDStore{}
	assert.NoError(t, store.Init(0, 3, []id_stores.Reservation{
		{Id: 0, StopsAt: 10},
	}))

	id, err := store.Next(0, 0, 20)
	assert.NoError(t, err)
	assert.Equal(t, uint32(1), id)

	assert.NoError(t, store.Init(10, 13, []id_stores.Reservation{
		{Id: 11, StopsAt: 100},
	}))

	id, err = store.Next(0, 0, 20)
	assert.NoError(t, err)
	assert.Equal(t, uint32(10), id)
	id, err = store.Next(0, 0, 20)
	assert.NoError(t, err)
	assert.Equal(t, uint32(12), id)
	_, err = store.Next(0, 0, 20)
	assert.Error(t, err)
}
