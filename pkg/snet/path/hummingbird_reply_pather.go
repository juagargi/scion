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
	"fmt"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/slayers"
	"github.com/scionproto/scion/pkg/snet"
)

type HummReplyPather struct {
	// origSrcIA is the original source IA of the packet (the "sender" when setting the
	// state of the reply pather via a packet).
	origSrcIA addr.IA
	// reservation is the Hummingbird path in the already reversed direction, src IA is this IA.
	reservation      *Reservation
	BackupRepyPather snet.ReplyPather // Used if no bidirectional reservation is available.
}

var _ snet.StatefulReplyPather = (*HummReplyPather)(nil)

// NewHummReplyPather returns a HummReplyPather with its backup reply pather set to a regular
// DefaultReplyPather.
func NewHummReplyPather() *HummReplyPather {
	return &HummReplyPather{
		BackupRepyPather: snet.DefaultReplyPather{},
	}
}

// SetState stores the necessary information for the Hummingbird reply pather to create a
// reservation. Being this reply pather run at AS A, the state is set when a packet is received
// by A from B, i.e. B->A. This packet contains some end2end extension options with the necessary
// serialized reservation state to reconstruct a valid reverse Reservation.
func (p *HummReplyPather) SetState(pkt snet.Packet) error {
	fmt.Println("deleteme humm reply pather SetState")
	// Record the sender.
	p.origSrcIA = pkt.Source.IA
	fmt.Printf("deleteme humm reply pather SetState orig src IA = %s\n", p.origSrcIA)

	// Check if there is any bidirectional reservation information in this packet.
	serializedReservation := containedReversePathState(pkt.E2eExtnContents)
	if serializedReservation == nil {
		fmt.Println("deleteme humm reply pather SetState no bidirectional reservation")
		// No bidirectional reservation information. Bail.
		return nil
	}
	fmt.Println("deleteme humm reply pather SetState we have a bidirectional reservation")

	// Build the reverse reservation.
	originalPath := pkt.Path.(snet.RawPath) // Can't fail, it was checked by the caller.
	var err error
	p.reservation, err = NewReservation(
		WithReverseFromBidirectional(serializedReservation, originalPath, p.origSrcIA))
	if err != nil {
		return err
	}
	return nil
}

func (r *HummReplyPather) ReplyPath(rpath snet.RawPath) (snet.DataplanePath, error) {
	fmt.Println("deleteme humm reply pather ReplyPath 1")
	// If we have a valid reversed reservation, return it already without reversing the current
	// passed path. This reversed reservation might have been constructed many packets ago.
	if r.reservation != nil {
		fmt.Println("deleteme humm reply pather ReplyPath using existing reservation")
		return r.reservation, nil
	}

	// Otherwise, just reverse the hummingbird path.
	fmt.Println("deleteme humm reply pather ReplyPath 2")
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
