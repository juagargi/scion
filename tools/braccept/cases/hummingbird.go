// Copyright 2026 SCION Association
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

package cases

import (
	"crypto/aes"
	"hash"
	"net"
	"path/filepath"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/private/util"
	"github.com/scionproto/scion/pkg/slayers"
	"github.com/scionproto/scion/pkg/slayers/path"
	"github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	"github.com/scionproto/scion/tools/braccept/runner"
)

// Hummingbird acceptance cases inject packets on one veth and compare
// the router output byte-for-byte with the expected packet.
//
// The AS under test is 1-ff00:0:1. The relevant interfaces (see
// acceptance/router_multi/conf/topology.json) are:
//
//	121 -> PEER   (veth_121_host, 192.168.12.x)
//	131 -> PARENT (veth_131_host, 192.168.13.x)
//	141 -> CHILD  (veth_141_host, 192.168.14.x)
//	151 -> CHILD  (veth_151_host, 192.168.15.x)
//	internal      (veth_int_host, 192.168.0.x)
//
// Flyover cases receive the router-derived secret from main.go. The MAC helpers
// mirror router/dataplane_hbird_test.go and must remain in sync with the router.
//
//
// Hummingbird acceptance coverage.
//
//	Behavior                              Best-effort  Flyover
//	Inbound delivery                      x            x
//	Inbound, converted reversed path      N/A          N/A
//	Outbound forwarding                   x            x
//	BR transit, construction direction    x            x
//	BR transit, reverse direction         x            x
//	Direct AS transit, ingress BR         x            x
//	Direct AS transit, egress BR          x            x
//	AS-transit cross-over, ingress BR     x            x
//	AS-transit cross-over, egress BR      x            x
//	Same-BR cross-over                    x            x
//	Peering boundary, construction dir.   x            x
//	Peering boundary, reverse dir.        x            x
//	After peering, downstream             x            x
//	Before peering, upstream              x            x
//	Malformed current-hop alignment       x            x
//	Invalid hop MAC / SCMP                x            x
//	Invalid source IA / SCMP              x            x
//	Invalid destination IA / SCMP         x            x
//	Invalid outbound source IA / SCMP     x            x
//	Invalid outbound destination IA/SCMP  x            x
//	Ingress router alert                  x            x
//	Egress router alert                   x            x
//	Expired reservation                   N/A          x
//	Stale/future packet freshness         N/A          x
//	Reservation exceeds bandwidth         N/A          x
//
// Notes on other not present test cases from router/dataplane_hbird_test.go:
// Reversed-path conversion is a test-fixture operation, not behavior visible on the wire.
// Key lifecycle, token-bucket identity and concurrency, priority labels, and token accounting
// remain unit-only as they are internal state rather than distinct wire behavior.

const hbirdPayload = "actualpayloadbytes"

// hbirdScionUDPPayloadLen is the SCION/UDP header (8B) plus the payload. It is
// used to fix the SCION PayloadLen before computing the flyover MAC, which
// depends on the total packet length as seen by the router.
const hbirdScionUDPPayloadLen = 8 + len(hbirdPayload)

// HummingbirdBestEffortChildToParent checks BR transit, reverse direction, best-effort.
// It matches TestProcessHbirdPacket/brtransit_non_consdir_best-effort.
func HummingbirdBestEffortChildToParent(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdBRTransit(
		artifactsDir, mac, nil, false, false, 3, true, "HummingbirdBestEffortChildToParent")
}

// HummingbirdBestEffortParentToChild checks BR transit, construction direction, best-effort.
// It matches TestProcessHbirdPacket/brtransit_consdir_best-effort.
func HummingbirdBestEffortParentToChild(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdBRTransit(
		artifactsDir, mac, nil, false, true, 3, true, "HummingbirdBestEffortParentToChild")
}

// HummingbirdMalformedCurrentHopAlignment checks malformed current-hop alignment, best-effort.
// CurrHF points into the middle of a three-line hop.
// It matches TestProcessHbirdPacket/malformed_current_hop_alignment_best-effort.
func HummingbirdMalformedCurrentHopAlignment(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdBRTransit(
		artifactsDir, mac, nil, false, true, 4, false, "HummingbirdMalformedCurrentHopAlignment")
}

// HummingbirdMalformedCurrentHopAlignmentFlyover checks malformed current-hop alignment, flyover.
// CurrHF points into a five-line flyover.
// It matches TestProcessHbirdPacket/malformed_current_hop_alignment_flyover.
func HummingbirdMalformedCurrentHopAlignmentFlyover(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdMalformedFlyover(artifactsDir, mac, sv)
}

// HummingbirdFlyoverParentToChild checks BR transit, construction direction, flyover.
// It matches TestProcessHbirdPacket/brtransit_consdir_flyover.
func HummingbirdFlyoverParentToChild(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdBRTransit(
		artifactsDir, mac, sv, true, true, 3, true, "HummingbirdFlyoverParentToChild")
}

// HummingbirdFlyoverInbound checks inbound delivery, flyover.
// It matches TestProcessHbirdPacket/inbound_flyover.
func HummingbirdFlyoverInbound(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdInbound(artifactsDir, mac, sv, true, "HummingbirdFlyoverInbound")
}

// HummingbirdFlyoverOutbound checks outbound forwarding, flyover.
// It matches TestProcessHbirdPacket/outbound_flyover.
func HummingbirdFlyoverOutbound(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdOutbound(
		artifactsDir, mac, sv, true, 0, 301, 129, []byte(hbirdPayload), "HummingbirdFlyoverOutbound")
}

// HummingbirdExpiredReservation checks an expired reservation, flyover.
// It matches TestProcessHbirdPacket/reservation_expired_flyover.
func HummingbirdExpiredReservation(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdOutbound(
		artifactsDir, mac, sv, true, 0, 2, 129, []byte(hbirdPayload), "HummingbirdExpiredReservation")
}

// HummingbirdBandwidthExceeded checks reservation bandwidth exceeded, flyover.
// It matches TestProcessHbirdPacket/reservation_exceeds_bandwidth_flyover.
func HummingbirdBandwidthExceeded(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdOutbound(
		artifactsDir, mac, sv, true, 0, 301, 1, make([]byte, 512), "HummingbirdBandwidthExceeded")
}

// HummingbirdStaleFlyover checks stale packet freshness, flyover.
// It matches TestProcessHbirdPacket/freshness_stale_flyover.
func HummingbirdStaleFlyover(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdOutbound(
		artifactsDir, mac, sv, true, -6*time.Second, 301, 129, []byte(hbirdPayload),
		"HummingbirdStaleFlyover")
}

// HummingbirdFutureFlyover checks future packet freshness, flyover.
// It matches TestProcessHbirdPacket/freshness_future_flyover.
func HummingbirdFutureFlyover(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdOutbound(
		artifactsDir, mac, sv, true, 6*time.Second, 301, 129, []byte(hbirdPayload),
		"HummingbirdFutureFlyover")
}

// HummingbirdBestEffortChildToChildXover checks same-BR cross-over, best-effort.
// It matches TestProcessHbirdPacket/brtransit_xover_best-effort.
func HummingbirdBestEffortChildToChildXover(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdChildToChildXover(
		artifactsDir, mac, nil, false, "HummingbirdBestEffortChildToChildXover")
}

// HummingbirdBadFlyoverMAC checks invalid hop MAC / SCMP, flyover.
// It matches TestProcessHbirdSCMP/invalid_mac_inbound_flyover.
func HummingbirdBadFlyoverMAC(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdSCMPFailureCase(
		artifactsDir, mac, sv, hbirdBadFlyoverMAC, "HummingbirdBadFlyoverMAC")
}

// HummingbirdBadBestEffortMAC checks invalid hop MAC / SCMP, best-effort.
// It matches TestProcessHbirdSCMP/invalid_mac_inbound_best-effort.
func HummingbirdBadBestEffortMAC(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdSCMPFailureCase(
		artifactsDir, mac, sv, hbirdBadBestEffortMAC, "HummingbirdBadBestEffortMAC")
}

// HummingbirdInvalidSourceIA checks invalid source IA / SCMP, best-effort.
// It matches TestProcessHbirdSCMP/invalid_source_ia_inbound_best-effort.
func HummingbirdInvalidSourceIA(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdSCMPFailureCase(
		artifactsDir, mac, sv, hbirdInvalidSourceIA, "HummingbirdInvalidSourceIA")
}

// HummingbirdInvalidDestinationIA checks invalid destination IA / SCMP, best-effort.
// It matches TestProcessHbirdSCMP/invalid_destination_ia_inbound_best-effort.
func HummingbirdInvalidDestinationIA(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdSCMPFailureCase(
		artifactsDir, mac, sv, hbirdInvalidDestinationIA, "HummingbirdInvalidDestinationIA")
}

// HummingbirdInvalidSourceIAFlyover checks invalid source IA / SCMP, flyover.
// It matches TestProcessHbirdSCMP/invalid_source_ia_inbound_flyover.
func HummingbirdInvalidSourceIAFlyover(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdSCMPFailureCase(
		artifactsDir, mac, sv, hbirdInvalidSourceIAFlyover, "HummingbirdInvalidSourceIAFlyover")
}

// HummingbirdInvalidDestinationIAFlyover checks invalid destination IA / SCMP, flyover.
// It matches TestProcessHbirdSCMP/invalid_destination_ia_inbound_flyover.
func HummingbirdInvalidDestinationIAFlyover(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdSCMPFailureCase(artifactsDir, mac, sv,
		hbirdInvalidDestinationIAFlyover, "HummingbirdInvalidDestinationIAFlyover")
}

