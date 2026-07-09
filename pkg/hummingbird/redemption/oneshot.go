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

package redemption

import (
	"context"
	"net"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/daemon"
	humm "github.com/scionproto/scion/pkg/hummingbird"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/slayers"
	dpscion "github.com/scionproto/scion/pkg/slayers/path/scion"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

// OneShotReservation redeems all flyovers for a path and returns a Hummingbird
// reservation ready to be used in the dataplane.
//
// If reverseRequestBw is greater than zero, the function also redeems the
// reverse-direction flyovers, serializes the reverse reservation into a reverse
// path E2E option, and attaches it to the returned forward reservation.
func OneShotReservation(
	ctx context.Context,
	sdConn daemon.Connector,
	localIP net.IP,
	p snet.Path,
	commonRequest humm.RedemptionRequestNoHop,
	reverseRequestBw uint16,
) (*snetpath.Reservation, error) {
	// Build a redemption client.
	redemptClient, err := NewRedemptionClient(ctx, sdConn, localIP)
	if err != nil {
		return nil, serrors.Wrap("new redemption client", err)
	}
	// Obtain the flyovers.
	flyovers, err := redemptClient.RedeemPathWithRequest(ctx, p, commonRequest)
	if err != nil {
		return nil, serrors.Wrap("redeeming flyovers", err)
	}

	// Convert the path to a dataplane path.
	scionPath, ok := p.Dataplane().(snetpath.SCION)
	if !ok {
		return nil, serrors.New("provided path must be of type scion")
	}

	// Bind the redeemed flyovers onto the forward SCION dataplane path.
	reservation, err := snetpath.NewReservation(
		snetpath.WithDataplanePath(scionPath, p.Destination(), flyovers),
	)
	if err != nil || reverseRequestBw == 0 {
		return reservation, err
	}

	// Build the reverse-direction reservation state and advertise it as an E2E
	// extension on the forward reservation.
	extn, err := reverseReservationExtn(ctx, redemptClient, p, scionPath, commonRequest, reverseRequestBw)
	if err != nil {
		return nil, err
	}
	reservation.SetReverseReservationExtn(extn)
	return reservation, nil
}

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

// reverseReservationExtn redeems the reverse flyovers and wraps the resulting
// reverse reservation into the reverse-path E2E extension carried by the
// forward reservation.
func reverseReservationExtn(
	ctx context.Context,
	redemptClient *RedemptionClient,
	p snet.Path,
	scionPath snetpath.SCION,
	commonRequest humm.RedemptionRequestNoHop,
	reverseRequestBw uint16,
) (*slayers.EndToEndExtn, error) {
	// Redeem reverse flyovers using the reversed hop sequence.
	flyovers, err := reverseFlyovers(ctx, redemptClient, p, commonRequest, reverseRequestBw)
	if err != nil {
		return nil, err
	}
	return BuildReverseReservationExtn(scionPath, p.Source(), flyovers)
}

// reverseFlyovers redeems flyovers for the reversed hop sequence by reusing
// the caller's request parameters and changing only the requested bandwidth.
func reverseFlyovers(
	ctx context.Context,
	redemptClient *RedemptionClient,
	p snet.Path,
	commonRequest humm.RedemptionRequestNoHop,
	reverseRequestBw uint16,
) ([]*snetpath.Hop, error) {
	hops, err := getHopsFromPath(p)
	if err != nil {
		return nil, err
	}

	// Derive the reverse hop sequence from the forward path and request those
	// flyovers with the caller-provided reverse bandwidth.
	reversedHops := reverseBaseHops(hops)
	reverseRequest := commonRequest
	reverseRequest.Bw = reverseRequestBw
	return redemptClient.RedeemHopsWithRequest(ctx, reversedHops, reverseRequest)
}

// reverseBaseHops returns the base-hop sequence for the reverse direction.
func reverseBaseHops(hops []snetpath.BaseHop) []snetpath.BaseHop {
	reversed := make([]snetpath.BaseHop, len(hops))
	for i, hop := range hops {
		// Reversing a hop swaps the ingress and egress interfaces and flips the
		// order of the AS sequence.
		reversed[len(hops)-1-i] = snetpath.BaseHop{
			IA:      hop.IA,
			Ingress: hop.Egress,
			Egress:  hop.Ingress,
		}
	}
	return reversed
}

// reverseSCIONPath decodes and reverses a SCION dataplane path while keeping
// the result in snet's SCION dataplane wrapper type.
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
