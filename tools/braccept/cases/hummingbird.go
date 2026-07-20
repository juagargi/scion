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

// Hummingbird acceptance cases inject packets on one veth and compare the
// router output with the expected packet after the runner normalizes fields
// such as IPv4 IDs and checksums that vary between runs.
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
// Flyover cases receive from main.go the secret derived from the same master
// key as the router. The MAC helpers mirror router/dataplane_hbird_test.go and
// must remain in sync with the router.
//
//
// Hummingbird acceptance coverage.
//
//	Behavior                              Best-effort  Flyover
//	Inbound delivery                      x            x
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
//
// Notes on other not present test cases from router/dataplane_hbird_test.go:
// Reversed-path conversion is a test-fixture operation, not behavior visible on the wire.
// Key lifecycle, token-bucket identity and concurrency, priority labels, and token accounting
// remain unit-only as they are internal state rather than distinct wire behavior.

const hbirdPayload = "actualpayloadbytes"

// hbirdScionUDPPayloadLen is the SCION payload length: the 8-byte SCION/UDP
// header plus the application payload. It is set before computing the flyover
// MAC, which depends on the total packet length seen by the router.
const hbirdScionUDPPayloadLen = 8 + len(hbirdPayload)

// Regular forwarding cases.

// HummingbirdBestEffortChildToParent checks BR transit, reverse direction, best-effort.
// It matches TestProcessHbirdPacket/brtransit_non_consdir_best-effort.
func HummingbirdBestEffortChildToParent(artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdBRTransit(
		artifactsDir, mac, sv, false, false, false, true, "HummingbirdBestEffortChildToParent")
}

// HummingbirdBestEffortParentToChild checks BR transit, construction direction, best-effort.
// It matches TestProcessHbirdPacket/brtransit_consdir_best-effort.
func HummingbirdBestEffortParentToChild(artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdBRTransit(
		artifactsDir, mac, sv, false, true, false, true, "HummingbirdBestEffortParentToChild")
}

// HummingbirdFlyoverParentToChild checks BR transit, construction direction, flyover.
// It matches TestProcessHbirdPacket/brtransit_consdir_flyover.
func HummingbirdFlyoverParentToChild(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdBRTransit(
		artifactsDir, mac, sv, true, true, false, true, "HummingbirdFlyoverParentToChild")
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
		artifactsDir, mac, sv, true, "HummingbirdFlyoverOutbound")
}

// HummingbirdBestEffortChildToChildXover checks same-BR cross-over, best-effort.
// It matches TestProcessHbirdPacket/brtransit_xover_best-effort.
func HummingbirdBestEffortChildToChildXover(artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdChildToChildXover(
		artifactsDir, mac, sv, false, "HummingbirdBestEffortChildToChildXover")
}

// HummingbirdBestEffortInbound checks inbound delivery, best-effort.
// It matches TestProcessHbirdPacket/inbound_best-effort.
func HummingbirdBestEffortInbound(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdInbound(artifactsDir, mac, sv, false, "HummingbirdBestEffortInbound")
}

// HummingbirdBestEffortOutbound checks outbound forwarding, best-effort.
// It matches TestProcessHbirdPacket/outbound_best-effort.
func HummingbirdBestEffortOutbound(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdOutbound(
		artifactsDir, mac, sv, false, "HummingbirdBestEffortOutbound")
}

