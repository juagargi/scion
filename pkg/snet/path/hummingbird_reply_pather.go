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
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/slayers"
	dphumm "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	"github.com/scionproto/scion/pkg/snet"
)

type HummReplyPather struct {
	// origSrcIA is the original source IA of the packet (the "sender" when setting the
	// state of the reply pather via a packet).
	origSrcIA addr.IA
	// reservation is the Hummingbird path in the already reversed direction, src IA is this IA.
	reservation *Reservation
}

// SetState stores the necessary information for the Hummingbird reply pather to create a
// reservation. Being this reply pather run at AS A, the state is set when a packet is received
// by A from B, i.e. B->A. This packet contains some end2end extension options with the necessary
// hops to reconstruct a valid Reservation.
//
// The MAC de-aggregation is done by recomputing the Hummingbird validation keys and XORing them
// to the current MAC fields.
func (p *HummReplyPather) SetState(pkt snet.Packet) error {
	// Record the sender.
	p.origSrcIA = pkt.Source.IA

	// Check if there is any bidirectional reservation information in this packet.
	serializedHops := containedReversePathState(pkt.E2eExtnContents)
	if serializedHops == nil {
		// No bidirectional reservation information. Bail.
		return nil
	}

	// 1. Deserialize the state into []Hop.
	hopSeq, err := DeserializeHops(serializedHops)
	if err != nil {
		return err
	}

	// 2. Deserialize the return path
	originalPath := pkt.Path.(snet.RawPath) // Can't fail, it was checked by the caller.
	if originalPath.PathType != dphumm.PathType {
		return serrors.New("bidirectional reservations supported only on hummingbird paths",
			"type", originalPath.PathType.String())
	}
	var dec dphumm.Decoded
	if err := dec.DecodeFromBytes(originalPath.Raw); err != nil {
		return serrors.Wrap("bidirectional reservation, decoding humm. path", err)
	}
	// Reverse in place.
	if _, err = dec.Reverse(); err != nil {
		return serrors.Wrap("cannot reverse hummingbird path", err)
	}
	snetHumm := &Reservation{
		Dec: &dec,
	}

	// 3. Build the reservation (with wrong, aggregated MACs)
	r, err := NewReservation(
		WithDataplanePath(snetHumm, pkt.Source.IA, hopSeq),
	)
	if err != nil {
		return serrors.Wrap("cannot build reservation", err)
	}

	// 4. De-aggregate MACs
	r.DeAggregateMACs(pkt.Destination.IA, uint16(len(pkt.Bytes)))

	p.reservation = r
	return nil
}

func (r HummReplyPather) ReplyPath(rpath snet.RawPath) (snet.DataplanePath, error) {
	// If we have a valid reversed reservation, return it already without reversing the current
	// passed path. This reversed reservation might have been constructed many packets ago.
	if r.reservation != nil {
		return r.reservation, nil
	}

	// Otherwise, reverse the hummingbird path.
	if rpath.PathType != dphumm.PathType {
		return nil, serrors.New("non hummingbird path type for a hummingbird reply pather",
			"path_type", rpath.PathType)
	}
	var dec dphumm.Decoded
	if err := dec.DecodeFromBytes(rpath.Raw); err != nil {
		return nil, serrors.Wrap("cannot decode hummingbird raw path", err)
	}

	// Reverse in place.
	_, err := dec.Reverse()
	if err != nil {
		return nil, serrors.Wrap("cannot reverse hummingbird path", err)
	}
	snetHumm := &Reservation{
		Dec: &dec,
	}

	// Construct a Reservation, with no flyovers.
	return NewReservation(
		WithDataplanePath(snetHumm, r.origSrcIA, nil),
	)
}

type HummReplyPath struct {
	OriginalPath snet.RawPath
	Reversed     *Reservation
}

func (p HummReplyPath) SetPath(s *slayers.SCION) error {
	return p.Reversed.SetPath(s)
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