// HummingbirdInvalidSourceIAOutbound checks invalid outbound source IA / SCMP, best-effort.
// It matches TestProcessHbirdSCMP/invalid_source_ia_outbound_best-effort.
func HummingbirdInvalidSourceIAOutbound(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdOutboundSCMPFailureCase(
		artifactsDir, mac, sv, hbirdInvalidSourceIA, "HummingbirdInvalidSourceIAOutbound")
}

// HummingbirdInvalidDestinationIAOutbound checks invalid outbound destination IA / SCMP,
// best-effort. It matches TestProcessHbirdSCMP/invalid_destination_ia_outbound_best-effort.
func HummingbirdInvalidDestinationIAOutbound(
	artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdOutboundSCMPFailureCase(
		artifactsDir, mac, sv, hbirdInvalidDestinationIA, "HummingbirdInvalidDestinationIAOutbound")
}

// HummingbirdInvalidSourceIAOutboundFlyover checks invalid outbound source IA / SCMP, flyover.
// It matches TestProcessHbirdSCMP/invalid_source_ia_outbound_flyover.
func HummingbirdInvalidSourceIAOutboundFlyover(
	artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdOutboundSCMPFailureCase(artifactsDir, mac, sv,
		hbirdInvalidSourceIAFlyover, "HummingbirdInvalidSourceIAOutboundFlyover")
}

// HummingbirdInvalidDestinationIAOutboundFlyover checks invalid outbound destination IA /
// SCMP, flyover. It matches TestProcessHbirdSCMP/invalid_destination_ia_outbound_flyover.
func HummingbirdInvalidDestinationIAOutboundFlyover(
	artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdOutboundSCMPFailureCase(artifactsDir, mac, sv,
		hbirdInvalidDestinationIAFlyover, "HummingbirdInvalidDestinationIAOutboundFlyover")
}

// HummingbirdIngressRouterAlert checks ingress router alert, best-effort.
// It matches TestProcessHbirdRouterAlert/ingress_router_alert_best-effort.
func HummingbirdIngressRouterAlert(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdRouterAlertCase(
		artifactsDir, mac, sv, false, true, "HummingbirdIngressRouterAlert")
}

// HummingbirdEgressRouterAlert checks egress router alert, best-effort.
// It matches TestProcessHbirdRouterAlert/egress_router_alert_best-effort.
func HummingbirdEgressRouterAlert(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdRouterAlertCase(
		artifactsDir, mac, sv, false, false, "HummingbirdEgressRouterAlert")
}

// HummingbirdIngressRouterAlertFlyover checks ingress router alert, flyover.
// It matches TestProcessHbirdRouterAlert/ingress_router_alert_flyover.
func HummingbirdIngressRouterAlertFlyover(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdRouterAlertCase(
		artifactsDir, mac, sv, true, true, "HummingbirdIngressRouterAlertFlyover")
}

// HummingbirdEgressRouterAlertFlyover checks egress router alert, flyover.
// It matches TestProcessHbirdRouterAlert/egress_router_alert_flyover.
func HummingbirdEgressRouterAlertFlyover(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdRouterAlertCase(
		artifactsDir, mac, sv, true, false, "HummingbirdEgressRouterAlertFlyover")
}

// HummingbirdBestEffortInbound checks inbound delivery, best-effort.
// It matches TestProcessHbirdPacket/inbound_best-effort.
func HummingbirdBestEffortInbound(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdInbound(artifactsDir, mac, nil, false, "HummingbirdBestEffortInbound")
}

// HummingbirdBestEffortOutbound checks outbound forwarding, best-effort.
// It matches TestProcessHbirdPacket/outbound_best-effort.
func HummingbirdBestEffortOutbound(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdOutbound(
		artifactsDir, mac, nil, false, 0, 0, 0, []byte(hbirdPayload), "HummingbirdBestEffortOutbound")
}

// HummingbirdBestEffortChildToInternalParent checks direct AS transit, ingress BR, best-effort.
// It matches TestProcessHbirdPacket/astransit_direct_ingress_best-effort.
func HummingbirdBestEffortChildToInternalParent(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdDirectASTransit(artifactsDir, mac, nil, false, false,
		"HummingbirdBestEffortChildToInternalParent")
}

// HummingbirdFlyoverChildToInternalParent checks direct AS transit, ingress BR, flyover.
// It matches TestProcessHbirdPacket/astransit_direct_ingress_flyover.
func HummingbirdFlyoverChildToInternalParent(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdDirectASTransit(artifactsDir, mac, sv, true, false,
		"HummingbirdFlyoverChildToInternalParent")
}

// HummingbirdBestEffortInternalParentToChild checks direct AS transit, egress BR, best-effort.
// It matches TestProcessHbirdPacket/astransit_direct_egress_best-effort.
func HummingbirdBestEffortInternalParentToChild(
	artifactsDir string,
	mac hash.Hash,
) runner.Case {
	return hummingbirdDirectASTransit(artifactsDir, mac, nil, false, true,
		"HummingbirdBestEffortInternalParentToChild")
}

// HummingbirdFlyoverInternalParentToChild checks direct AS transit, egress BR, flyover.
// It matches TestProcessHbirdPacket/astransit_direct_egress_flyover.
func HummingbirdFlyoverInternalParentToChild(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdDirectASTransit(artifactsDir, mac, sv, true, true,
		"HummingbirdFlyoverInternalParentToChild")
}

// HummingbirdFlyoverChildToParentNonConsDir checks BR transit, reverse direction, flyover.
// It matches TestProcessHbirdPacket/brtransit_non_consdir_flyover.
func HummingbirdFlyoverChildToParentNonConsDir(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdBRTransit(
		artifactsDir, mac, sv, true, false, 3, true, "HummingbirdFlyoverChildToParentNonConsDir")
}

// HummingbirdFlyoverChildToChildXover checks same-BR cross-over, flyover.
// It matches TestProcessHbirdPacket/brtransit_xover_flyover.
func HummingbirdFlyoverChildToChildXover(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdChildToChildXover(
		artifactsDir, mac, sv, true, "HummingbirdFlyoverChildToChildXover")
}

// HummingbirdFlyoverXoverASTransitIngress checks AS-transit cross-over, ingress BR, flyover.
// It matches TestProcessHbirdPacket/astransit_xover_ingress_flyover.
func HummingbirdFlyoverXoverASTransitIngress(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdXoverASTransitIngress(
		artifactsDir, mac, sv, true, "HummingbirdFlyoverXoverASTransitIngress")
}

// HummingbirdFlyoverXoverASTransitEgress checks AS-transit cross-over, egress BR, flyover.
// It matches TestProcessHbirdPacket/astransit_xover_egress_flyover.
func HummingbirdFlyoverXoverASTransitEgress(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdXoverASTransitEgress(
		artifactsDir, mac, sv, true, "HummingbirdFlyoverXoverASTransitEgress")
}

// HummingbirdBestEffortXoverASTransitIngress checks AS-transit cross-over, ingress BR,
// best-effort. It matches TestProcessHbirdPacket/astransit_xover_ingress_best-effort.
func HummingbirdBestEffortXoverASTransitIngress(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdXoverASTransitIngress(
		artifactsDir, mac, nil, false, "HummingbirdBestEffortXoverASTransitIngress")
}

// HummingbirdBestEffortXoverASTransitEgress checks AS-transit cross-over, egress BR,
// best-effort. It matches TestProcessHbirdPacket/astransit_xover_egress_best-effort.
func HummingbirdBestEffortXoverASTransitEgress(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdXoverASTransitEgress(
		artifactsDir, mac, nil, false, "HummingbirdBestEffortXoverASTransitEgress")
}

// HummingbirdFlyoverChildToPeer checks peering boundary, reverse direction, flyover.
// It matches TestProcessHbirdPacket/brtransit_peering_non_consdir_flyover.
func HummingbirdFlyoverChildToPeer(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdPeeringCase(
		artifactsDir, mac, sv, true, false, false, "HummingbirdFlyoverChildToPeer")
}

// HummingbirdFlyoverPeerToChild checks peering boundary, construction direction, flyover.
// It matches TestProcessHbirdPacket/brtransit_peering_consdir_flyover.
func HummingbirdFlyoverPeerToChild(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdPeeringCase(
		artifactsDir, mac, sv, true, true, false, "HummingbirdFlyoverPeerToChild")
}

// HummingbirdBestEffortChildToPeer checks peering boundary, reverse direction, best-effort.
// It matches TestProcessHbirdPacket/brtransit_peering_non_consdir_best-effort.
func HummingbirdBestEffortChildToPeer(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdPeeringCase(artifactsDir, mac, nil, false, false, false,
		"HummingbirdBestEffortChildToPeer")
}

// HummingbirdBestEffortPeerToChild checks peering boundary, construction direction, best-effort.
// It matches TestProcessHbirdPacket/brtransit_peering_consdir_best-effort.
func HummingbirdBestEffortPeerToChild(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdPeeringCase(artifactsDir, mac, nil, false, true, false,
		"HummingbirdBestEffortPeerToChild")
}

// HummingbirdBestEffortPeeringDownstream checks after peering, downstream, best-effort.
// It matches TestProcessHbirdPacket/peering_consdir_downstream_best-effort.
func HummingbirdBestEffortPeeringDownstream(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdPeeringCase(artifactsDir, mac, nil, false, true, true,
		"HummingbirdBestEffortPeeringDownstream")
}

// HummingbirdFlyoverPeeringDownstream checks after peering, downstream, flyover.
// It matches TestProcessHbirdPacket/peering_consdir_downstream_flyover.
func HummingbirdFlyoverPeeringDownstream(
	artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdPeeringCase(artifactsDir, mac, sv, true, true, true,
		"HummingbirdFlyoverPeeringDownstream")
}

