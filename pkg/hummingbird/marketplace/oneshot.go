// Copyright 2026 ETH Zurich
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

package marketplace

import (
	"context"

	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

// OneShotReservation is a single use Reserver.Reserve:
// it connects to the marketplace, reserves, and closes the connection again.
func OneShotReservation(
	ctx context.Context,
	p snet.Path,
	jwt string,
	querier snet.PathQuerier,
	topo snet.Topology,
	insecure bool,
	req ReservationRequest,
) (*snetpath.Reservation, error) {
	r := NewReserver(jwt, querier, topo, insecure)
	defer func() {
		if err := r.Close(); err != nil {
			log.Error("error closing the marketplace clients", "err", err)
		}
	}()
	return r.Reserve(ctx, p, req)
}
