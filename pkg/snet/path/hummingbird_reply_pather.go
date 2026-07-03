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
	"crypto/cipher"
	"time"

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

var _ snet.StatefulReplyPather = (*HummReplyPather)(nil)

// SetState stores the necessary information for the Hummingbird reply pather to create a
// reservation. Being this reply pather run at AS A, the state is set when a packet is received
// by A from B, i.e. B->A. This packet contains some end2end extension options with the necessary
// serialized reservation state to reconstruct a valid reverse Reservation.
func (p *HummReplyPather) SetState(pkt snet.Packet) error {
	// Record the sender.
	p.origSrcIA = pkt.Source.IA

	// Check if there is any bidirectional reservation information in this packet.
	serializedReservation := containedReversePathState(pkt.E2eExtnContents)
	if serializedReservation == nil {
		// No bidirectional reservation information. Bail.
		return nil
	}

	// 1. Deserialize the serialized reverse reservation state.
	reverseReservation := &Reservation{}
	if err := reverseReservation.Deserialize(serializedReservation); err != nil {
		return serrors.Wrap("cannot deserialize reverse reservation state", err)
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
	if _, err := dec.Reverse(); err != nil {
		return serrors.Wrap("cannot reverse hummingbird path", err)
	}
	if len(reverseReservation.Hops) != len(dec.HopFields) {
		return serrors.New("reverse reservation state does not match reversed dataplane path",
			"reservation_hops", len(reverseReservation.Hops),
			"hop_fields", len(dec.HopFields),
		)
	}

	// 3. Rebind the reversed dataplane path onto the serialized reverse reservation state.
	reverseReservation.Dec = &dec
	reverseReservation.DstIA = pkt.Source.IA
	reverseReservation.Now = time.Now
	reverseReservation.blocksPerAk = make([]cipher.Block, len(reverseReservation.Hops))
	for i, hop := range reverseReservation.Hops {
		if hop == nil {
			continue
		}
		if err := reverseReservation.SetHopAndFlyover(uint8(i), hop); err != nil {
			return serrors.Wrap("cannot bind reverse reservation hop to dataplane path", err,
				"index", i)
		}
	}

	p.reservation = reverseReservation
	return nil
}

func (r *HummReplyPather) ReplyPath(rpath snet.RawPath) (snet.DataplanePath, error) {
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