// HummingbirdBestEffortPeeringUpstream checks before peering, upstream, best-effort.
// It matches TestProcessHbirdPacket/peering_non_consdir_upstream_best-effort.
func HummingbirdBestEffortPeeringUpstream(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdPeeringCase(artifactsDir, mac, nil, false, false, true,
		"HummingbirdBestEffortPeeringUpstream")
}

// HummingbirdFlyoverPeeringUpstream checks before peering, upstream, flyover.
// It matches TestProcessHbirdPacket/peering_non_consdir_upstream_flyover.
func HummingbirdFlyoverPeeringUpstream(
	artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdPeeringCase(artifactsDir, mac, sv, true, false, true,
		"HummingbirdFlyoverPeeringUpstream")
}

// hummingbirdBRTransit builds BR-transit cases: a single-segment path whose
// middle hop is the current hop, entering on one external interface (child 141 /
// parent 131) and leaving on the other. With flyover the current hop carries a
// reservation the router verifies and de-aggregates, advancing by a flyover hop;
// best-effort forwards with the plain SCION MAC and advances by a regular hop.
// Against construction direction the ingress SegID is derived from the SCION MAC.
// currHF/expectPacket support the malformed-alignment best-effort case, where
// CurrHF points into the middle of the hop and no packet is expected. The
// best-effort and flyover fixtures use different (never-verified) surrounding
// hops, preserved here per mode.
func hummingbirdBRTransit(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	flyover bool,
	consDir bool,
	currHF uint8,
	expectPacket bool,
	name string,
) runner.Case {
	// Up segment (against construction direction): enter on child 141, exit on
	// parent 131. Current hop is the middle hop (line 3).
	now := time.Now()
	current := hummingbird.FlyoverHopField{
		HopField: path.HopField{ConsIngress: 131, ConsEgress: 141},
	}
	first := path.HopField{ConsIngress: 411, ConsEgress: 0}
	last := path.HopField{ConsIngress: 0, ConsEgress: 311}
	segLines := uint8(9) // Three best-effort hops.
	advance := hummingbird.HopLines
	if flyover {
		current.Flyover = true
		current.ResID = 42
		current.Bw = 129
		current.ResStartTime = 5
		current.Duration = 301
		first = path.HopField{ConsIngress: 0, ConsEgress: 311}
		last = path.HopField{ConsIngress: 411, ConsEgress: 0}
		segLines = 3 + hummingbird.FlyoverLines + 3
		advance = hummingbird.FlyoverLines
	}
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    currHF,
				SegLen:    [3]uint8{segLines, 0, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   1,
			NumLines: int(segLines),
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: consDir, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: first},
			current,
			{HopField: last},
		},
	}

	srcIA, dstIA := "1-ff00:0:4", "1-ff00:0:3"
	srcHost, dstHost := "172.16.4.1", "174.16.3.1"
	inputLink, outputLink := hbirdExternalInput(141), hbirdExternalOutput(131)
	if consDir {
		srcIA, dstIA = "1-ff00:0:3", "1-ff00:0:4"
		srcHost, dstHost = "172.16.3.1", "174.16.4.1"
		inputLink, outputLink = hbirdExternalInput(131), hbirdExternalOutput(141)
	}
	scionL := hbirdSCION(srcIA, dstIA, srcHost, dstHost, dpath)

	// The plain SCION MAC is reused for both the de-aggregated value and the SegID
	// update, so it is never recomputed against a mutated SegID.
	scionMac := path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	if flyover {
		// The aggregate MAC must be computed with the packet length the router sees.
		scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)
		dpath.HopFields[1].HopField.Mac = hbirdAggregateMAC(
			mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[1], dpath.PathMeta)
	} else {
		dpath.HopFields[1].HopField.Mac = scionMac
	}
	if !consDir {
		// Against construction direction: the ingress SegID is derived from the SCION MAC.
		dpath.InfoFields[0].UpdateSegID(scionMac)
	}

	input := hbirdSerializeUDP(inputLink, scionL, []byte(hbirdPayload))
	if !expectPacket {
		return runner.Case{
			Name: name, WriteTo: inputLink.device, ReadFrom: "no_pkt_expected",
			Input: input, Want: nil, StoreDir: filepath.Join(artifactsDir, name),
		}
	}

	// Expected: forwarded to the far interface, path advanced by one hop; SegID
	// updated with the SCION MAC (against construction direction this is a second,
	// self-canceling XOR). Flyover de-aggregates the current hop MAC.
	dpath.HopFields[1].HopField.Mac = scionMac
	if err := dpath.IncPath(advance); err != nil {
		panic(err)
	}
	dpath.InfoFields[0].UpdateSegID(scionMac)
	want := hbirdSerializeUDP(outputLink, scionL, []byte(hbirdPayload))
	return hbirdRunnerCase(artifactsDir, name, inputLink.device, outputLink.device, input, want)
}

// hummingbirdMalformedFlyover builds a valid flyover encoding whose CurrHF
// points at its second line, which the router must discard as malformed.
func hummingbirdMalformedFlyover(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF: 4, SegLen: [3]uint8{11, 0, 0}, BaseTS: util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF: 1, NumLines: 11,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 311}},
			{HopField: path.HopField{ConsIngress: 131, ConsEgress: 141},
				Flyover: true, ResID: 42, Bw: 129, ResStartTime: 5, Duration: 301},
			{HopField: path.HopField{ConsIngress: 411, ConsEgress: 0}},
		},
	}
	scionL := hbirdSCION(
		"1-ff00:0:3", "1-ff00:0:4", "172.16.3.1", "174.16.4.1", dpath)
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)
	// MAC calculation uses the aligned metadata; only CurrHF is malformed.
	meta := dpath.PathMeta
	meta.CurrHF = 3
	dpath.HopFields[1].HopField.Mac = hbirdAggregateMAC(
		mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[1], meta)
	inputLink := hbirdExternalInput(131)
	input := hbirdSerializeUDP(inputLink, scionL, []byte(hbirdPayload))
	return hbirdRunnerCase(artifactsDir, "HummingbirdMalformedCurrentHopAlignmentFlyover",
		inputLink.device, "no_pkt_expected", input, nil)
}

// hummingbirdInbound tests a Hummingbird packet with the last (destination-AS)
// hop as the current hop, arriving from a child and delivered to a local host.
// Analogue of ChildToInternalHost. With flyover, the router verifies the
// aggregate MAC and de-aggregates it; best-effort delivers with the plain SCION
// MAC. In both cases the path is not advanced.
func hummingbirdInbound(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	flyover bool,
	name string,
) runner.Case {
	const endhostPort = 21000
	options := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

	// Construction direction, last hop enters this AS on child 141 (ConsIngress).
	now := time.Now()
	lastHopLines := uint8(hummingbird.HopLines)
	last := hummingbird.FlyoverHopField{
		HopField: path.HopField{ConsIngress: 141, ConsEgress: 0},
	}
	if flyover {
		lastHopLines = hummingbird.FlyoverLines
		last.Flyover = true
		last.ResID = 42
		last.Bw = 129
		last.ResStartTime = 5
		last.Duration = 301
	}
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    6, // third hop (two regular hops precede it)
				SegLen:    [3]uint8{3 + 3 + lastHopLines, 0, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   1,
			NumLines: 3 + 3 + int(lastHopLines),
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 41, ConsEgress: 40}},
			{HopField: path.HopField{ConsIngress: 31, ConsEgress: 30}},
			last,
		},
	}

	scionL := hbirdSCION("1-ff00:0:4", "1-ff00:0:1", "172.16.4.1", "192.168.0.51", dpath)
	if flyover {
		// The aggregate MAC depends on the packet length the router sees.
		scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)
		dpath.HopFields[2].HopField.Mac = hbirdAggregateMAC(
			mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[2], dpath.PathMeta)
	} else {
		dpath.HopFields[2].HopField.Mac =
			path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[2].HopField, nil)
	}

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 2345
	scionudp.DstPort = uint16(endhostPort)
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	inputLink := hbirdExternalInput(141)
	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		inputLink.ethernet, inputLink.ip, inputLink.udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: delivered to the local host 192.168.0.51 on the internal
	// interface; the path is not advanced. Flyover de-aggregates the current hop
	// MAC; best-effort leaves it unchanged.
	if flyover {
		dpath.HopFields[2].HopField.Mac =
			path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[2].HopField, nil)
	}
	outputLink := hbirdInternalOutput(51, endhostPort)
	want := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(want, options,
		outputLink.ethernet, outputLink.ip, outputLink.udp,
		scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return hbirdRunnerCase(
		artifactsDir, name, inputLink.device, outputLink.device, input.Bytes(), want.Bytes())
}

