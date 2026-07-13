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

package path

import (
	"github.com/scionproto/scion/pkg/slayers"
	"github.com/scionproto/scion/pkg/snet"
)

type HummReplyPather struct {
	BackupRepyPather snet.ReplyPather // Used if no bidirectional reservation is available.

	// reservations maintains a Hummingbird Reservation to each source who has sent a reverse
	// reservation to reach them. It is populated by SetState.
	reservations map[snet.SourceIdentifier]*Reservation

	// TODO clean the reservation map via a configurable callback.
}

var _ snet.StatefulReplyPather = (*HummReplyPather)(nil)

// NewHummReplyPather returns a HummReplyPather with its backup reply pather set to a regular
// DefaultReplyPather.
func NewHummReplyPather() *HummReplyPather {
	return &HummReplyPather{
		BackupRepyPather: snet.DefaultReplyPather{},
		reservations:     make(map[snet.SourceIdentifier]*Reservation),
	}
}

// SetState stores the necessary information for the Hummingbird reply pather to create a
// reservation. Being this reply pather run at AS A, the state is set when a packet is received
// by A from B, i.e. B->A. This packet contains some end2end extension options with the necessary
// serialized reservation state to reconstruct a valid reverse Reservation.
func (p *HummReplyPather) SetState(sourceId snet.SourceIdentifier, pkt snet.Packet) error {
	// Check if there is any bidirectional reservation information in this packet.
	serializedReservation := containedReversePathState(pkt.E2eExtnContents)
	if serializedReservation == nil {
		// No bidirectional reservation information. Bail.
		return nil
	}

	// Build the reverse reservation.
	originalPath := pkt.Path.(snet.RawPath) // Can't fail, it was checked by the caller.
	rsv, err := NewReservation(
		WithReverseFromBidirectional(serializedReservation, originalPath, pkt.Source.IA))
	if err != nil {
		return err
	}

	p.reservations[sourceId] = rsv
	return nil
}

func (r *HummReplyPather) ReplyPathTo(
	sourceId snet.SourceIdentifier,
	rpath snet.RawPath,
) (snet.DataplanePath, error) {
	// If we have a valid reversed reservation for this source,
	// return it already without reversing the current passed path.
	// This reversed reservation might have been constructed many packets ago.
	if rsv, ok := r.reservations[sourceId]; ok {
		return rsv, nil
	}

	// Otherwise, just reverse the hummingbird path.
	return r.ReplyPath(rpath)
}

// ReplyPath uses the embedded backup DefaultReplyPather to return the reply path.
func (r *HummReplyPather) ReplyPath(rpath snet.RawPath) (snet.DataplanePath, error) {
	return r.BackupRepyPather.ReplyPath(rpath)
}

// containedReversePathState extracts the reverse path information reservation option from the
// end to end extension and returns it, or nil if none is present.
func containedReversePathState(opts []*slayers.EndToEndOption) []byte {
	for _, opt := range opts {
		if opt.OptType == slayers.OptTypeReversePath {
			return opt.OptData
		}
	}
	return nil
}