// HummingbirdBestEffortChildToInternalParent checks direct AS transit, ingress BR, best-effort.
// It matches TestProcessHbirdPacket/astransit_direct_ingress_best-effort.
func HummingbirdBestEffortChildToInternalParent(artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdDirectASTransit(artifactsDir, mac, sv, false, false,
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
	sv []byte,
) runner.Case {
	return hummingbirdDirectASTransit(artifactsDir, mac, sv, false, true,
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
	return hummingbirdBRTransit(artifactsDir, mac, sv, true, false, false, true,
		"HummingbirdFlyoverChildToParentNonConsDir")
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
	return hummingbirdXoverASTransit(
		artifactsDir, mac, sv, true, false, "HummingbirdFlyoverXoverASTransitIngress")
}

// HummingbirdFlyoverXoverASTransitEgress checks AS-transit cross-over, egress BR, flyover.
// It matches TestProcessHbirdPacket/astransit_xover_egress_flyover.
func HummingbirdFlyoverXoverASTransitEgress(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdXoverASTransit(
		artifactsDir, mac, sv, true, true, "HummingbirdFlyoverXoverASTransitEgress")
}

// HummingbirdBestEffortXoverASTransitIngress checks AS-transit cross-over, ingress BR,
// best-effort. It matches TestProcessHbirdPacket/astransit_xover_ingress_best-effort.
func HummingbirdBestEffortXoverASTransitIngress(artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdXoverASTransit(
		artifactsDir, mac, sv, false, false, "HummingbirdBestEffortXoverASTransitIngress")
}

// HummingbirdBestEffortXoverASTransitEgress checks AS-transit cross-over, egress BR,
// best-effort. It matches TestProcessHbirdPacket/astransit_xover_egress_best-effort.
func HummingbirdBestEffortXoverASTransitEgress(artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdXoverASTransit(
		artifactsDir, mac, sv, false, true, "HummingbirdBestEffortXoverASTransitEgress")
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

// Malformed and validation-failure cases.

// HummingbirdMalformedCurrentHopAlignment checks malformed current-hop alignment, best-effort.
// CurrHF points into the middle of a three-line hop.
// It matches TestProcessHbirdPacket/malformed_current_hop_alignment_best-effort.
func HummingbirdMalformedCurrentHopAlignment(artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdBRTransit(
		artifactsDir, mac, sv, false, true, true, false, "HummingbirdMalformedCurrentHopAlignment")
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

// HummingbirdBadFlyoverMAC checks invalid hop MAC / SCMP, flyover.
// It matches TestProcessHbirdSCMP/invalid_mac_inbound_flyover.
func HummingbirdBadFlyoverMAC(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdInboundSCMPFailureCase(
		artifactsDir, mac, sv, hbirdBadFlyoverMAC, "HummingbirdBadFlyoverMAC")
}

// HummingbirdBadBestEffortMAC checks invalid hop MAC / SCMP, best-effort.
// It matches TestProcessHbirdSCMP/invalid_mac_inbound_best-effort.
func HummingbirdBadBestEffortMAC(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdInboundSCMPFailureCase(
		artifactsDir, mac, sv, hbirdBadBestEffortMAC, "HummingbirdBadBestEffortMAC")
}

// HummingbirdInvalidSourceIA checks invalid source IA / SCMP, best-effort.
// It matches TestProcessHbirdSCMP/invalid_source_ia_inbound_best-effort.
func HummingbirdInvalidSourceIA(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdInboundSCMPFailureCase(
		artifactsDir, mac, sv, hbirdInvalidSourceIA, "HummingbirdInvalidSourceIA")
}

// HummingbirdInvalidDestinationIA checks invalid destination IA / SCMP, best-effort.
// It matches TestProcessHbirdSCMP/invalid_destination_ia_inbound_best-effort.
func HummingbirdInvalidDestinationIA(artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdInboundSCMPFailureCase(
		artifactsDir, mac, sv, hbirdInvalidDestinationIA, "HummingbirdInvalidDestinationIA")
}

// HummingbirdInvalidSourceIAFlyover checks invalid source IA / SCMP, flyover.
// It matches TestProcessHbirdSCMP/invalid_source_ia_inbound_flyover.
func HummingbirdInvalidSourceIAFlyover(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdInboundSCMPFailureCase(
		artifactsDir, mac, sv, hbirdInvalidSourceIAFlyover, "HummingbirdInvalidSourceIAFlyover")
}

// HummingbirdInvalidDestinationIAFlyover checks invalid destination IA / SCMP, flyover.
// It matches TestProcessHbirdSCMP/invalid_destination_ia_inbound_flyover.
func HummingbirdInvalidDestinationIAFlyover(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdInboundSCMPFailureCase(artifactsDir, mac, sv,
		hbirdInvalidDestinationIAFlyover, "HummingbirdInvalidDestinationIAFlyover")
}

// HummingbirdInvalidSourceIAOutbound checks invalid outbound source IA / SCMP, best-effort.
// It matches TestProcessHbirdSCMP/invalid_source_ia_outbound_best-effort.
func HummingbirdInvalidSourceIAOutbound(artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdOutboundSCMPFailureCase(
		artifactsDir, mac, sv, hbirdInvalidSourceIA, "HummingbirdInvalidSourceIAOutbound")
}

// HummingbirdInvalidDestinationIAOutbound checks invalid outbound destination IA / SCMP,
// best-effort. It matches TestProcessHbirdSCMP/invalid_destination_ia_outbound_best-effort.
func HummingbirdInvalidDestinationIAOutbound(
	artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdOutboundSCMPFailureCase(artifactsDir, mac, sv,
		hbirdInvalidDestinationIA, "HummingbirdInvalidDestinationIAOutbound")
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

// Router-alert cases.

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
func HummingbirdIngressRouterAlertFlyover(artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdRouterAlertCase(
		artifactsDir, mac, sv, true, true, "HummingbirdIngressRouterAlertFlyover")
}

// HummingbirdEgressRouterAlertFlyover checks egress router alert, flyover.
// It matches TestProcessHbirdRouterAlert/egress_router_alert_flyover.
func HummingbirdEgressRouterAlertFlyover(artifactsDir string, mac hash.Hash, sv []byte,
) runner.Case {
	return hummingbirdRouterAlertCase(
		artifactsDir, mac, sv, true, false, "HummingbirdEgressRouterAlertFlyover")
}

// Regular forwarding helpers.

// hummingbirdBRTransit builds BR-transit cases with the local AS as either an
// up-segment or down-segment transit hop, entering on one external interface
// (child 141 or parent 131) and leaving on the other. With flyover every hop
// carries a reservation, and the router verifies and de-aggregates the current
// hop's MAC; best-effort uses its plain SCION MAC. Every hop contributes the
// same per-mode line count, so changing direction does not change the path's
// overall shape.
// Against construction direction the ingress SegID is derived from the SCION MAC.
// misaligned/expectPacket support the malformed-alignment best-effort case,
// where CurrHF points into the middle of the hop and no packet is expected.
func hummingbirdBRTransit(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	flyover bool,
	consDir bool,
	misaligned bool,
	expectPacket bool,
	name string,
) runner.Case {
	now := time.Now()
	// Construction direction uses the down-segment transit position; against
	// construction direction uses the up-segment transit position.
	pos := hbirdUpTransit
	if consDir {
		pos = hbirdDownTransit
	}
	result := hbirdPath(mac, sv, pos, hbirdModeBRTransit, flyover,
		uint16(hbirdScionUDPPayloadLen), now)
	dpath, scionL := result.Decoded, result.SCION
	inf := result.CurrentINF
	// The plain SCION MAC is reused for both the de-aggregated value and the SegID
	// update, so it is never recomputed against a mutated SegID.
	scionMac := result.ScionMAC[result.Current]

	if misaligned {
		dpath.PathMeta.CurrHF++
	}
	if !consDir {
		// Against construction direction: the ingress SegID is derived from the SCION MAC.
		dpath.InfoFields[inf].UpdateSegID(scionMac)
	}

	input := hbirdSerializeUDP(result.InLink, scionL, []byte(hbirdPayload))
	if !expectPacket {
		return runner.Case{
			Name: name, WriteTo: result.InLink.device, ReadFrom: "no_pkt_expected",
			Input: input, Want: nil, StoreDir: filepath.Join(artifactsDir, name),
		}
	}

	// Expected: forwarded to the far interface, path advanced by one hop; SegID
	// updated with the SCION MAC (against construction direction this is a second,
	// self-canceling XOR). Flyover de-aggregates the current hop MAC.
	result.DeAggregateCurrent()
	if err := dpath.IncPath(hbirdHopLines(flyover)); err != nil {
		panic(err)
	}
	dpath.InfoFields[inf].UpdateSegID(scionMac)
	want := hbirdSerializeUDP(result.OutLink, scionL, []byte(hbirdPayload))
	return hbirdRunnerCase(
		artifactsDir, name, result.InLink.device, result.OutLink.device, input, want)
}

// hummingbirdInbound prepares a Hummingbird test with the last (destination-AS)
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

	// Canonical path with the destination-leaf hop (AS1) current: construction
	// direction, entering this AS on child 141 (ConsIngress), delivered locally.
	now := time.Now()
	result := hbirdPath(mac, sv, hbirdDeliver, hbirdModeDeliver, flyover,
		uint16(hbirdScionUDPPayloadLen), now)
	scionL := result.SCION

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 2345
	scionudp.DstPort = uint16(endhostPort)
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	inputLink := result.InLink
	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options, inputLink.ethernet,
		inputLink.ip, inputLink.udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: delivered to the local host 192.168.0.51 on the internal
	// interface; the path is not advanced. Flyover de-aggregates the current hop
	// MAC; best-effort already carries the plain MAC.
	result.DeAggregateCurrent()
	outputLink := result.OutLink
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
// (first hop), sent out to a child. Analogue of InternalHostToChild. For a
// flyover, the router verifies and de-aggregates the aggregate MAC and advances
// by a flyover hop. Best-effort forwards with the plain SCION MAC and advances
// by a regular hop.
func hummingbirdOutbound(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	flyover bool,
	name string,
) runner.Case {
	// The local AS occupies the source-leaf position: this is the first hop of a
	// locally originated packet, egressing to child 141 in construction direction.
	now := time.Now()
	result := hbirdPath(mac, sv, hbirdOriginate, hbirdModeOriginate, flyover,
		uint16(hbirdScionUDPPayloadLen), now)
	dpath, scionL := result.Decoded, result.SCION
	inf := result.CurrentINF

	inputLink := result.InLink
	input := hbirdSerializeUDP(inputLink, scionL, []byte(hbirdPayload))

	// Expected: forwarded to child 141; path advanced by one hop; SegID updated
	// (construction direction). Flyover de-aggregates the current hop MAC.
	result.DeAggregateCurrent()
	if err := dpath.IncPath(hbirdHopLines(flyover)); err != nil {
		panic(err)
	}
	dpath.InfoFields[inf].UpdateSegID(result.ScionMAC[result.Current])
	outputLink := result.OutLink
	want := hbirdSerializeUDP(outputLink, scionL, []byte(hbirdPayload))
	return hbirdRunnerCase(artifactsDir, name, inputLink.device, outputLink.device, input, want)
}

// hummingbirdChildToChildXover tests a Hummingbird packet that crosses over from
// an up segment to a down segment on the same BR, from a child to another child.
// Analogue of ChildToChildXover; exercises the Hummingbird cross-over handling
// (doHbirdXoverBestEffort / doHbirdXoverFlyover). With flyover the up-segment
// cross-over hop carries a reservation spanning ingress 151 (incoming hop) and
// egress 141 (outgoing hop), which the router verifies and de-aggregates; the
// AS-transit cross-over variants are covered by the
// hummingbirdXoverASTransit{Ingress,Egress} cases.
func hummingbirdChildToChildXover(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	flyover bool,
	name string,
) runner.Case {
	// The local AS occupies the core position, current on its up-segment
	// registration. The two core registrations are handled by the same router
	// (crossing over within it from child 151 to child 141), so only the
	// up-segment registration carries a reservation. The down-segment
	// registration remains a plain hop; it becomes current, is verified, and is
	// advanced over during the same packet-processing pass.
	now := time.Now()
	result := hbirdPath(mac, sv, hbirdXoverUp, hbirdModeXoverSameBR, flyover,
		uint16(hbirdScionUDPPayloadLen), now)
	dpath, scionL := result.Decoded, result.SCION

	// Up-segment core hop MAC (reused, not recomputed against a mutated SegID).
	scionMac1 := result.ScionMAC[result.Current]
	dpath.InfoFields[0].UpdateSegID(scionMac1)

	input := hbirdSerializeUDP(result.InLink, scionL, []byte(hbirdPayload))

	// Expected: forwarded to child 141 after switching to the down segment; both
	// SegIDs updated and the path advanced past the cross-over. Flyover
	// de-aggregates the up-segment hop MAC.
	result.DeAggregateCurrent()
	if err := dpath.IncPath(hbirdHopLines(flyover)); err != nil {
		panic(err)
	}
	if err := dpath.IncPath(hummingbird.HopLines); err != nil {
		panic(err)
	}
	dpath.InfoFields[0].UpdateSegID(scionMac1)
	dpath.InfoFields[1].UpdateSegID(result.ScionMAC[result.Other])
	want := hbirdSerializeUDP(result.OutLink, scionL, []byte(hbirdPayload))
	return hbirdRunnerCase(
		artifactsDir, name, result.InLink.device, result.OutLink.device, input, want)
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
	//     191 (ingress, from brD) -> 141 (egress, brA), and the endpoint IAs are
	//     mirrored accordingly.
	now := time.Now()
	mode := hbirdModeASIngress
	if egressBR {
		mode = hbirdModeASEgress
	}
	result := hbirdPath(mac, sv, hbirdDownTransit, mode, flyover,
		uint16(hbirdScionUDPPayloadLen), now)
	dpath, scionL := result.Decoded, result.SCION
	inf := result.CurrentINF

	input := hbirdSerializeUDP(result.InLink, scionL, []byte(hbirdPayload))

	// The ingress BR forwards the authenticated hop internally without advancing;
	// only the egress BR de-aggregates, updates the SegID and advances.
	if egressBR {
		result.DeAggregateCurrent()
		dpath.InfoFields[inf].UpdateSegID(result.ScionMAC[result.Current])
		if err := dpath.IncPath(hbirdHopLines(flyover)); err != nil {
			panic(err)
		}
	}
	want := hbirdSerializeUDP(result.OutLink, scionL, []byte(hbirdPayload))
	return hbirdRunnerCase(
		artifactsDir, name, result.InLink.device, result.OutLink.device, input, want)
}

// hummingbirdXoverASTransit tests one BR of an AS-transit cross-over: the up
// segment's cross-over hop lands on a child link of one BR and the down
// segment's hop lands on a child link of the other, so the two segments are
// stitched together over the internal network between this BR and sibling brC
// (192.168.0.13). With egressBR false this is the ingress BR: the packet
// arrives externally on child 151 and is forwarded internally to brC; a flyover
// reservation on the up-seg hop is moved to the down-seg hop for the egress BR
// to consume (xoverMoveFlyoverToNext), shifting the SegLens by 2 lines;
// best-effort forwards unchanged apart from the advance. With egressBR true
// this is the egress BR: the packet arrives internally from brC and egresses on
// child 141; a flyover reservation on the down-seg hop is de-aggregated and
// moved back to the up-seg hop (xoverMoveFlyoverToPrevious).
func hummingbirdXoverASTransit(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	flyover bool,
	egressBR bool,
	name string,
) runner.Case {
	// The local AS occupies the core position, with its two registrations split
	// across sibling BRs, so only one of them carries the reservation at a time.
	// The ingress-BR case has it on the up-segment hop; the egress-BR case has it
	// on the down-segment hop, as if received in that form from the sibling.
	now := time.Now()
	pos, mode := hbirdXoverUp, hbirdModeXoverSplitIngress
	if egressBR {
		pos, mode = hbirdXoverDown, hbirdModeXoverSplitEgress
	}
	result := hbirdPath(mac, sv, pos, mode, flyover, uint16(hbirdScionUDPPayloadLen), now)
	dpath, scionL := result.Decoded, result.SCION

	// currIdx/otherIdx are the HopFields indices of the current (variable length)
	// hop and its cross-over neighbor; currInfoIdx/otherInfoIdx are their segments.
	currIdx, otherIdx := result.Current, result.Other
	currInfoIdx, otherInfoIdx := result.CurrentINF, 1-result.CurrentINF

	// The up- and down-segment core hops' plain SCION MACs (reused, not
	// recomputed against mutated SegIDs). The reservation always spans the same
	// interfaces regardless of which core hop currently carries it.
	segMAC := [2][path.MacLen]byte{
		result.ScionMAC[hbirdXoverUp],
		result.ScionMAC[hbirdXoverDown],
	}
	if !egressBR {
		dpath.InfoFields[0].UpdateSegID(segMAC[0])
	}

	input := hbirdSerializeUDP(result.InLink, scionL, []byte(hbirdPayload))

	// Expected: the current hop's reservation (if any) is de-aggregated and moved
	// to the neighboring hop, shrinking its own segment's SegLen by 2 lines and
	// growing the neighbor's by 2; the path advances past the current hop. For the
	// ingress best-effort case, the router's own non-consdir-ingress SegID update
	// is a second, self-canceling XOR.
	advance := hummingbird.HopLines
	if flyover {
		dpath.HopFields[currIdx].Flyover = false
		dpath.HopFields[currIdx].HopField.Mac = segMAC[currInfoIdx]
		dpath.HopFields[otherIdx].Flyover = true
		dpath.HopFields[otherIdx].ResID, dpath.HopFields[otherIdx].Bw = 42, 129
		dpath.HopFields[otherIdx].ResStartTime, dpath.HopFields[otherIdx].Duration = 5, 301
		dpath.PathMeta.SegLen[currInfoIdx] -= 2
		dpath.PathMeta.SegLen[otherInfoIdx] += 2
		if !egressBR {
			// Only the ingress BR re-aggregates the moved-to hop's MAC; the egress
			// BR leaves the up-seg hop's plain SCION MAC untouched.
			result.RecomputeOtherAggregate()
		} else {
			advance = hummingbird.FlyoverLines
		}
	}
	if !egressBR {
		dpath.InfoFields[0].UpdateSegID(segMAC[0])
	} else {
		dpath.InfoFields[1].UpdateSegID(segMAC[1])
	}
	if err := dpath.IncPath(advance); err != nil {
		panic(err)
	}
	want := hbirdSerializeUDP(result.OutLink, scionL, []byte(hbirdPayload))
	return hbirdRunnerCase(
		artifactsDir, name, result.InLink.device, result.OutLink.device, input, want)
}

// hummingbirdPeeringCase builds peering-boundary cases and the ordinary hops
// immediately before or after that boundary in either packet mode.
// This helper function does not use the common hbirdPath,
// as we require peering hop fields, and hbirdPath does not provide them.
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

// Malformed and validation-failure helpers.

// hummingbirdMalformedFlyover builds a valid flyover BR-transit path, then
// points CurrHF at the second line of the current five-line flyover. The router
// must discard that malformed current-hop alignment.
func hummingbirdMalformedFlyover(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	now := time.Now()
	result := hbirdPath(mac, sv, hbirdDownTransit, hbirdModeBRTransit, true,
		uint16(hbirdScionUDPPayloadLen), now)
	dpath, scionL := result.Decoded, result.SCION
	// The MAC is already computed against the aligned metadata; only CurrHF is
	// malformed afterward.
	dpath.PathMeta.CurrHF++
	input := hbirdSerializeUDP(result.InLink, scionL, []byte(hbirdPayload))
	return hbirdRunnerCase(artifactsDir, "HummingbirdMalformedCurrentHopAlignmentFlyover",
		result.InLink.device, "no_pkt_expected", input, nil)
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

// hummingbirdInboundSCMPFailureCase builds an inbound validation failure and its
// expected SCMP Parameter Problem response.
func hummingbirdInboundSCMPFailureCase(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	mode hbirdFailureMode,
	name string,
) runner.Case {
	return hummingbirdSCMPFailure(artifactsDir, mac, sv, mode, name, false)
}

// hummingbirdOutboundSCMPFailureCase builds a locally originated (first-hop) validation
// failure and its expected SCMP Parameter Problem response. Unlike
// hummingbirdSCMPFailureCase (inbound), the invalid IA is caught before an egress
// interface is ever chosen, so the reply is sent back internally rather than out an
// external link. This helper is used for the invalid-source and
// invalid-destination IA modes, in both best-effort and flyover form.
func hummingbirdOutboundSCMPFailureCase(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	mode hbirdFailureMode,
	name string,
) runner.Case {
	return hummingbirdSCMPFailure(artifactsDir, mac, sv, mode, name, true)
}

// hummingbirdSCMPFailure builds the offending packet and expected Parameter
// Problem reply shared by the inbound and locally originated failure cases.
func hummingbirdSCMPFailure(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	mode hbirdFailureMode,
	name string,
	outbound bool,
) runner.Case {
	pos, pathMode := hbirdDeliver, hbirdModeDeliver
	replyDstHost := "172.16.4.1"
	replyLink := hbirdExternalOutput(141)
	if outbound {
		pos, pathMode = hbirdOriginate, hbirdModeOriginate
		replyDstHost = "192.168.0.51"
		replyLink = hbirdInternalOutput(51, 30041)
	}

	result := hbirdPath(mac, sv, pos, pathMode, mode.flyover(),
		uint16(hbirdScionUDPPayloadLen), time.Now())
	code, pointer := applyHbirdFailure(&result, mode, outbound)
	input := hbirdSerializeUDP(result.InLink, result.SCION, []byte(hbirdPayload))

	prepareHbirdSCMPReply(&result, replyDstHost, !outbound)
	want := hbirdSerializeSCMPParameterProblem(
		replyLink, result.SCION, code, pointer, hbirdSCIONQuote(input))
	testCase := hbirdRunnerCase(
		artifactsDir, name, result.InLink.device, replyLink.device, input, want)
	testCase.NormalizePacket = scmpNormalizePacket
	return testCase
}

func (m hbirdFailureMode) flyover() bool {
	return m == hbirdBadFlyoverMAC || m == hbirdInvalidSourceIAFlyover ||
		m == hbirdInvalidDestinationIAFlyover
}

// applyHbirdFailure mutates an otherwise-valid packet and returns the SCMP code
// and pointer expected for that validation failure.
func applyHbirdFailure(
	result *hbirdPathResult,
	mode hbirdFailureMode,
	outbound bool,
) (slayers.SCMPCode, int) {
	switch mode {
	case hbirdBadFlyoverMAC, hbirdBadBestEffortMAC:
		if outbound {
			panic("MAC failure mode is not supported for an outbound SCMP case")
		}
		result.Decoded.HopFields[result.Current].HopField.Mac[0] ^= 0xff
		pointer := slayers.CmnHdrLen + result.SCION.AddrHdrLen() + hummingbird.MetaLen +
			path.InfoLen*result.Decoded.NumINF +
			int(result.Decoded.PathMeta.CurrHF)*hummingbird.LineLen
		return slayers.SCMPCodeInvalidHopFieldMAC, pointer
	case hbirdInvalidSourceIA, hbirdInvalidSourceIAFlyover:
		if outbound {
			result.SCION.SrcIA = addr.MustParseIA("1-ff00:0:2")
		} else {
			result.SCION.SrcIA = addr.MustParseIA("1-ff00:0:1")
		}
		return slayers.SCMPCodeInvalidSourceAddress, slayers.CmnHdrLen + addr.IABytes
	case hbirdInvalidDestinationIA, hbirdInvalidDestinationIAFlyover:
		if outbound {
			result.SCION.DstIA = addr.MustParseIA("1-ff00:0:1")
		} else {
			result.SCION.DstIA = addr.MustParseIA("1-ff00:0:9")
		}
		return slayers.SCMPCodeInvalidDestinationAddress, slayers.CmnHdrLen
	default:
		panic("unknown Hummingbird failure mode")
	}
}

// prepareHbirdSCMPReply updates the SCION endpoints and reverses the path as
// prepareHbirdSCMP does. External replies additionally update the SegID and
// advance past the local hop.
func prepareHbirdSCMPReply(result *hbirdPathResult, dstHost string, external bool) {
	scionL := result.SCION
	scionL.DstIA = scionL.SrcIA
	scionL.SrcIA = addr.MustParseIA("1-ff00:0:1")
	if err := scionL.SetDstAddr(addr.MustParseHost(dstHost)); err != nil {
		panic(err)
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost("192.168.0.11")); err != nil {
		panic(err)
	}

	reversed, err := result.Decoded.Reverse()
	if err != nil {
		panic(err)
	}
	revPath := reversed.(*hummingbird.Decoded)
	if external {
		info := &revPath.InfoFields[revPath.PathMeta.CurrINF]
		if info.ConsDir {
			hop, err := revPath.GetCurrentHopField()
			if err != nil {
				panic(err)
			}
			info.UpdateSegID(hop.HopField.Mac)
		}
		if err := revPath.IncPath(hummingbird.HopLines); err != nil {
			panic(err)
		}
	}
	scionL.Path = revPath
	scionL.PathType = revPath.Type()
}

// hbirdSerializeSCMPParameterProblem serializes a Parameter Problem reply with
// the packet-authenticator extension used by the router's SCMP slow path.
func hbirdSerializeSCMPParameterProblem(
	underlay hbirdUnderlay,
	scionL *slayers.SCION,
	code slayers.SCMPCode,
	pointer int,
	quote []byte,
) []byte {
	scionL.NextHdr = slayers.End2EndClass
	e2e := normalizedSCMPPacketAuthEndToEndExtn()
	e2e.NextHdr = slayers.L4SCMP
	scmpH := &slayers.SCMP{
		TypeCode: slayers.CreateSCMPTypeCode(slayers.SCMPTypeParameterProblem, code),
	}
	scmpH.SetNetworkLayerForChecksum(scionL)
	scmpP := &slayers.SCMPParameterProblem{Pointer: uint16(pointer)}

	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, gopacket.SerializeOptions{
		FixLengths: true, ComputeChecksums: true,
	}, underlay.ethernet, underlay.ip, underlay.udp, scionL, e2e, scmpH, scmpP,
		gopacket.Payload(quote)); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

// hbirdSCIONQuote removes the fixed Ethernet, IPv4, and underlay UDP headers
// from a serialized input packet, leaving the SCION packet quoted by SCMP.
func hbirdSCIONQuote(packet []byte) []byte {
	const underlayHeaderLen = 14 + 20 + 8
	return packet[underlayHeaderLen:]
}

// Router-alert helpers.

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

	// The tested hop is a construction-direction BR-transit hop (parent 131 ->
	// child 141). The SCION payload length must describe the SCMP traceroute
	// request exactly; in flyover mode the aggregate MAC is also bound to the
	// resulting packet length.
	now := time.Now()
	payloadLen := uint16(slayers.ScmpHeaderSize(slayers.SCMPTypeTracerouteRequest))
	result := hbirdPath(mac, sv, hbirdDownTransit, hbirdModeBRTransit, flyover, payloadLen, now)
	dpath, scionL := result.Decoded, result.SCION
	// This packet carries an SCMP traceroute request, not UDP.
	scionL.NextHdr = slayers.L4SCMP
	srcA := addr.MustParseHost("172.16.3.1")
	// The router-alert flags are part of the hop field bytes the MAC covers, so
	// hbirdPath's MAC (computed without them) must be recomputed once they're set.
	dpath.HopFields[result.Current].HopField.IngressRouterAlert = ingressAlert
	dpath.HopFields[result.Current].HopField.EgressRouterAlert = !ingressAlert
	result.RecomputeCurrentAggregate()

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

	dpath.HopFields[result.Current].HopField.IngressRouterAlert = false
	dpath.HopFields[result.Current].HopField.EgressRouterAlert = false
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

// Canonical interface numbers for the non-current hops in the reusable
// six-hop path template:
//
//	Up segment:   0 -> far-up -> near-up -> 0
//	Down segment: 0 -> near-down -> far-down -> 0
//
// hbirdPath replaces the tested hop's interfaces with scenario-specific values.
// The remaining values are not inspected by the router in these cases, but are
// kept uniform so the reusable paths have a consistent shape.
const (
	hbirdFarUpIface    = 101
	hbirdNearUpIface   = 102
	hbirdNearDownIface = 103
	hbirdFarDownIface  = 104
)

// hbirdHopLines returns the per-hop line count for the given mode: a plain hop
// is hummingbird.HopLines, a flyover hop is hummingbird.FlyoverLines. Every
// hop of the reusable path (filler or current) uses this same count within a
// given mode, except at a crossover where only one of the two core
// registrations carries the reservation at a time.
func hbirdHopLines(flyover bool) int {
	if flyover {
		return hummingbird.FlyoverLines
	}
	return hummingbird.HopLines
}

// hbirdFillerHop builds one hop of the path template. ingress and egress are
// construction-direction interface IDs. With flyover it also carries a
// structurally valid reservation and occupies five lines instead of three.
func hbirdFillerHop(ingress, egress uint16, flyover bool) hummingbird.FlyoverHopField {
	fhf := hummingbird.FlyoverHopField{
		HopField: path.HopField{ConsIngress: ingress, ConsEgress: egress},
	}
	if flyover {
		fhf.Flyover = true
		fhf.ResID = 42
		fhf.Bw = 129
		fhf.ResStartTime = 5
		fhf.Duration = 301
	}
	return fhf
}

// hbirdPosition selects the structural role occupied by the AS under test
// (1-ff00:0:1) in the reusable two-segment path. The six values map one-to-one
// to its six hop fields (up segment HF0..HF2, down segment HF3..HF5). Endpoint
// IAs and the local AS's interfaces depend on the selected scenario.
type hbirdPosition uint8

const (
	hbirdOriginate   hbirdPosition = iota // HF0, up segment, source leaf
	hbirdUpTransit                        // HF1, up-segment transit
	hbirdXoverUp                          // HF2, core's up-segment side
	hbirdXoverDown                        // HF3, core's down-segment side
	hbirdDownTransit                      // HF4, down-segment transit
	hbirdDeliver                          // HF5, down segment, destination leaf
)

// hbirdMode selects how the current hop is forwarded. Together with the position
// it fixes the tested interfaces, direction and input/output links, all drawn
// from the constants in hbirdConfigFor; the caller passes no topology value.
type hbirdMode uint8

const (
	hbirdModeBRTransit         hbirdMode = iota // external in, external out, same BR
	hbirdModeDeliver                            // external in, local delivery
	hbirdModeOriginate                          // internal in, external out
	hbirdModeASIngress                          // external in, internal out (path not advanced)
	hbirdModeASEgress                           // internal in, external out
	hbirdModeXoverSameBR                        // same-BR crossover
	hbirdModeXoverSplitIngress                  // split-BR crossover, ingress BR
	hbirdModeXoverSplitEgress                   // split-BR crossover, egress BR
)

// hbirdConf holds the constant-derived parameters for one (position, mode)
// scenario. Every value here is fixed; the caller of hbirdPath supplies only the
// position, mode and flyover flag. consIn/consEg are the current hop's
// construction-direction interfaces (non-crossover); spanIn/spanEg are the
// reservation's packet-direction ingress/egress at a crossover (which also
// become the two core hops' stored egress interfaces).
type hbirdConf struct {
	consIn, consEg   uint16
	spanIn, spanEg   uint16
	consDirUp        bool
	consDirDown      bool
	srcIA, dstIA     string
	srcHost, dstHost string
	inLink, outLink  hbirdUnderlay
}

// hbirdConfigFor is the single constant table mapping a (position, mode)
// scenario to its fixed topology values and input/output links.
func hbirdConfigFor(pos hbirdPosition, mode hbirdMode) hbirdConf {
	switch {
	case pos == hbirdOriginate && mode == hbirdModeOriginate:
		return hbirdConf{
			consIn: 0, consEg: 141, consDirUp: true,
			srcIA: "1-ff00:0:1", dstIA: "1-ff00:0:4",
			srcHost: "192.168.0.51", dstHost: "174.16.4.1",
			inLink:  hbirdInternalInput(51, 30041),
			outLink: hbirdExternalOutput(141),
		}
	case pos == hbirdUpTransit && mode == hbirdModeBRTransit:
		return hbirdConf{
			consIn: 131, consEg: 141, consDirUp: false,
			srcIA: "1-ff00:0:4", dstIA: "1-ff00:0:3",
			srcHost: "172.16.4.1", dstHost: "174.16.3.1",
			inLink:  hbirdExternalInput(141),
			outLink: hbirdExternalOutput(131),
		}
	case pos == hbirdDownTransit && mode == hbirdModeBRTransit:
		return hbirdConf{
			consIn: 131, consEg: 141, consDirDown: true,
			srcIA: "1-ff00:0:3", dstIA: "1-ff00:0:4",
			srcHost: "172.16.3.1", dstHost: "174.16.4.1",
			inLink:  hbirdExternalInput(131),
			outLink: hbirdExternalOutput(141),
		}
	case pos == hbirdDownTransit && mode == hbirdModeASIngress:
		return hbirdConf{
			consIn: 141, consEg: 191, consDirDown: true,
			srcIA: "1-ff00:0:4", dstIA: "1-ff00:0:9",
			srcHost: "172.16.4.1", dstHost: "174.16.9.1",
			inLink:  hbirdExternalInput(141),
			outLink: hbirdInternalOutput(14, 30004),
		}
	case pos == hbirdDownTransit && mode == hbirdModeASEgress:
		return hbirdConf{
			consIn: 191, consEg: 141, consDirDown: true,
			srcIA: "1-ff00:0:9", dstIA: "1-ff00:0:4",
			srcHost: "172.16.9.1", dstHost: "174.16.4.1",
			inLink:  hbirdInternalInput(14, 30004),
			outLink: hbirdExternalOutput(141),
		}
	case pos == hbirdDeliver && mode == hbirdModeDeliver:
		return hbirdConf{
			consIn: 141, consEg: 0, consDirDown: true,
			srcIA: "1-ff00:0:4", dstIA: "1-ff00:0:1",
			srcHost: "172.16.4.1", dstHost: "192.168.0.51",
			inLink:  hbirdExternalInput(141),
			outLink: hbirdInternalOutput(51, 21000),
		}
	case pos == hbirdXoverUp && mode == hbirdModeXoverSameBR:
		return hbirdConf{
			spanIn: 151, spanEg: 141, consDirUp: false, consDirDown: true,
			srcIA: "1-ff00:0:5", dstIA: "1-ff00:0:4",
			srcHost: "172.16.5.1", dstHost: "174.16.4.1",
			inLink:  hbirdExternalInput(151),
			outLink: hbirdExternalOutput(141),
		}
	case pos == hbirdXoverUp && mode == hbirdModeXoverSplitIngress:
		return hbirdConf{
			spanIn: 151, spanEg: 181, consDirUp: false, consDirDown: true,
			srcIA: "1-ff00:0:5", dstIA: "1-ff00:0:8",
			srcHost: "172.16.5.1", dstHost: "172.16.8.1",
			inLink:  hbirdExternalInput(151),
			outLink: hbirdInternalOutput(13, 30003),
		}
	case pos == hbirdXoverDown && mode == hbirdModeXoverSplitEgress:
		return hbirdConf{
			spanIn: 181, spanEg: 141, consDirUp: false, consDirDown: true,
			srcIA: "1-ff00:0:5", dstIA: "1-ff00:0:4",
			srcHost: "172.16.5.1", dstHost: "174.16.4.1",
			inLink:  hbirdInternalInput(13, 30003),
			outLink: hbirdExternalOutput(141),
		}
	default:
		panic("hbirdConfigFor: unsupported position/mode combination")
	}
}

// hbirdPathResult is what hbirdPath returns: the fully built, valid Hummingbird
// path and its SCION header (every hop's initial MAC already computed), the
// HopFields index of the current (tested) hop and, at a crossover, the other
// core hop (else -1), the InfoFields index of the current hop, every hop's
// plain SCION MAC, and the scenario's input/output underlay links. hbirdPath performs
// initial MAC computation; callers use the methods below when mutations require
// a MAC to be replaced or recomputed.
type hbirdPathResult struct {
	Decoded    *hummingbird.Decoded
	SCION      *slayers.SCION
	Current    int
	Other      int
	CurrentINF int
	ScionMAC   [][path.MacLen]byte
	InLink     hbirdUnderlay
	OutLink    hbirdUnderlay

	macHasher      hash.Hash
	sv             []byte
	flyover        bool
	spanIn, spanEg uint16 // crossover reservation span (packet direction)
}

// DeAggregateCurrent sets the current hop's MAC to its plain SCION MAC, as the
// router does after verifying a flyover before forwarding it on.
func (r *hbirdPathResult) DeAggregateCurrent() {
	r.Decoded.HopFields[r.Current].HopField.Mac = r.ScionMAC[r.Current]
}

// RecomputeCurrentAggregate recomputes the current hop's MAC after the caller
// mutates MAC-covered fields (e.g. router-alert flags): the plain SCION MAC
// cached in ScionMAC and, in flyover mode, the aggregate stored on the wire.
func (r *hbirdPathResult) RecomputeCurrentAggregate() {
	hf := &r.Decoded.HopFields[r.Current]
	info := r.Decoded.InfoFields[r.CurrentINF]
	plain := path.MAC(r.macHasher, info, hf.HopField, nil)
	r.ScionMAC[r.Current] = plain
	if r.flyover {
		hf.HopField.Mac = hbirdAggregateMAC(
			r.macHasher, r.sv, r.SCION, r.Decoded, info, *hf, r.Decoded.PathMeta)
	} else {
		hf.HopField.Mac = plain
	}
}

// RecomputeOtherAggregate recomputes the crossover's other core hop's aggregate
// MAC after a reservation has been moved onto it (split-BR ingress). The
// reservation spans (spanIn, spanEg) regardless of which core hop carries it, so
// it must be called after the SegLen/flyover mutations that grow the path.
func (r *hbirdPathResult) RecomputeOtherAggregate() {
	hf := &r.Decoded.HopFields[r.Other]
	info := r.Decoded.InfoFields[1-r.CurrentINF]
	hf.HopField.Mac = hbirdAggregateMACForInterfaces(
		r.macHasher, r.sv, r.SCION, r.Decoded, r.spanIn, r.spanEg, info, *hf, r.Decoded.PathMeta)
}

// hbirdPath builds the reusable two-segment Hummingbird path shared by most
// acceptance cases and places the AS under test (1-ff00:0:1) at the selected
// structural position. The up segment contains HF0..HF2 and the down segment
// HF3..HF5; HF2 and HF3 are the two registrations at a segment crossover. pos
// selects the current hop, while mode selects how the local AS forwards it.
// Together they determine the endpoint IAs, interface IDs, host addresses,
// direction, and underlay links from hbirdConfigFor. With flyover, every hop
// carries a reservation except the crossover's non-current registration, and
// the aggregate MACs are computed here. Without flyover, reservations are
// stripped and every hop carries its plain SCION MAC.
//
// mac/sv are the router's MAC hasher and Hummingbird secret (crypto material,
// not topology). payloadLen must match what the caller serializes, since the
// flyover MAC is bound to the total packet length. now supplies BaseTS and the
// info-field timestamps; HighResTS is fixed at 500 milliseconds below.
func hbirdPath(
	mac hash.Hash,
	sv []byte,
	pos hbirdPosition,
	mode hbirdMode,
	flyover bool,
	payloadLen uint16,
	now time.Time,
) hbirdPathResult {
	c := hbirdConfigFor(pos, mode)
	current := int(pos)
	isXover := pos == hbirdXoverUp || pos == hbirdXoverDown
	other := -1
	if pos == hbirdXoverUp {
		other = int(hbirdXoverDown)
	} else if pos == hbirdXoverDown {
		other = int(hbirdXoverUp)
	}

	// Start with uniform template hops. Scenario-specific interfaces replace the
	// current hop (or both crossover registrations) below.
	hops := []hummingbird.FlyoverHopField{
		hbirdFillerHop(0, hbirdFarUpIface, true),
		hbirdFillerHop(hbirdFarUpIface, hbirdNearUpIface, true),
		hbirdFillerHop(hbirdNearUpIface, 0, true),
		hbirdFillerHop(0, hbirdNearDownIface, true),
		hbirdFillerHop(hbirdNearDownIface, hbirdFarDownIface, true),
		hbirdFillerHop(hbirdFarDownIface, 0, true),
	}
	if isXover {
		// The two core registrations carry the reservation-spanning interfaces;
		// only the current one is a flyover, the other stays a plain hop, matching
		// how the router represents a crossover (exactly one carries the reservation).
		hops[hbirdXoverUp] = hbirdFillerHop(0, c.spanIn, current == int(hbirdXoverUp))
		hops[hbirdXoverDown] = hbirdFillerHop(0, c.spanEg, current == int(hbirdXoverDown))
	} else {
		hops[current].HopField.ConsIngress = c.consIn
		hops[current].HopField.ConsEgress = c.consEg
	}

	// Segment line counts and CurrHF/CurrINF follow directly from each hop's
	// per-mode line count (flyover: 5, plain: 3).
	var segLen [3]uint8
	var currHF uint8
	for i := range hops {
		lines := uint8(hummingbird.HopLines)
		if hops[i].Flyover {
			lines = uint8(hummingbird.FlyoverLines)
		}
		seg := 0
		if i >= 3 {
			seg = 1
		}
		segLen[seg] += lines
		if i < current {
			currHF += lines
		}
	}
	currINF := uint8(0)
	if current >= 3 {
		currINF = 1
	}

	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrINF: currINF, CurrHF: currHF, SegLen: segLen,
				BaseTS: util.TimeToSecs(now), HighResTS: 500 << 22,
			},
			NumINF:   2,
			NumLines: int(segLen[0] + segLen[1]),
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: c.consDirUp, Timestamp: util.TimeToSecs(now)},   // up seg
			{SegID: 0x222, ConsDir: c.consDirDown, Timestamp: util.TimeToSecs(now)}, // down seg
		},
		HopFields: hops,
	}

	scionL := hbirdSCION(c.srcIA, c.dstIA, c.srcHost, c.dstHost, dpath)
	scionL.PayloadLen = payloadLen

	// Compute every hop's MAC: plain SCION MAC (cached in scionMAC) plus, for
	// flyover hops, the aggregate stored on the wire. At a crossover the
	// reservation spans (spanIn, spanEg), not the hop's own interfaces.
	scionMAC := make([][path.MacLen]byte, len(dpath.HopFields))
	for i := range dpath.HopFields {
		hf := &dpath.HopFields[i]
		info := dpath.InfoFields[0]
		if i >= 3 {
			info = dpath.InfoFields[1]
		}
		scionMAC[i] = path.MAC(mac, info, hf.HopField, nil)
		if !hf.Flyover {
			hf.HopField.Mac = scionMAC[i]
			continue
		}
		if isXover {
			hf.HopField.Mac = hbirdAggregateMACForInterfaces(
				mac, sv, scionL, dpath, c.spanIn, c.spanEg, info, *hf, dpath.PathMeta)
		} else {
			hf.HopField.Mac = hbirdAggregateMAC(mac, sv, scionL, dpath, info, *hf, dpath.PathMeta)
		}
	}

	if !flyover {
		// RemoveFlyovers strips reservations and corrects SegLen/CurrHF/NumLines
		// but does not fix MACs, so restore every hop's plain SCION MAC.
		if err := dpath.RemoveFlyovers(); err != nil {
			panic(err)
		}
		for i := range dpath.HopFields {
			dpath.HopFields[i].HopField.Mac = scionMAC[i]
		}
	}

	return hbirdPathResult{
		Decoded:    dpath,
		SCION:      scionL,
		Current:    current,
		Other:      other,
		CurrentINF: int(currINF),
		ScionMAC:   scionMAC,
		InLink:     c.inLink,
		OutLink:    c.outLink,
		macHasher:  mac,
		sv:         sv,
		flyover:    flyover,
		spanIn:     c.spanIn,
		spanEg:     c.spanEg,
	}
}

// hbirdSCION creates the base SCION header used by Hummingbird cases. Callers
// that carry SCMP replace the default UDP next-header value.
func hbirdSCION(srcIA, dstIA, srcHost, dstHost string, dpath *hummingbird.Decoded,
) *slayers.SCION {
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
// ingress/egress in packet traversal direction.
// At a cross-over the reservation spans the ingress of
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

// hbirdExternalInput returns the underlay used to inject a packet from an
// external host into the router interface identified by id.
// E.g. for id=121:
// Device:          veth_121_host
// Source IP:       192.168.12.3
// Destination IP:  192.168.12.2
// Router MAC:      f0:0d:ca:fe:00:12 (the passed value is 12)
// UDP ports:       40000 -> 50000
func hbirdExternalInput(id byte) hbirdUnderlay {
	return hbirdUnderlayLayers(
		"veth_"+string([]byte{'0' + id/100, '0' + id/10%10, '0' + id%10})+"_host",
		net.IP{192, 168, id / 10, 3},
		net.IP{192, 168, id / 10, 2},
		(id/100)<<4|id/10%10,
		40000,
		50000,
		true)
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

// hbirdUnderlayLayers creates the Ethernet, IPv4, and UDP envelope used by the
// reusable Hummingbird packet helpers.
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