// hummingbirdOutbound builds outbound forwarding cases originating in this AS
// (first hop), sent out to a child. Analogue of InternalHostToChild. With
// flyover it also covers the demotion cases (expired/stale/future/bandwidth
// exceeded): the router verifies and de-aggregates the aggregate MAC and
// advances by a flyover hop. Best-effort forwards with the plain SCION MAC and
// advances by a regular hop. timestampOffset/duration/bw shape the flyover
// reservation and are ignored when flyover is false.
func hummingbirdOutbound(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	flyover bool,
	timestampOffset time.Duration,
	duration uint16,
	bw uint16,
	payload []byte,
	name string,
) runner.Case {
	// First hop in construction direction: egress to child 141.
	now := time.Now().Add(timestampOffset)
	currentLines := uint8(hummingbird.HopLines)
	advance := hummingbird.HopLines
	current := hummingbird.FlyoverHopField{
		HopField: path.HopField{ConsIngress: 0, ConsEgress: 141},
	}
	if flyover {
		currentLines = hummingbird.FlyoverLines
		advance = hummingbird.FlyoverLines
		current.Flyover = true
		current.ResID = 42
		current.Bw = bw
		current.ResStartTime = 5
		current.Duration = duration
	}
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    0,
				SegLen:    [3]uint8{currentLines + 3, 0, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   1,
			NumLines: int(currentLines) + 3,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			current,
			{HopField: path.HopField{ConsIngress: 411, ConsEgress: 0}},
		},
	}

	scionL := hbirdSCION("1-ff00:0:1", "1-ff00:0:4", "192.168.0.51", "174.16.4.1", dpath)
	if flyover {
		// The aggregate MAC depends on the packet length the router sees.
		scionL.PayloadLen = uint16(8 + len(payload))
		dpath.HopFields[0].HopField.Mac = hbirdAggregateMAC(
			mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[0], dpath.PathMeta)
	} else {
		dpath.HopFields[0].HopField.Mac =
			path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[0].HopField, nil)
	}

	inputLink := hbirdInternalInput(51, 30041)
	input := hbirdSerializeUDP(inputLink, scionL, payload)

	// Expected: forwarded to child 141; path advanced by one hop; SegID updated
	// (construction direction). Flyover de-aggregates the current hop MAC.
	if flyover {
		dpath.HopFields[0].HopField.Mac =
			path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[0].HopField, nil)
	}
	if err := dpath.IncPath(advance); err != nil {
		panic(err)
	}
	dpath.InfoFields[0].UpdateSegID(dpath.HopFields[0].HopField.Mac)
	outputLink := hbirdExternalOutput(141)
	want := hbirdSerializeUDP(outputLink, scionL, payload)
	return hbirdRunnerCase(artifactsDir, name, inputLink.device, outputLink.device, input, want)
}

// hummingbirdChildToChildXover tests a Hummingbird packet that crosses over from
// an up segment to a down segment on the same BR, from a child to another child.
// Analogue of ChildToChildXover; exercises the Hummingbird cross-over handling
// (doHbirdXoverBestEffort / doHbirdXoverFlyover). With flyover the up-segment
// cross-over hop carries a reservation spanning ingress 151 (incoming hop) and
// egress 141 (outgoing hop), which the router verifies and de-aggregates; the AS
// -transit cross-over variants are covered by the
// hummingbirdXoverASTransit{Ingress,Egress} cases.
func hummingbirdChildToChildXover(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	flyover bool,
	name string,
) runner.Case {
	now := time.Now()
	currentLines := uint8(hummingbird.HopLines)
	firstAdvance := hummingbird.HopLines
	current := hummingbird.FlyoverHopField{
		HopField: path.HopField{ConsIngress: 0, ConsEgress: 151},
	}
	if flyover {
		currentLines = hummingbird.FlyoverLines
		firstAdvance = hummingbird.FlyoverLines
		current.Flyover = true
		current.ResID = 42
		current.Bw = 129
		current.ResStartTime = 5
		current.Duration = 301
	}
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    3, // last hop of the up segment (the cross-over hop)
				CurrINF:   0,
				SegLen:    [3]uint8{3 + currentLines, 6, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   2,
			NumLines: 3 + int(currentLines) + 6,
		},
		InfoFields: []path.InfoField{
			// up segment (against construction direction)
			{SegID: 0x111, ConsDir: false, Timestamp: util.TimeToSecs(now)},
			// down segment (construction direction)
			{SegID: 0x222, ConsDir: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 511, ConsEgress: 0}},
			current,
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 141}},
			{HopField: path.HopField{ConsIngress: 411, ConsEgress: 0}},
		},
	}

	scionL := hbirdSCION("1-ff00:0:5", "1-ff00:0:4", "172.16.5.1", "174.16.4.1", dpath)

	// Up-segment hop MAC computed with the base SegID (reused, not recomputed).
	scionMac1 := path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	if flyover {
		scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)
		// Reservation spans ingress 151 (incoming hop) and egress 141 (outgoing hop).
		dpath.HopFields[1].HopField.Mac = hbirdAggregateMACForInterfaces(
			mac, sv, scionL, dpath, 151, 141,
			dpath.InfoFields[0], dpath.HopFields[1], dpath.PathMeta)
	} else {
		dpath.HopFields[1].HopField.Mac = scionMac1
	}
	dpath.InfoFields[0].UpdateSegID(scionMac1)
	dpath.HopFields[2].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[1], dpath.HopFields[2].HopField, nil)

	inputLink := hbirdExternalInput(151)
	input := hbirdSerializeUDP(inputLink, scionL, []byte(hbirdPayload))

	// Expected: forwarded to child 141 after switching to the down segment; both
	// SegIDs updated and the path advanced past the cross-over. Flyover
	// de-aggregates the up-segment hop MAC.
	dpath.HopFields[1].HopField.Mac = scionMac1
	if err := dpath.IncPath(firstAdvance); err != nil {
		panic(err)
	}
	if err := dpath.IncPath(hummingbird.HopLines); err != nil {
		panic(err)
	}
	dpath.InfoFields[0].UpdateSegID(scionMac1)
	dpath.InfoFields[1].UpdateSegID(dpath.HopFields[2].HopField.Mac)
	outputLink := hbirdExternalOutput(141)
	want := hbirdSerializeUDP(outputLink, scionL, []byte(hbirdPayload))
	return hbirdRunnerCase(artifactsDir, name, inputLink.device, outputLink.device, input, want)
}

// hbirdFailureMode selects the validation failure built by the shared SCMP case.
type hbirdFailureMode uint8

const (
	hbirdBadFlyoverMAC hbirdFailureMode = iota
	hbirdBadBestEffortMAC
	hbirdInvalidSourceIA
	hbirdInvalidDestinationIA
	hbirdInvalidSourceIAFlyover
	hbirdInvalidDestinationIAFlyover
)

// hummingbirdSCMPFailureCase builds an inbound validation failure and its
// expected SCMP Parameter Problem response.
func hummingbirdSCMPFailureCase(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	mode hbirdFailureMode,
	name string,
) runner.Case {
	flyover := mode == hbirdBadFlyoverMAC || mode == hbirdInvalidSourceIAFlyover ||
		mode == hbirdInvalidDestinationIAFlyover
	options := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

	ethernet := &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef},
		DstMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x14},
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip := &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      64,
		SrcIP:    net.IP{192, 168, 14, 3},
		DstIP:    net.IP{192, 168, 14, 2},
		Protocol: layers.IPProtocolUDP,
		Flags:    layers.IPv4DontFragment,
	}
	udp := &layers.UDP{SrcPort: 40000, DstPort: 50000}
	_ = udp.SetNetworkLayerForChecksum(ip)

	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    6,
				SegLen:    [3]uint8{3 + 3 + 3, 0, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   1,
			NumLines: 3 + 3 + 3,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 41, ConsEgress: 40}},
			{HopField: path.HopField{ConsIngress: 31, ConsEgress: 30}},
			{HopField: path.HopField{ConsIngress: 141, ConsEgress: 0}},
		},
	}
	if flyover {
		dpath.PathMeta.SegLen[0] += 2
		dpath.NumLines += 2
		dpath.HopFields[2].Flyover = true
		dpath.HopFields[2].ResID = 42
		dpath.HopFields[2].Bw = 129
		dpath.HopFields[2].ResStartTime = 5
		dpath.HopFields[2].Duration = 301
	}

	srcA := addr.MustParseHost("172.16.4.1")
	scionL := &slayers.SCION{
		Version:      0,
		TrafficClass: 0xb8,
		FlowID:       0xdead,
		NextHdr:      slayers.L4UDP,
		PathType:     hummingbird.PathType,
		SrcIA:        addr.MustParseIA("1-ff00:0:4"),
		DstIA:        addr.MustParseIA("1-ff00:0:1"),
		Path:         dpath,
	}
	if mode == hbirdInvalidSourceIA || mode == hbirdInvalidSourceIAFlyover {
		scionL.SrcIA = addr.MustParseIA("1-ff00:0:1")
	}
	if mode == hbirdInvalidDestinationIA || mode == hbirdInvalidDestinationIAFlyover {
		scionL.DstIA = addr.MustParseIA("1-ff00:0:9")
	}
	if err := scionL.SetSrcAddr(srcA); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("192.168.0.51")); err != nil {
		panic(err)
	}
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	if flyover {
		dpath.HopFields[2].HopField.Mac = hbirdAggregateMAC(
			mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[2], dpath.PathMeta)
	} else {
		dpath.HopFields[2].HopField.Mac =
			path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[2].HopField, nil)
	}
	if mode == hbirdBadFlyoverMAC || mode == hbirdBadBestEffortMAC {
		// Corrupt only the MAC so the failure is unambiguously authentication.
		dpath.HopFields[2].HopField.Mac[0] ^= 0xff
	}

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	// Pointer to the current Hummingbird hop line in the offending packet.
	pointer := slayers.CmnHdrLen + scionL.AddrHdrLen() +
		(hummingbird.MetaLen + path.InfoLen*dpath.NumINF +
			int(dpath.PathMeta.CurrHF)*hummingbird.LineLen)
	code := slayers.SCMPCodeInvalidHopFieldMAC
	if mode == hbirdInvalidSourceIA || mode == hbirdInvalidSourceIAFlyover {
		code = slayers.SCMPCodeInvalidSourceAddress
		pointer = slayers.CmnHdrLen + addr.IABytes
	}
	if mode == hbirdInvalidDestinationIA || mode == hbirdInvalidDestinationIAFlyover {
		code = slayers.SCMPCodeInvalidDestinationAddress
		pointer = slayers.CmnHdrLen
	}

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: SCMP ParameterProblem/InvalidHopFieldMAC returned to the source
	// over the reversed (flyover-stripped) Hummingbird path, out the ingress
	// link (child 141). See prepareHbirdSCMP in router/dataplane_hbird.go.
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x14}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 14, 2}
	ip.DstIP = net.IP{192, 168, 14, 3}
	udp.SrcPort, udp.DstPort = udp.DstPort, udp.SrcPort

	scionL.DstIA = scionL.SrcIA
	scionL.SrcIA = addr.MustParseIA("1-ff00:0:1")
	if err := scionL.SetDstAddr(srcA); err != nil {
		panic(err)
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost("192.168.0.11")); err != nil {
		panic(err)
	}

	// Build the reply path exactly as prepareHbirdSCMP does: decode, reverse
	// (drops flyovers), then (external egress link, construction direction)
	// update SegID and increment by one regular hop.
	revTmp, err := dpath.Reverse()
	if err != nil {
		panic(err)
	}
	revPath := revTmp.(*hummingbird.Decoded)
	infoField := &revPath.InfoFields[revPath.PathMeta.CurrINF]
	if infoField.ConsDir {
		hf, err := revPath.GetCurrentHopField()
		if err != nil {
			panic(err)
		}
		infoField.UpdateSegID(hf.HopField.Mac)
	}
	if err := revPath.IncPath(hummingbird.HopLines); err != nil {
		panic(err)
	}
	scionL.Path = revPath
	scionL.PathType = revPath.Type()

	scionL.NextHdr = slayers.End2EndClass
	e2e := normalizedSCMPPacketAuthEndToEndExtn()
	e2e.NextHdr = slayers.L4SCMP
	scmpH := &slayers.SCMP{
		TypeCode: slayers.CreateSCMPTypeCode(slayers.SCMPTypeParameterProblem,
			code),
	}
	scmpH.SetNetworkLayerForChecksum(scionL)
	scmpP := &slayers.SCMPParameterProblem{
		Pointer: uint16(pointer),
	}

	// Skip Ethernet + IPv4 + UDP to obtain the quoted SCION packet.
	quoteStart := 14 + 20 + 8
	quote := input.Bytes()[quoteStart:]
	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, e2e, scmpH, scmpP, gopacket.Payload(quote),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:            name,
		WriteTo:         "veth_141_host",
		ReadFrom:        "veth_141_host",
		Input:           input.Bytes(),
		Want:            want.Bytes(),
		StoreDir:        filepath.Join(artifactsDir, name),
		NormalizePacket: scmpNormalizePacket,
	}
}

