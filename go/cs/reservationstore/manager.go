// Copyright 2021 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package reservationstore

import (
	"context"

	"github.com/scionproto/scion/go/cs/reservation/conf"
	"github.com/scionproto/scion/go/cs/reservationstorage"
	"github.com/scionproto/scion/go/lib/periodic"
)

// Manager takes care of the health of the segment reservations.
// TODO(juagargi) do the Manager interface
type Manager struct {
	store   reservationstorage.Store
	initial conf.Reservations
}

var _ periodic.Task = (*Manager)(nil)

func NewColibriManager(store reservationstorage.Store, initial conf.Reservations) *Manager {
	return &Manager{
		store:   store,
		initial: initial,
	}
}

func (m *Manager) Name() string {
	return "colibri.Manager"
}

func (m *Manager) Run(ctx context.Context) {
	// read configuration
}
