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

package hummingbird

import (
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/slayers"
	dpscion "github.com/scionproto/scion/pkg/slayers/path/scion"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

// BuildReverseReservationExtn serializes a reverse reservation into the
// reverse-path E2E extension carried by the forward reservation.
func BuildReverseReservationExtn(
	forwardPath snetpath.SCION,
	reverseDst addr.IA,
	reverseFlyovers []*snetpath.Hop,
) (*slayers.EndToEndExtn, error) {
	reversePath, err := reverseSCIONPath(forwardPath)
	if err != nil {
		return nil, err
	}
	reservation, err := snetpath.NewReservation(
		snetpath.WithDataplanePath(reversePath, reverseDst, reverseFlyovers),
	)
	if err != nil {
		return nil, err
	}
	state := make([]byte, reservation.SerializedLen())
	if err := reservation.Serialize(state); err != nil {
		return nil, err
	}
	return &slayers.EndToEndExtn{
		Options: []*slayers.EndToEndOption{{
			OptType: slayers.OptTypeReversePath,
			OptData: state,
		}},
	}, nil
}

func reverseSCIONPath(scionPath snetpath.SCION) (snetpath.SCION, error) {
	var dec dpscion.Decoded
	// Work on a copy so the caller's dataplane path bytes stay untouched.
	raw := append([]byte(nil), scionPath.Raw...)
	if err := dec.DecodeFromBytes(raw); err != nil {
		return snetpath.SCION{}, serrors.Wrap("decoding scion path", err)
	}
	reversed, err := dec.Reverse()
	if err != nil {
		return snetpath.SCION{}, serrors.Wrap("reversing scion path", err)
	}
	reversedDecoded, ok := reversed.(*dpscion.Decoded)
	if !ok {
		return snetpath.SCION{}, serrors.New("unexpected reversed path type")
	}
	return snetpath.NewSCIONFromDecoded(*reversedDecoded)
}