// hummingbirdOutboundSCMPFailureCase builds a locally originated (first-hop) validation
// failure and its expected SCMP Parameter Problem response. Unlike
// hummingbirdSCMPFailureCase (inbound), the invalid IA is caught before an egress
// interface is ever chosen, so the reply is sent back internally rather than out an
// external link. Only the two invalid-IA modes (best-effort and flyover) apply here;
// invalid-MAC modes are not, since IA validation happens before MAC verification and thus
// would mask a MAC-only failure.
func hummingbirdOutboundSCMPFailureCase(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	mode hbirdFailureMode,
	name string,
) runner.Case {
	flyover := mode == hbirdInvalidSourceIAFlyover || mode == hbirdInvalidDestinationIAFlyover
	options := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

	// Injected as if from the internal host 192.168.0.51, leaving via child 141.
	ethernet := &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef},
		DstMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x1},
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip := &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      64,
		SrcIP:    net.IP{192, 168, 0, 51},
		DstIP:    net.IP{192, 168, 0, 11},
		Protocol: layers.IPProtocolUDP,
		Flags:    layers.IPv4DontFragment,
	}
	udp := &layers.UDP{SrcPort: 30041, DstPort: 30001}
	_ = udp.SetNetworkLayerForChecksum(ip)

	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    0,
				SegLen:    [3]uint8{3 + 3, 0, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   1,
			NumLines: 6,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 141}},
			{HopField: path.HopField{ConsIngress: 411, ConsEgress: 0}},
		},
	}
	if flyover {
		dpath.PathMeta.SegLen[0] += 2
		dpath.NumLines += 2
		dpath.HopFields[0].Flyover = true
		dpath.HopFields[0].ResID = 42
		dpath.HopFields[0].Bw = 129
		dpath.HopFields[0].ResStartTime = 5
		dpath.HopFields[0].Duration = 301
	}

	srcA := addr.MustParseHost("192.168.0.51")
	scionL := &slayers.SCION{
		Version:      0,
		TrafficClass: 0xb8,
		FlowID:       0xdead,
		NextHdr:      slayers.L4UDP,
		PathType:     hummingbird.PathType,
		SrcIA:        addr.MustParseIA("1-ff00:0:1"),
		DstIA:        addr.MustParseIA("1-ff00:0:4"),
		Path:         dpath,
	}
	if mode == hbirdInvalidSourceIA || mode == hbirdInvalidSourceIAFlyover {
		// Not local: fails "IsFirstHop && !srcIsLocal".
		scionL.SrcIA = addr.MustParseIA("1-ff00:0:2")
	}
	if mode == hbirdInvalidDestinationIA || mode == hbirdInvalidDestinationIAFlyover {
		// Local: fails "dstIsLocal" on an outbound (locally originated) packet.
		scionL.DstIA = addr.MustParseIA("1-ff00:0:1")
	}
	if err := scionL.SetSrcAddr(srcA); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("174.16.4.1")); err != nil {
		panic(err)
	}
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	if flyover {
		dpath.HopFields[0].HopField.Mac = hbirdAggregateMAC(
			mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[0], dpath.PathMeta)
	} else {
		dpath.HopFields[0].HopField.Mac =
			path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[0].HopField, nil)
	}

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	code := slayers.SCMPCodeInvalidSourceAddress
	pointer := slayers.CmnHdrLen + addr.IABytes
	if mode == hbirdInvalidDestinationIA || mode == hbirdInvalidDestinationIAFlyover {
		code = slayers.SCMPCodeInvalidDestinationAddress
		pointer = slayers.CmnHdrLen
	}

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: SCMP ParameterProblem returned internally to the originating host,
	// since the invalid IA is caught before an egress interface is ever chosen. See
	// prepareHbirdSCMP in router/dataplane_hbird.go: replying on an internal link
	// skips the "external egress" SegID update/path increment.
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC, ethernet.DstMAC = ethernet.DstMAC, ethernet.SrcMAC
	ip.SrcIP, ip.DstIP = ip.DstIP, ip.SrcIP
	udp.SrcPort, udp.DstPort = udp.DstPort, udp.SrcPort

	scionL.DstIA = scionL.SrcIA
	scionL.SrcIA = addr.MustParseIA("1-ff00:0:1")
	if err := scionL.SetDstAddr(srcA); err != nil {
		panic(err)
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost("192.168.0.11")); err != nil {
		panic(err)
	}

	revTmp, err := dpath.Reverse()
	if err != nil {
		panic(err)
	}
	scionL.Path = revTmp
	scionL.PathType = revTmp.Type()

	scionL.NextHdr = slayers.End2EndClass
	e2e := normalizedSCMPPacketAuthEndToEndExtn()
	e2e.NextHdr = slayers.L4SCMP
	scmpH := &slayers.SCMP{
		TypeCode: slayers.CreateSCMPTypeCode(slayers.SCMPTypeParameterProblem, code),
	}
	scmpH.SetNetworkLayerForChecksum(scionL)
	scmpP := &slayers.SCMPParameterProblem{
		Pointer: uint16(pointer),
	}

	// Skip Ethernet + IPv4 + UDP to obtain the quoted SCION packet.
	quoteStart := 14 + 20 + 8
	quote := input.Bytes()[quoteStart:]
	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, e2e, scmpH, scmpP, gopacket.Payload(quote),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:            name,
		WriteTo:         "veth_int_host",
		ReadFrom:        "veth_int_host",
		Input:           input.Bytes(),
		Want:            want.Bytes(),
		StoreDir:        filepath.Join(artifactsDir, name),
		NormalizePacket: scmpNormalizePacket,
	}
}

// hummingbirdDirectASTransit builds either half of direct split-BR AS transit.
// The ingress BR forwards the authenticated current hop internally without
// advancing it; the egress BR de-aggregates flyovers and advances on egress.
func hummingbirdDirectASTransit(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	flyover bool,
	egressBR bool,
	name string,
) runner.Case {
	now := time.Now()
	hopLines := uint8(hummingbird.HopLines)
	advance := hummingbird.HopLines

	// The router process under test is brA, which owns interface 141 (child) but
	// not 191 (parent, owned by sibling brD). A direct AS transit spans two BRs,
	// so which one is under test determines the direction that keeps brA on the
	// interface it actually owns:
	//   ingress BR: the packet enters externally on brA's 141 (child) and, since
	//     the egress 191 is on the sibling brD, brA forwards it internally toward
	//     brD. Direction is child(AS4) -> parent(AS9).
	//   egress BR: the packet enters internally from the sibling ingress BR (brD)
	//     and leaves externally on brA's 141 (child), so brA must own the egress.
	//     Direction is therefore parent(AS9) -> child(AS4): the transit hop is
	//     191 (ingress, from brD) -> 141 (egress, brA), and the surrounding hops
	//     and endpoint IAs are mirrored accordingly.
	consIn, consEg := uint16(141), uint16(191)
	firstHop := path.HopField{ConsIngress: 0, ConsEgress: 411}
	lastHop := path.HopField{ConsIngress: 911, ConsEgress: 0}
	srcIA, dstIA := "1-ff00:0:4", "1-ff00:0:9"
	srcHost, dstHost := "172.16.4.1", "174.16.9.1"
	if egressBR {
		consIn, consEg = 191, 141
		firstHop = path.HopField{ConsIngress: 0, ConsEgress: 911}
		lastHop = path.HopField{ConsIngress: 411, ConsEgress: 0}
		srcIA, dstIA = "1-ff00:0:9", "1-ff00:0:4"
		srcHost, dstHost = "172.16.9.1", "174.16.4.1"
	}

	current := hummingbird.FlyoverHopField{
		HopField: path.HopField{ConsIngress: consIn, ConsEgress: consEg},
	}
	if flyover {
		hopLines = hummingbird.FlyoverLines
		advance = hummingbird.FlyoverLines
		current.Flyover = true
		current.ResID = 42
		current.Bw = 129
		current.ResStartTime = 5
		current.Duration = 301
	}
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF: 3, SegLen: [3]uint8{3 + hopLines + 3, 0, 0},
				BaseTS: util.TimeToSecs(now), HighResTS: 500 << 22,
			},
			NumINF: 1, NumLines: 3 + int(hopLines) + 3,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: firstHop},
			current,
			{HopField: lastHop},
		},
	}
	scionL := hbirdSCION(srcIA, dstIA, srcHost, dstHost, dpath)
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)
	if flyover {
		dpath.HopFields[1].HopField.Mac = hbirdAggregateMACForInterfaces(
			mac, sv, scionL, dpath, consIn, consEg, dpath.InfoFields[0], dpath.HopFields[1],
			dpath.PathMeta)
	} else {
		dpath.HopFields[1].HopField.Mac =
			path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	}

	inputLink, outputLink := hbirdExternalInput(141), hbirdInternalOutput(14, 30004)
	if egressBR {
		inputLink, outputLink = hbirdInternalInput(14, 30004), hbirdExternalOutput(141)
	}
	input := hbirdSerializeUDP(inputLink, scionL, []byte(hbirdPayload))

	if egressBR {
		if flyover {
			dpath.HopFields[1].HopField.Mac =
				path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
		}
		dpath.InfoFields[0].UpdateSegID(dpath.HopFields[1].HopField.Mac)
		if err := dpath.IncPath(advance); err != nil {
			panic(err)
		}
	}
	want := hbirdSerializeUDP(outputLink, scionL, []byte(hbirdPayload))
	return hbirdRunnerCase(artifactsDir, name, inputLink.device, outputLink.device, input, want)
}

// hummingbirdXoverASTransitIngress tests the ingress BR of an AS-transit
// cross-over: the packet arrives on an external child link (151), switches
// segments, and its egress interface belongs to a sibling BR, so it is forwarded
// internally to brC (192.168.0.13). With flyover the reservation sits on the
// incoming (up-seg) hop and the router moves it to the outgoing hop for the
// egress BR (xoverMoveFlyoverToNext), shifting the SegLens by 2 lines and
// re-aggregating the MAC; best-effort forwards unchanged apart from the advance.
// Mirrors unit tests "astransit_xover_ingress_{flyover,best-effort}".
func hummingbirdXoverASTransitIngress(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	flyover bool,
	name string,
) runner.Case {
	now := time.Now()
	currentLines := uint8(hummingbird.HopLines)
	current := hummingbird.FlyoverHopField{
		HopField: path.HopField{ConsIngress: 0, ConsEgress: 151},
	}
	if flyover {
		currentLines = hummingbird.FlyoverLines
		current.Flyover = true
		current.ResID = 42
		current.Bw = 129
		current.ResStartTime = 5
		current.Duration = 301
	}
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrINF:   0,
				CurrHF:    3, // up-seg cross-over hop
				SegLen:    [3]uint8{3 + currentLines, 6, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   2,
			NumLines: 3 + int(currentLines) + 6,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: false, Timestamp: util.TimeToSecs(now)}, // up seg
			{SegID: 0x222, ConsDir: true, Timestamp: util.TimeToSecs(now)},  // down seg
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 511, ConsEgress: 0}},
			current,
			// xover here (up->down shortcut); egress 181 is a child on sibling brC.
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 181}},
			{HopField: path.HopField{ConsIngress: 811, ConsEgress: 0}},
		},
	}

	scionL := hbirdSCION("1-ff00:0:5", "1-ff00:0:8", "172.16.5.1", "172.16.8.1", dpath)
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	// Reservation spans ingress 151 (incoming hop) and egress 181 (outgoing hop).
	scionMac1 := path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	if flyover {
		dpath.HopFields[1].HopField.Mac = hbirdAggregateMACForInterfaces(
			mac, sv, scionL, dpath, 151, 181,
			dpath.InfoFields[0], dpath.HopFields[1], dpath.PathMeta)
	} else {
		dpath.HopFields[1].HopField.Mac = scionMac1
	}
	dpath.InfoFields[0].UpdateSegID(scionMac1)
	dpath.HopFields[2].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[1], dpath.HopFields[2].HopField, nil)

	inputLink := hbirdExternalInput(151)
	input := hbirdSerializeUDP(inputLink, scionL, []byte(hbirdPayload))

	// Expected: forwarded internally to sibling brC; the path advances by one
	// regular hop. Flyover de-aggregates the up-seg hop and moves the reservation
	// to the down-seg hop, re-aggregating its MAC and shifting the SegLens. For
	// best-effort the router's own non-consdir-ingress SegID update is a second,
	// self-canceling XOR with the same MAC.
	if flyover {
		dpath.HopFields[1].Flyover = false
		dpath.HopFields[1].HopField.Mac = scionMac1
		dpath.HopFields[2].Flyover = true
		dpath.HopFields[2].ResID = 42
		dpath.HopFields[2].Bw = 129
		dpath.HopFields[2].ResStartTime = 5
		dpath.HopFields[2].Duration = 301
		dpath.PathMeta.SegLen[0] -= 2
		dpath.PathMeta.SegLen[1] += 2
		dpath.HopFields[2].HopField.Mac = hbirdAggregateMACForInterfaces(
			mac, sv, scionL, dpath, 151, 181,
			dpath.InfoFields[1], dpath.HopFields[2], dpath.PathMeta)
	}
	dpath.InfoFields[0].UpdateSegID(scionMac1)
	if err := dpath.IncPath(hummingbird.HopLines); err != nil {
		panic(err)
	}
	outputLink := hbirdInternalOutput(13, 30003)
	want := hbirdSerializeUDP(outputLink, scionL, []byte(hbirdPayload))
	return hbirdRunnerCase(artifactsDir, name, inputLink.device, outputLink.device, input, want)
}

// hummingbirdXoverASTransitEgress tests the egress BR of an AS-transit
// cross-over: the packet arrives internally from the sibling BR (brC) that
// handled the up segment, and this BR egresses it externally on child 141. With
// flyover the reservation sits on the outgoing (down-seg) hop; the router
// de-aggregates it and moves it back to the incoming (up-seg) hop
// (xoverMoveFlyoverToPrevious), shifting the SegLens by 2 lines; best-effort just
// advances past the current hop. Mirrors unit tests
// "astransit_xover_egress_{flyover,best-effort}".
func hummingbirdXoverASTransitEgress(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	flyover bool,
	name string,
) runner.Case {
	now := time.Now()
	currentLines := uint8(hummingbird.HopLines)
	advance := hummingbird.HopLines
	current := hummingbird.FlyoverHopField{
		HopField: path.HopField{ConsIngress: 0, ConsEgress: 141},
	}
	if flyover {
		currentLines = hummingbird.FlyoverLines
		advance = hummingbird.FlyoverLines
		current.Flyover = true
		current.ResID = 42
		current.Bw = 129
		current.ResStartTime = 5
		current.Duration = 301
	}
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrINF:   1,
				CurrHF:    6, // down-seg cross-over hop
				SegLen:    [3]uint8{6, 3 + currentLines, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   2,
			NumLines: 6 + 3 + int(currentLines),
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: false, Timestamp: util.TimeToSecs(now)}, // up seg
			{SegID: 0x222, ConsDir: true, Timestamp: util.TimeToSecs(now)},  // down seg
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 511, ConsEgress: 0}},
			// up-seg hop; its real ingress is 181 (on sibling brC).
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 181}},
			// xover; down-seg hop, egress on this BR's child 141 (current).
			current,
			{HopField: path.HopField{ConsIngress: 411, ConsEgress: 0}},
		},
	}

	scionL := hbirdSCION("1-ff00:0:5", "1-ff00:0:4", "172.16.5.1", "174.16.4.1", dpath)
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	// The up-seg hop keeps its plain SCION MAC (never verified or changed here).
	dpath.HopFields[1].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	// The down-seg reservation spans ingress 181 (previous hop) and egress 141.
	scionMac2 := path.MAC(mac, dpath.InfoFields[1], dpath.HopFields[2].HopField, nil)
	if flyover {
		dpath.HopFields[2].HopField.Mac = hbirdAggregateMACForInterfaces(
			mac, sv, scionL, dpath, 181, 141,
			dpath.InfoFields[1], dpath.HopFields[2], dpath.PathMeta)
	} else {
		dpath.HopFields[2].HopField.Mac = scionMac2
	}

	inputLink := hbirdInternalInput(13, 30003)
	input := hbirdSerializeUDP(inputLink, scionL, []byte(hbirdPayload))

	// Expected: egressed on child 141; the down-seg SegID is updated (construction
	// direction); the path advances past the current hop. Flyover de-aggregates the
	// down-seg hop and moves the reservation back to the up-seg hop, shifting the
	// SegLens by 2 lines.
	if flyover {
		dpath.HopFields[2].Flyover = false
		dpath.HopFields[2].HopField.Mac = scionMac2
		dpath.HopFields[1].Flyover = true
		dpath.HopFields[1].ResID = 42
		dpath.HopFields[1].Bw = 129
		dpath.HopFields[1].ResStartTime = 5
		dpath.HopFields[1].Duration = 301
		dpath.PathMeta.SegLen[0] += 2
		dpath.PathMeta.SegLen[1] -= 2
	}
	dpath.InfoFields[1].UpdateSegID(scionMac2)
	if err := dpath.IncPath(advance); err != nil {
		panic(err)
	}
	outputLink := hbirdExternalOutput(141)
	want := hbirdSerializeUDP(outputLink, scionL, []byte(hbirdPayload))
	return hbirdRunnerCase(artifactsDir, name, inputLink.device, outputLink.device, input, want)
}

// hummingbirdPeeringCase builds peering-boundary cases and the ordinary hops
// immediately before or after that boundary in either packet mode.
func hummingbirdPeeringCase(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	flyover bool,
	consDir bool,
	adjacent bool,
	name string,
) runner.Case {
	now := time.Now()
	currentLines := uint8(hummingbird.HopLines)
	advance := hummingbird.HopLines
	if flyover {
		currentLines = hummingbird.FlyoverLines
		advance = hummingbird.FlyoverLines
	}
	current := hummingbird.FlyoverHopField{
		HopField: path.HopField{ConsIngress: 121, ConsEgress: 151},
		Flyover:  flyover, ResID: 42, Bw: 129, ResStartTime: 5, Duration: 301,
	}
	info0 := path.InfoField{
		SegID: 0x111, ConsDir: false, Peer: true, Timestamp: util.TimeToSecs(now),
	}
	info1 := path.InfoField{
		SegID: 0x222, ConsDir: true, Peer: true, Timestamp: util.TimeToSecs(now),
	}
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF: 3, BaseTS: util.TimeToSecs(now), HighResTS: 500 << 22,
			},
			NumINF: 2,
		},
		InfoFields: []path.InfoField{info0, info1},
	}
	if adjacent {
		if consDir {
			dpath.PathMeta.CurrINF = 1
			dpath.PathMeta.CurrHF = 6
			dpath.PathMeta.SegLen = [3]uint8{3, 3 + currentLines + 3, 0}
			dpath.HopFields = []hummingbird.FlyoverHopField{
				{HopField: path.HopField{ConsIngress: 211, ConsEgress: 0}},
				{HopField: path.HopField{ConsIngress: 121, ConsEgress: 0}},
				current,
				{HopField: path.HopField{ConsIngress: 511, ConsEgress: 0}},
			}
		} else {
			dpath.PathMeta.CurrINF = 0
			dpath.PathMeta.SegLen = [3]uint8{3 + currentLines + 3, 3, 0}
			dpath.HopFields = []hummingbird.FlyoverHopField{
				{HopField: path.HopField{ConsIngress: 511, ConsEgress: 0}},
				current,
				{HopField: path.HopField{ConsIngress: 121, ConsEgress: 0}},
				{HopField: path.HopField{ConsIngress: 211, ConsEgress: 0}},
			}
		}
	} else {
		dpath.PathMeta.CurrINF = 0
		dpath.PathMeta.SegLen = [3]uint8{3 + currentLines, 3, 0}
		dpath.HopFields = []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 511, ConsEgress: 0}}, current,
			{HopField: path.HopField{ConsIngress: 211, ConsEgress: 0}},
		}
		if consDir {
			dpath.PathMeta.CurrINF = 1
			dpath.PathMeta.SegLen = [3]uint8{3, currentLines + 3, 0}
		}
	}
	dpath.NumLines = int(dpath.PathMeta.SegLen[0] + dpath.PathMeta.SegLen[1])
	srcIA, dstIA, srcHost, dstHost :=
		"1-ff00:0:5", "1-ff00:0:2", "172.16.5.1", "174.16.2.1"
	inputLink, outputLink := hbirdExternalInput(151), hbirdExternalOutput(121)
	if consDir {
		srcIA, dstIA, srcHost, dstHost =
			"1-ff00:0:2", "1-ff00:0:5", "172.16.2.1", "174.16.5.1"
		inputLink, outputLink = hbirdExternalInput(121), hbirdExternalOutput(151)
	}
	scionL := hbirdSCION(srcIA, dstIA, srcHost, dstHost, dpath)
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)
	currentIndex := 1
	infoIndex := 0
	if consDir {
		infoIndex = 1
		if adjacent {
			currentIndex = 2
		}
	}
	plainMAC := path.MAC(mac, dpath.InfoFields[infoIndex],
		dpath.HopFields[currentIndex].HopField, nil)
	if flyover {
		dpath.HopFields[currentIndex].HopField.Mac = hbirdAggregateMAC(
			mac, sv, scionL, dpath, dpath.InfoFields[infoIndex],
			dpath.HopFields[currentIndex], dpath.PathMeta)
	} else {
		dpath.HopFields[currentIndex].HopField.Mac = plainMAC
	}
	if !consDir && adjacent {
		dpath.InfoFields[0].UpdateSegID(plainMAC)
	}
	input := hbirdSerializeUDP(inputLink, scionL, []byte(hbirdPayload))
	if flyover {
		dpath.HopFields[currentIndex].HopField.Mac = plainMAC
	}
	if adjacent {
		dpath.InfoFields[infoIndex].UpdateSegID(plainMAC)
	}
	if err := dpath.IncPath(advance); err != nil {
		panic(err)
	}
	want := hbirdSerializeUDP(outputLink, scionL, []byte(hbirdPayload))
	return hbirdRunnerCase(artifactsDir, name, inputLink.device, outputLink.device, input, want)
}

// hummingbirdRouterAlertCase builds a BR-transit Hummingbird packet carrying a genuine SCMP
// traceroute request, with exactly one router-alert flag set on the current (parent->child,
// construction-direction) hop. The router must divert it to the slow path and reply with an
// SCMP traceroute reply reporting the alerted interface (the ingress interface 131 for an
// ingress alert, the would-be egress interface 141 for an egress alert), sent back out the
// same external link the request arrived on. Mirrors the plain SCION cases
// SCMPTracerouteIngressConsDir/SCMPTracerouteEgressConsDir, adapted to the Hummingbird path and,
// when flyover is true, an aggregate MAC on the current hop.
func hummingbirdRouterAlertCase(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	flyover bool,
	ingressAlert bool,
	name string,
) runner.Case {
	options := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

	// Arrives on parent 131, construction direction.
	ethernet := &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef},
		DstMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x13},
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip := &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      64,
		SrcIP:    net.IP{192, 168, 13, 3},
		DstIP:    net.IP{192, 168, 13, 2},
		Protocol: layers.IPProtocolUDP,
		Flags:    layers.IPv4DontFragment,
	}
	udp := &layers.UDP{SrcPort: 40000, DstPort: 50000}
	_ = udp.SetNetworkLayerForChecksum(ip)

	now := time.Now()
	hopLines := uint8(hummingbird.HopLines)
	if flyover {
		hopLines = hummingbird.FlyoverLines
	}
	current := hummingbird.FlyoverHopField{
		HopField: path.HopField{
			ConsIngress:        131,
			ConsEgress:         141,
			IngressRouterAlert: ingressAlert,
			EgressRouterAlert:  !ingressAlert,
		},
	}
	if flyover {
		current.Flyover = true
		current.ResID = 42
		current.Bw = 129
		current.ResStartTime = 5
		current.Duration = 301
	}
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF: 3, SegLen: [3]uint8{3 + hopLines + 3, 0, 0},
				BaseTS: util.TimeToSecs(now), HighResTS: 500 << 22,
			},
			NumINF: 1, NumLines: 3 + int(hopLines) + 3,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 311}},
			current,
			{HopField: path.HopField{ConsIngress: 411, ConsEgress: 0}},
		},
	}

	srcA := addr.MustParseHost("172.16.3.1")
	scionL := &slayers.SCION{
		Version:      0,
		TrafficClass: 0xb8,
		FlowID:       0xdead,
		NextHdr:      slayers.L4SCMP,
		PathType:     hummingbird.PathType,
		SrcIA:        addr.MustParseIA("1-ff00:0:3"),
		DstIA:        addr.MustParseIA("1-ff00:0:4"),
		Path:         dpath,
	}
	if err := scionL.SetSrcAddr(srcA); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("174.16.4.1")); err != nil {
		panic(err)
	}
	if flyover {
		// The aggregate MAC depends on the packet length seen by the router, so the
		// exact length of the SCMP traceroute request must be known up front.
		scionL.PayloadLen = uint16(slayers.ScmpHeaderSize(slayers.SCMPTypeTracerouteRequest))
		dpath.HopFields[1].HopField.Mac = hbirdAggregateMAC(
			mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[1], dpath.PathMeta)
	} else {
		dpath.HopFields[1].HopField.Mac =
			path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	}

	scmpH := &slayers.SCMP{
		TypeCode: slayers.CreateSCMPTypeCode(slayers.SCMPTypeTracerouteRequest, 0),
	}
	scmpH.SetNetworkLayerForChecksum(scionL)
	scmpP := &slayers.SCMPTraceroute{
		Identifier: 567,
		Sequence:   129,
	}

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scmpH, scmpP,
	); err != nil {
		panic(err)
	}

	// Expected: an SCMP traceroute reply sent back out the same link (131), reporting
	// the alerted interface. The alert flag is cleared before the path is reversed,
	// mirroring handleHbirdIngressRouterAlert/handleHbirdEgressRouterAlert clearing it
	// in place before diverting to the slow path.
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x13}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 13, 2}
	ip.DstIP = net.IP{192, 168, 13, 3}
	udp.SrcPort, udp.DstPort = udp.DstPort, udp.SrcPort

	scionL.DstIA = scionL.SrcIA
	scionL.SrcIA = addr.MustParseIA("1-ff00:0:1")
	if err := scionL.SetDstAddr(srcA); err != nil {
		panic(err)
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost("192.168.0.11")); err != nil {
		panic(err)
	}

	dpath.HopFields[1].HopField.IngressRouterAlert = false
	dpath.HopFields[1].HopField.EgressRouterAlert = false
	revTmp, err := dpath.Reverse()
	if err != nil {
		panic(err)
	}
	revPath := revTmp.(*hummingbird.Decoded)
	infoField := &revPath.InfoFields[revPath.PathMeta.CurrINF]
	if infoField.ConsDir {
		hf, err := revPath.GetCurrentHopField()
		if err != nil {
			panic(err)
		}
		infoField.UpdateSegID(hf.HopField.Mac)
	}
	if err := revPath.IncPath(hummingbird.HopLines); err != nil {
		panic(err)
	}
	scionL.Path = revPath
	scionL.PathType = revPath.Type()

	alertedInterface := uint64(131)
	if !ingressAlert {
		alertedInterface = 141
	}
	scionL.NextHdr = slayers.L4SCMP
	scmpH = &slayers.SCMP{
		TypeCode: slayers.CreateSCMPTypeCode(slayers.SCMPTypeTracerouteReply, 0),
	}
	scmpH.SetNetworkLayerForChecksum(scionL)
	scmpP = &slayers.SCMPTraceroute{
		Identifier: scmpP.Identifier,
		Sequence:   scmpP.Sequence,
		IA:         scionL.SrcIA,
		Interface:  alertedInterface,
	}

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scmpH, scmpP,
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     name,
		WriteTo:  "veth_131_host",
		ReadFrom: "veth_131_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, name),
	}
}

// hbirdUnderlay contains the layers and veth used for one underlay direction.
type hbirdUnderlay struct {
	device   string
	ethernet *layers.Ethernet
	ip       *layers.IPv4
	udp      *layers.UDP
}

// hbirdSCION creates the SCION header shared by Hummingbird UDP cases.
func hbirdSCION(srcIA, dstIA, srcHost, dstHost string, dpath *hummingbird.Decoded) *slayers.SCION {
	scionL := &slayers.SCION{
		Version: 0, TrafficClass: 0xb8, FlowID: 0xdead, NextHdr: slayers.L4UDP,
		PathType: hummingbird.PathType, SrcIA: addr.MustParseIA(srcIA),
		DstIA: addr.MustParseIA(dstIA), Path: dpath,
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost(srcHost)); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost(dstHost)); err != nil {
		panic(err)
	}
	return scionL
}

// hbirdAggregateMAC computes the aggregate MAC stored in a flyover hop field:
// the SCION hop MAC XORed with the flyover MAC. See router/dataplane_hbird.go
// (verifyHbirdFlyoverMac) and the computeAggregateMac test helper.
func hbirdAggregateMAC(
	mac hash.Hash,
	sv []byte,
	spkt *slayers.SCION,
	dpath *hummingbird.Decoded,
	info path.InfoField,
	hf hummingbird.FlyoverHopField,
	meta hummingbird.MetaHdr,
) [path.MacLen]byte {
	// Reservations are made in construction direction; against it, ingress and
	// egress are swapped relative to the hop field.
	ingress, egress := hf.HopField.ConsIngress, hf.HopField.ConsEgress
	if !info.ConsDir {
		ingress, egress = egress, ingress
	}
	return hbirdAggregateMACForInterfaces(mac, sv, spkt, dpath, ingress, egress, info, hf, meta)
}

// hbirdExternalInput returns the underlay used to inject a packet from an
// external host into the router interface identified by id.
func hbirdExternalInput(id byte) hbirdUnderlay {
	return hbirdUnderlayLayers(
		"veth_"+string([]byte{'0' + id/100, '0' + id/10%10, '0' + id%10})+"_host",
		net.IP{192, 168, id / 10, 3}, net.IP{192, 168, id / 10, 2},
		(id/100)<<4|id/10%10,
		40000, 50000, true)
}

// hbirdSerializeUDP serializes one complete Hummingbird/SCION UDP packet.
func hbirdSerializeUDP(underlay hbirdUnderlay, scionL *slayers.SCION, payload []byte) []byte {
	scionUDP := &slayers.UDP{SrcPort: 40111, DstPort: 40222}
	scionUDP.SetNetworkLayerForChecksum(scionL)
	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, gopacket.SerializeOptions{
		FixLengths: true, ComputeChecksums: true,
	}, underlay.ethernet, underlay.ip, underlay.udp, scionL, scionUDP,
		gopacket.Payload(payload)); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

// hbirdRunnerCase assembles a runner case from serialized input and expected packets.
func hbirdRunnerCase(
	artifactsDir, name, writeTo, readFrom string,
	input, want []byte,
) runner.Case {
	return runner.Case{
		Name: name, WriteTo: writeTo, ReadFrom: readFrom, Input: input, Want: want,
		StoreDir: filepath.Join(artifactsDir, name),
	}
}

// hbirdAggregateMACForInterfaces is like hbirdAggregateMAC but takes the reservation
// ingress/egress in packet traversal direction. At a cross-over the reservation spans the ingress of
// the incoming hop and the egress of the outgoing hop, so those interfaces are
// not simply the hop field's ConsIngress/ConsEgress (see getFlyoverInterfaces in
// router/dataplane_hbird.go and the computeAggregateMacExplicitInEg test helper).
func hbirdAggregateMACForInterfaces(
	mac hash.Hash,
	sv []byte,
	spkt *slayers.SCION,
	dpath *hummingbird.Decoded,
	ingress uint16,
	egress uint16,
	info path.InfoField,
	hf hummingbird.FlyoverHopField,
	meta hummingbird.MetaHdr,
) [path.MacLen]byte {
	scionMac := path.MAC(mac, info, hf.HopField, nil)

	block, err := aes.NewCipher(sv)
	if err != nil {
		panic(err)
	}

	akBuffer := make([]byte, hummingbird.AkBufferSize)
	macBuffer := make([]byte, hummingbird.FlyoverMacBufferSize)
	xkBuffer := make([]uint32, hummingbird.XkBufferSize)

	ak := hummingbird.DeriveAuthKey(block, hf.ResID, hf.Bw, ingress, egress,
		meta.BaseTS-uint32(hf.ResStartTime), hf.Duration, akBuffer)
	flyoverMac := hummingbird.FullFlyoverMac(ak, spkt.DstIA,
		hbirdPacketLen(spkt, dpath), hf.ResStartTime, meta.HighResTS, macBuffer, xkBuffer)

	for i := range scionMac {
		scionMac[i] ^= flyoverMac[i]
	}
	return scionMac
}

// hbirdInternalOutput returns the underlay packet sent from the router under
// test to a sibling router.
func hbirdInternalOutput(remoteSuffix byte, remotePort layers.UDPPort) hbirdUnderlay {
	return hbirdUnderlayLayers("veth_int_host",
		net.IP{192, 168, 0, 11}, net.IP{192, 168, 0, remoteSuffix}, 1,
		30001, remotePort, false)
}

// hbirdInternalInput returns an underlay packet sent by sibling router host to
// the router under test. remoteSuffix and remotePort identify that sibling.
func hbirdInternalInput(remoteSuffix byte, remotePort layers.UDPPort) hbirdUnderlay {
	return hbirdUnderlayLayers("veth_int_host",
		net.IP{192, 168, 0, remoteSuffix}, net.IP{192, 168, 0, 11}, 1,
		remotePort, 30001, true)
}

// hbirdExternalOutput returns the underlay emitted by the router on external
// interface id.
func hbirdExternalOutput(id byte) hbirdUnderlay {
	return hbirdUnderlayLayers(
		"veth_"+string([]byte{'0' + id/100, '0' + id/10%10, '0' + id%10})+"_host",
		net.IP{192, 168, id / 10, 2}, net.IP{192, 168, id / 10, 3},
		(id/100)<<4|id/10%10,
		50000, 40000, false)
}

// hbirdUnderlayLayers creates the Ethernet, IPv4, and UDP envelope shared by
// all Hummingbird acceptance packets.
func hbirdUnderlayLayers(
	device string,
	srcIP net.IP,
	dstIP net.IP,
	routerMACByte byte,
	srcPort layers.UDPPort,
	dstPort layers.UDPPort,
	incoming bool,
) hbirdUnderlay {
	remoteMAC := net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	routerMAC := net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, routerMACByte}
	srcMAC, dstMAC := routerMAC, remoteMAC
	if incoming {
		srcMAC, dstMAC = remoteMAC, routerMAC
	}
	ip := &layers.IPv4{
		Version: 4, IHL: 5, TTL: 64, SrcIP: srcIP, DstIP: dstIP,
		Protocol: layers.IPProtocolUDP, Flags: layers.IPv4DontFragment,
	}
	udp := &layers.UDP{SrcPort: srcPort, DstPort: dstPort}
	_ = udp.SetNetworkLayerForChecksum(ip)
	return hbirdUnderlay{
		device: device,
		ethernet: &layers.Ethernet{
			SrcMAC: srcMAC, DstMAC: dstMAC, EthernetType: layers.EthernetTypeIPv4,
		},
		ip: ip, udp: udp,
	}
}

// hbirdPacketLen returns the packet length as the router computes it for the
// flyover MAC. It serializes the decoded path into a Raw path, temporarily
// installs it on spkt, reads PacketLen and restores spkt.
func hbirdPacketLen(spkt *slayers.SCION, dpath *hummingbird.Decoded) uint16 {
	savedPath, savedType := spkt.Path, spkt.PathType

	rawBytes := make([]byte, dpath.Len())
	if err := dpath.SerializeTo(rawBytes); err != nil {
		panic(err)
	}
	rawPath := &hummingbird.Raw{}
	if err := rawPath.DecodeFromBytes(rawBytes); err != nil {
		panic(err)
	}
	spkt.Path = rawPath
	spkt.PathType = rawPath.Type()
	l := spkt.PacketLen()

	spkt.Path, spkt.PathType = savedPath, savedType
	return l
}
