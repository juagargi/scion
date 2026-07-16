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
//	AS-transit cross-over, ingress BR     missing      x
//	AS-transit cross-over, egress BR      missing      x
//	Same-BR cross-over                    x            x
//	Peering boundary, construction dir.   x            x
//	Peering boundary, reverse dir.        x            x
//	After peering, downstream             x            x
//	Before peering, upstream              x            x
//	Malformed current-hop alignment       x            x
//	Invalid hop MAC / SCMP                x            x
//	Invalid source IA / SCMP              x            x
//	Invalid destination IA / SCMP         x            x
//	Invalid outbound source IA / SCMP     missing      missing
//	Invalid outbound destination IA/SCMP  missing      missing
//	Ingress router alert                  missing      missing
//	Egress router alert                   missing      missing
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
	return hummingbirdBestEffortTransit(
		artifactsDir, mac, false, 3, true, "HummingbirdBestEffortChildToParent")
}

// HummingbirdBestEffortParentToChild checks BR transit, construction direction, best-effort.
// It matches TestProcessHbirdPacket/brtransit_consdir_best-effort.
func HummingbirdBestEffortParentToChild(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdBestEffortTransit(
		artifactsDir, mac, true, 3, true, "HummingbirdBestEffortParentToChild")
}

// HummingbirdMalformedCurrentHopAlignment checks malformed current-hop alignment, best-effort.
// CurrHF points into the middle of a three-line hop.
// It matches TestProcessHbirdPacket/malformed_current_hop_alignment_best-effort.
func HummingbirdMalformedCurrentHopAlignment(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdBestEffortTransit(
		artifactsDir, mac, true, 4, false, "HummingbirdMalformedCurrentHopAlignment")
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
	return hummingbirdFlyoverParentToChild(artifactsDir, mac, sv)
}

// HummingbirdFlyoverInbound checks inbound delivery, flyover.
// It matches TestProcessHbirdPacket/inbound_flyover.
func HummingbirdFlyoverInbound(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdFlyoverInbound(artifactsDir, mac, sv)
}

// HummingbirdFlyoverOutbound checks outbound forwarding, flyover.
// It matches TestProcessHbirdPacket/outbound_flyover.
func HummingbirdFlyoverOutbound(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdFlyoverOutbound(
		artifactsDir, mac, sv, 0, 301, 129, []byte(hbirdPayload), "HummingbirdFlyoverOutbound")
}

// HummingbirdExpiredReservation checks an expired reservation, flyover.
// It matches TestProcessHbirdPacket/reservation_expired_flyover.
func HummingbirdExpiredReservation(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdFlyoverOutbound(
		artifactsDir, mac, sv, 0, 2, 129, []byte(hbirdPayload), "HummingbirdExpiredReservation")
}

// HummingbirdBandwidthExceeded checks reservation bandwidth exceeded, flyover.
// It matches TestProcessHbirdPacket/reservation_exceeds_bandwidth_flyover.
func HummingbirdBandwidthExceeded(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdFlyoverOutbound(
		artifactsDir, mac, sv, 0, 301, 1, make([]byte, 512), "HummingbirdBandwidthExceeded")
}

// HummingbirdStaleFlyover checks stale packet freshness, flyover.
// It matches TestProcessHbirdPacket/freshness_stale_flyover.
func HummingbirdStaleFlyover(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdFlyoverOutbound(
		artifactsDir, mac, sv, -6*time.Second, 301, 129, []byte(hbirdPayload),
		"HummingbirdStaleFlyover")
}

// HummingbirdFutureFlyover checks future packet freshness, flyover.
// It matches TestProcessHbirdPacket/freshness_future_flyover.
func HummingbirdFutureFlyover(artifactsDir string, mac hash.Hash, sv []byte) runner.Case {
	return hummingbirdFlyoverOutbound(
		artifactsDir, mac, sv, 6*time.Second, 301, 129, []byte(hbirdPayload),
		"HummingbirdFutureFlyover")
}

// HummingbirdBestEffortChildToChildXover checks same-BR cross-over, best-effort.
// It matches TestProcessHbirdPacket/brtransit_xover_best-effort.
func HummingbirdBestEffortChildToChildXover(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdBestEffortChildToChildXover(artifactsDir, mac)
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

// HummingbirdBestEffortInbound checks inbound delivery, best-effort.
// It matches TestProcessHbirdPacket/inbound_best-effort.
func HummingbirdBestEffortInbound(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdBestEffortInbound(artifactsDir, mac)
}

// HummingbirdBestEffortOutbound checks outbound forwarding, best-effort.
// It matches TestProcessHbirdPacket/outbound_best-effort.
func HummingbirdBestEffortOutbound(artifactsDir string, mac hash.Hash) runner.Case {
	return hummingbirdBestEffortOutbound(artifactsDir, mac)
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
	return hummingbirdFlyoverChildToParentNonConsDir(artifactsDir, mac, sv)
}

// HummingbirdFlyoverChildToChildXover checks same-BR cross-over, flyover.
// It matches TestProcessHbirdPacket/brtransit_xover_flyover.
func HummingbirdFlyoverChildToChildXover(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdFlyoverChildToChildXover(artifactsDir, mac, sv)
}

// HummingbirdFlyoverXoverASTransitIngress checks AS-transit cross-over, ingress BR, flyover.
// It matches TestProcessHbirdPacket/astransit_xover_ingress_flyover.
func HummingbirdFlyoverXoverASTransitIngress(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdFlyoverXoverASTransitIngress(artifactsDir, mac, sv)
}

// HummingbirdFlyoverXoverASTransitEgress checks AS-transit cross-over, egress BR, flyover.
// It matches TestProcessHbirdPacket/astransit_xover_egress_flyover.
func HummingbirdFlyoverXoverASTransitEgress(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdFlyoverXoverASTransitEgress(artifactsDir, mac, sv)
}

// HummingbirdFlyoverChildToPeer checks peering boundary, reverse direction, flyover.
// It matches TestProcessHbirdPacket/brtransit_peering_non_consdir_flyover.
func HummingbirdFlyoverChildToPeer(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdFlyoverChildToPeer(artifactsDir, mac, sv)
}

// HummingbirdFlyoverPeerToChild checks peering boundary, construction direction, flyover.
// It matches TestProcessHbirdPacket/brtransit_peering_consdir_flyover.
func HummingbirdFlyoverPeerToChild(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	return hummingbirdFlyoverPeerToChild(artifactsDir, mac, sv)
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

// hummingbirdBestEffortTransit builds best-effort BR-transit and malformed-hop cases.
func hummingbirdBestEffortTransit(
	artifactsDir string,
	mac hash.Hash,
	consDir bool,
	currHF uint8,
	expectPacket bool,
	name string,
) runner.Case {
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
	writeTo, readFrom := "veth_141_host", "veth_131_host"
	srcIA, dstIA := "1-ff00:0:4", "1-ff00:0:3"
	srcHost, dstHost := "172.16.4.1", "174.16.3.1"
	if consDir {
		ethernet.DstMAC[5] = 0x13
		ip.SrcIP, ip.DstIP = net.IP{192, 168, 13, 3}, net.IP{192, 168, 13, 2}
		writeTo, readFrom = "veth_131_host", "veth_141_host"
		srcIA, dstIA = "1-ff00:0:3", "1-ff00:0:4"
		srcHost, dstHost = "172.16.3.1", "174.16.4.1"
	}

	// Up segment (against construction direction): enter on child 141, exit on
	// parent 131. Current hop is the middle hop (line 3).
	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    currHF,
				SegLen:    [3]uint8{9, 0, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   1,
			NumLines: 9, // Three best-effort hops.
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: consDir, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 411, ConsEgress: 0}},
			{HopField: path.HopField{ConsIngress: 131, ConsEgress: 141}},
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 311}},
		},
	}
	dpath.HopFields[1].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	if !consDir {
		dpath.InfoFields[0].UpdateSegID(dpath.HopFields[1].HopField.Mac)
	}

	scionL := &slayers.SCION{
		Version:      0,
		TrafficClass: 0xb8,
		FlowID:       0xdead,
		NextHdr:      slayers.L4UDP,
		PathType:     hummingbird.PathType,
		SrcIA:        addr.MustParseIA(srcIA),
		DstIA:        addr.MustParseIA(dstIA),
		Path:         dpath,
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost(srcHost)); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost(dstHost)); err != nil {
		panic(err)
	}

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}
	if !expectPacket {
		return runner.Case{
			Name: name, WriteTo: writeTo, ReadFrom: "no_pkt_expected",
			Input: input.Bytes(), Want: nil, StoreDir: filepath.Join(artifactsDir, name),
		}
	}

	// Expected: forwarded to parent 131, path advanced by one regular hop.
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x13}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 13, 2}
	ip.DstIP = net.IP{192, 168, 13, 3}
	if consDir {
		ethernet.SrcMAC[5] = 0x14
		ip.SrcIP, ip.DstIP = net.IP{192, 168, 14, 2}, net.IP{192, 168, 14, 3}
	}
	udp.SrcPort, udp.DstPort = udp.DstPort, udp.SrcPort
	if err := dpath.IncPath(hummingbird.HopLines); err != nil {
		panic(err)
	}
	dpath.InfoFields[0].UpdateSegID(dpath.HopFields[1].HopField.Mac)

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     name,
		WriteTo:  writeTo,
		ReadFrom: readFrom,
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, name),
	}
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

// hummingbirdFlyoverParentToChild tests transit of a Hummingbird packet whose
// current hop carries a flyover (reservation), in construction direction from a
// parent to a child. The router must verify the aggregate MAC (flyover XOR
// SCION), de-aggregate it back to the plain SCION MAC and forward, advancing the
// path by a flyover hop (5 lines).
func hummingbirdFlyoverParentToChild(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	options := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

	// Construction direction: enter on parent 131, exit on child 141.
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
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    3, // second hop (first hop is a regular 3-line hop)
				SegLen:    [3]uint8{3 + 5 + 3, 0, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   1,
			NumLines: 3 + 5 + 3,
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

	scionL := &slayers.SCION{
		Version:      0,
		TrafficClass: 0xb8,
		FlowID:       0xdead,
		NextHdr:      slayers.L4UDP,
		PathType:     hummingbird.PathType,
		SrcIA:        addr.MustParseIA("1-ff00:0:3"),
		DstIA:        addr.MustParseIA("1-ff00:0:4"),
		Path:         dpath,
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost("172.16.3.1")); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("174.16.4.1")); err != nil {
		panic(err)
	}
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	// The aggregate MAC must be computed with the packet length the router sees.
	dpath.HopFields[1].HopField.Mac = hbirdAggregateMAC(
		mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[1], dpath.PathMeta)

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: forwarded to child 141; MAC de-aggregated to the SCION MAC; path
	// advanced by a flyover hop; SegID updated (construction direction).
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x14}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 14, 2}
	ip.DstIP = net.IP{192, 168, 14, 3}
	udp.SrcPort, udp.DstPort = udp.DstPort, udp.SrcPort
	dpath.HopFields[1].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	if err := dpath.IncPath(hummingbird.FlyoverLines); err != nil {
		panic(err)
	}
	dpath.InfoFields[0].UpdateSegID(dpath.HopFields[1].HopField.Mac)

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     "HummingbirdFlyoverParentToChild",
		WriteTo:  "veth_131_host",
		ReadFrom: "veth_141_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdFlyoverParentToChild"),
	}
}

// hummingbirdFlyoverInbound tests a Hummingbird packet with a flyover on the
// last (destination-AS) hop, arriving from a child and delivered to a local
// host. The router verifies the aggregate MAC, de-aggregates it and delivers
// the packet on the internal network. Analogue of ChildToInternalHost.
func hummingbirdFlyoverInbound(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	const endhostPort = 21000
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

	// Construction direction, last hop enters this AS on child 141 (ConsIngress).
	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    6, // third hop (two regular hops precede it)
				SegLen:    [3]uint8{3 + 3 + 5, 0, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   1,
			NumLines: 3 + 3 + 5,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 41, ConsEgress: 40}},
			{HopField: path.HopField{ConsIngress: 31, ConsEgress: 30}},
			{HopField: path.HopField{ConsIngress: 141, ConsEgress: 0},
				Flyover: true, ResID: 42, Bw: 129, ResStartTime: 5, Duration: 301},
		},
	}

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
	if err := scionL.SetSrcAddr(addr.MustParseHost("172.16.4.1")); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("192.168.0.51")); err != nil {
		panic(err)
	}
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	dpath.HopFields[2].HopField.Mac = hbirdAggregateMAC(
		mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[2], dpath.PathMeta)

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 2345
	scionudp.DstPort = uint16(endhostPort)
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: delivered to the local host 192.168.0.51 on the internal
	// interface; the current hop MAC is de-aggregated; the path is not advanced.
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x1}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 0, 11}
	ip.DstIP = net.IP{192, 168, 0, 51}
	udp.SrcPort, udp.DstPort = 30001, layers.UDPPort(scionudp.DstPort)
	dpath.HopFields[2].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[2].HopField, nil)

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     "HummingbirdFlyoverInbound",
		WriteTo:  "veth_141_host",
		ReadFrom: "veth_int_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdFlyoverInbound"),
	}
}

// hummingbirdFlyoverOutbound builds outbound flyover and demotion cases.
func hummingbirdFlyoverOutbound(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
	timestampOffset time.Duration,
	duration uint16,
	bw uint16,
	payload []byte,
	name string,
) runner.Case {
	options := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

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

	// First hop in construction direction: egress to child 141.
	now := time.Now().Add(timestampOffset)
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    0,
				SegLen:    [3]uint8{5 + 3, 0, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   1,
			NumLines: 5 + 3,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 141},
				Flyover: true, ResID: 42, Bw: bw, ResStartTime: 5, Duration: duration},
			{HopField: path.HopField{ConsIngress: 411, ConsEgress: 0}},
		},
	}

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
	if err := scionL.SetSrcAddr(addr.MustParseHost("192.168.0.51")); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("174.16.4.1")); err != nil {
		panic(err)
	}
	scionL.PayloadLen = uint16(8 + len(payload))

	dpath.HopFields[0].HopField.Mac = hbirdAggregateMAC(
		mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[0], dpath.PathMeta)

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: forwarded to child 141; MAC de-aggregated; path advanced by a
	// flyover hop; SegID updated (construction direction).
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x14}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 14, 2}
	ip.DstIP = net.IP{192, 168, 14, 3}
	udp.SrcPort, udp.DstPort = 50000, 40000
	dpath.HopFields[0].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[0].HopField, nil)
	if err := dpath.IncPath(hummingbird.FlyoverLines); err != nil {
		panic(err)
	}
	dpath.InfoFields[0].UpdateSegID(dpath.HopFields[0].HopField.Mac)

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     name,
		WriteTo:  "veth_int_host",
		ReadFrom: "veth_141_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, name),
	}
}

// hummingbirdBestEffortChildToChildXover tests a best-effort Hummingbird packet
// that crosses over from an up segment to a down segment on the same BR, from a
// child to another child. Analogue of ChildToChildXover; exercises the
// Hummingbird cross-over handling (doHbirdXoverBestEffort). The flyover
// cross-over variants are covered by HummingbirdFlyoverChildToChildXover and the
// HummingbirdFlyoverXoverASTransit{Ingress,Egress} cases below.
func hummingbirdBestEffortChildToChildXover(artifactsDir string, mac hash.Hash) runner.Case {
	options := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

	ethernet := &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef},
		DstMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x15},
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip := &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      64,
		SrcIP:    net.IP{192, 168, 15, 3},
		DstIP:    net.IP{192, 168, 15, 2},
		Protocol: layers.IPProtocolUDP,
		Flags:    layers.IPv4DontFragment,
	}
	udp := &layers.UDP{SrcPort: 40000, DstPort: 50000}
	_ = udp.SetNetworkLayerForChecksum(ip)

	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    3, // last hop of the up segment (the cross-over hop)
				CurrINF:   0,
				SegLen:    [3]uint8{6, 6, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   2,
			NumLines: 12,
		},
		InfoFields: []path.InfoField{
			// up segment (against construction direction)
			{SegID: 0x111, ConsDir: false, Timestamp: util.TimeToSecs(now)},
			// down segment (construction direction)
			{SegID: 0x222, ConsDir: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 511, ConsEgress: 0}},
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 151}},
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 141}},
			{HopField: path.HopField{ConsIngress: 411, ConsEgress: 0}},
		},
	}
	dpath.HopFields[1].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	dpath.InfoFields[0].UpdateSegID(dpath.HopFields[1].HopField.Mac)
	dpath.HopFields[2].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[1], dpath.HopFields[2].HopField, nil)

	scionL := &slayers.SCION{
		Version:      0,
		TrafficClass: 0xb8,
		FlowID:       0xdead,
		NextHdr:      slayers.L4UDP,
		PathType:     hummingbird.PathType,
		SrcIA:        addr.MustParseIA("1-ff00:0:5"),
		DstIA:        addr.MustParseIA("1-ff00:0:4"),
		Path:         dpath,
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost("172.16.5.1")); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("174.16.4.1")); err != nil {
		panic(err)
	}

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: forwarded to child 141 after switching to the down segment; both
	// SegIDs updated and the path advanced past the cross-over.
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x14}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 14, 2}
	ip.DstIP = net.IP{192, 168, 14, 3}
	udp.SrcPort, udp.DstPort = udp.DstPort, udp.SrcPort
	if err := dpath.IncPath(hummingbird.HopLines); err != nil {
		panic(err)
	}
	if err := dpath.IncPath(hummingbird.HopLines); err != nil {
		panic(err)
	}
	dpath.InfoFields[0].UpdateSegID(dpath.HopFields[1].HopField.Mac)
	dpath.InfoFields[1].UpdateSegID(dpath.HopFields[2].HopField.Mac)

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     "HummingbirdBestEffortChildToChildXover",
		WriteTo:  "veth_151_host",
		ReadFrom: "veth_141_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdBestEffortChildToChildXover"),
	}
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

// hummingbirdBestEffortInbound tests a best-effort (non-flyover) Hummingbird
// packet arriving from a child and delivered to a local host. Analogue of
// ChildToInternalHost; the counterpart of HummingbirdFlyoverInbound without the
// flyover (plain SCION MAC, no de-aggregation).
func hummingbirdBestEffortInbound(artifactsDir string, mac hash.Hash) runner.Case {
	const endhostPort = 21000
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

	// Construction direction, last hop enters this AS on child 141 (ConsIngress).
	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    6, // third hop (two regular hops precede it)
				SegLen:    [3]uint8{9, 0, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   1,
			NumLines: 9,
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
	dpath.HopFields[2].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[2].HopField, nil)

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
	if err := scionL.SetSrcAddr(addr.MustParseHost("172.16.4.1")); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("192.168.0.51")); err != nil {
		panic(err)
	}

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 2345
	scionudp.DstPort = uint16(endhostPort)
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: delivered to the local host on the internal interface; the path
	// and MAC are unchanged (best-effort, delivered without advancing).
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x1}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 0, 11}
	ip.DstIP = net.IP{192, 168, 0, 51}
	udp.SrcPort, udp.DstPort = 30001, layers.UDPPort(scionudp.DstPort)

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     "HummingbirdBestEffortInbound",
		WriteTo:  "veth_141_host",
		ReadFrom: "veth_int_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdBestEffortInbound"),
	}
}

// hummingbirdBestEffortOutbound tests a best-effort Hummingbird packet
// originating in this AS (first hop), sent out to a child. Analogue of
// InternalHostToChild; the counterpart of HummingbirdFlyoverOutbound without
// the flyover.
func hummingbirdBestEffortOutbound(artifactsDir string, mac hash.Hash) runner.Case {
	options := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

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

	// First hop in construction direction: egress to child 141.
	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    0,
				SegLen:    [3]uint8{6, 0, 0},
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
	dpath.HopFields[0].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[0].HopField, nil)

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
	if err := scionL.SetSrcAddr(addr.MustParseHost("192.168.0.51")); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("174.16.4.1")); err != nil {
		panic(err)
	}

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: forwarded to child 141; path advanced by a regular hop; SegID
	// updated (construction direction).
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x14}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 14, 2}
	ip.DstIP = net.IP{192, 168, 14, 3}
	udp.SrcPort, udp.DstPort = 50000, 40000
	if err := dpath.IncPath(hummingbird.HopLines); err != nil {
		panic(err)
	}
	dpath.InfoFields[0].UpdateSegID(dpath.HopFields[0].HopField.Mac)

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     "HummingbirdBestEffortOutbound",
		WriteTo:  "veth_int_host",
		ReadFrom: "veth_141_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdBestEffortOutbound"),
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
	current := hummingbird.FlyoverHopField{
		HopField: path.HopField{ConsIngress: 141, ConsEgress: 191},
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
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 411}},
			current,
			{HopField: path.HopField{ConsIngress: 911, ConsEgress: 0}},
		},
	}
	scionL := hbirdSCION(
		"1-ff00:0:4", "1-ff00:0:9", "172.16.4.1", "174.16.9.1", dpath)
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)
	if flyover {
		dpath.HopFields[1].HopField.Mac = hbirdAggregateMACForInterfaces(
			mac, sv, scionL, dpath, 141, 191, dpath.InfoFields[0], dpath.HopFields[1],
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

// hummingbirdFlyoverChildToParentNonConsDir tests transit of a Hummingbird
// packet with a flyover on the current hop, against the construction direction
// (child to parent). It complements HummingbirdFlyoverParentToChild (which is in
// construction direction) and exercises the non-consdir SegID handling together
// with flyover de-aggregation. See unit test "brtransit_non_consdir_flyover".
func hummingbirdFlyoverChildToParentNonConsDir(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
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

	// Against construction direction: enter on child 141, exit on parent 131.
	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    3,
				SegLen:    [3]uint8{3 + 5 + 3, 0, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   1,
			NumLines: 3 + 5 + 3,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: false, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 311}},
			{HopField: path.HopField{ConsIngress: 131, ConsEgress: 141},
				Flyover: true, ResID: 42, Bw: 129, ResStartTime: 5, Duration: 301},
			{HopField: path.HopField{ConsIngress: 411, ConsEgress: 0}},
		},
	}

	scionL := &slayers.SCION{
		Version:      0,
		TrafficClass: 0xb8,
		FlowID:       0xdead,
		NextHdr:      slayers.L4UDP,
		PathType:     hummingbird.PathType,
		SrcIA:        addr.MustParseIA("1-ff00:0:4"),
		DstIA:        addr.MustParseIA("1-ff00:0:3"),
		Path:         dpath,
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost("172.16.4.1")); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("174.16.3.1")); err != nil {
		panic(err)
	}
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	// The SCION hop MAC is computed with the base SegID; it is reused for both the
	// SegID update and the de-aggregated value, so it is never recomputed against a
	// mutated SegID (which would yield a different MAC).
	scionMac := path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	dpath.HopFields[1].HopField.Mac = hbirdAggregateMAC(
		mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[1], dpath.PathMeta)
	// Non-consdir: the ingress SegID is derived from the SCION MAC.
	dpath.InfoFields[0].UpdateSegID(scionMac)

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: forwarded to parent 131; MAC de-aggregated; path advanced by a
	// flyover hop. The router reverses the non-consdir SegID on ingress.
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x13}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 13, 2}
	ip.DstIP = net.IP{192, 168, 13, 3}
	udp.SrcPort, udp.DstPort = udp.DstPort, udp.SrcPort
	dpath.HopFields[1].HopField.Mac = scionMac
	// The router reverses the non-consdir SegID on ingress (XOR by the SCION MAC),
	// bringing it back to the base value.
	dpath.InfoFields[0].UpdateSegID(scionMac)
	if err := dpath.IncPath(hummingbird.FlyoverLines); err != nil {
		panic(err)
	}

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     "HummingbirdFlyoverChildToParentNonConsDir",
		WriteTo:  "veth_141_host",
		ReadFrom: "veth_131_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdFlyoverChildToParentNonConsDir"),
	}
}

// hummingbirdFlyoverChildToChildXover tests a Hummingbird cross-over (up→down
// segment) on the same BR, from a child to another child, with a flyover on the
// up-segment cross-over hop. Exercises doHbirdXoverFlyover in the external-egress
// branch; the unit-test analogue is "brtransit_xover_flyover" in
// router/dataplane_hbird_test.go. The reservation spans the ingress of the
// incoming hop (151) and the egress of the outgoing hop (141), so the flyover
// MAC uses those interfaces explicitly.
func hummingbirdFlyoverChildToChildXover(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	options := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

	ethernet := &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef},
		DstMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x15},
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip := &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      64,
		SrcIP:    net.IP{192, 168, 15, 3},
		DstIP:    net.IP{192, 168, 15, 2},
		Protocol: layers.IPProtocolUDP,
		Flags:    layers.IPv4DontFragment,
	}
	udp := &layers.UDP{SrcPort: 40000, DstPort: 50000}
	_ = udp.SetNetworkLayerForChecksum(ip)

	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    3, // up-seg cross-over hop (flyover, lines 3..7)
				CurrINF:   0,
				SegLen:    [3]uint8{3 + 5, 6, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   2,
			NumLines: 3 + 5 + 6,
		},
		InfoFields: []path.InfoField{
			// up segment (against construction direction)
			{SegID: 0x111, ConsDir: false, Timestamp: util.TimeToSecs(now)},
			// down segment (construction direction)
			{SegID: 0x222, ConsDir: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 511, ConsEgress: 0}},
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 151},
				Flyover: true, ResID: 42, Bw: 129, ResStartTime: 5, Duration: 301},
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 141}},
			{HopField: path.HopField{ConsIngress: 411, ConsEgress: 0}},
		},
	}

	scionL := &slayers.SCION{
		Version:      0,
		TrafficClass: 0xb8,
		FlowID:       0xdead,
		NextHdr:      slayers.L4UDP,
		PathType:     hummingbird.PathType,
		SrcIA:        addr.MustParseIA("1-ff00:0:5"),
		DstIA:        addr.MustParseIA("1-ff00:0:4"),
		Path:         dpath,
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost("172.16.5.1")); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("174.16.4.1")); err != nil {
		panic(err)
	}
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	// Up-segment hop MAC computed with the base SegID (reused, not recomputed).
	scionMac1 := path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	// Reservation spans ingress 151 (incoming hop) and egress 141 (outgoing hop).
	dpath.HopFields[1].HopField.Mac = hbirdAggregateMACForInterfaces(
		mac, sv, scionL, dpath, 151, 141, dpath.InfoFields[0], dpath.HopFields[1], dpath.PathMeta)
	dpath.InfoFields[0].UpdateSegID(scionMac1)
	dpath.HopFields[2].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[1], dpath.HopFields[2].HopField, nil)

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: forwarded to child 141 after switching to the down segment; the
	// up-seg flyover MAC is de-aggregated; both SegIDs updated; path advanced past
	// the flyover hop and the down-seg hop.
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x14}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 14, 2}
	ip.DstIP = net.IP{192, 168, 14, 3}
	udp.SrcPort, udp.DstPort = udp.DstPort, udp.SrcPort
	dpath.HopFields[1].HopField.Mac = scionMac1
	if err := dpath.IncPath(hummingbird.FlyoverLines); err != nil {
		panic(err)
	}
	if err := dpath.IncPath(hummingbird.HopLines); err != nil {
		panic(err)
	}
	dpath.InfoFields[0].UpdateSegID(scionMac1)
	dpath.InfoFields[1].UpdateSegID(dpath.HopFields[2].HopField.Mac)

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     "HummingbirdFlyoverChildToChildXover",
		WriteTo:  "veth_151_host",
		ReadFrom: "veth_141_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdFlyoverChildToChildXover"),
	}
}

// hummingbirdFlyoverXoverASTransitIngress tests the ingress BR of an AS-transit
// cross-over carrying a flyover: the packet arrives on an external child link,
// switches segments, and its egress interface belongs to a sibling BR, so it is
// forwarded internally. The flyover sits on the incoming (up-seg) hop and the
// router moves it to the outgoing hop for the egress BR (xoverMoveFlyoverToNext).
// Mirrors unit test "astransit_xover_ingress_flyover".
func hummingbirdFlyoverXoverASTransitIngress(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	options := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

	// Arrives on child 151.
	ethernet := &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef},
		DstMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x15},
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip := &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      64,
		SrcIP:    net.IP{192, 168, 15, 3},
		DstIP:    net.IP{192, 168, 15, 2},
		Protocol: layers.IPProtocolUDP,
		Flags:    layers.IPv4DontFragment,
	}
	udp := &layers.UDP{SrcPort: 40000, DstPort: 50000}
	_ = udp.SetNetworkLayerForChecksum(ip)

	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrINF:   0,
				CurrHF:    3, // up-seg cross-over hop (flyover)
				SegLen:    [3]uint8{3 + 5, 6, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   2,
			NumLines: 3 + 5 + 6,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: false, Timestamp: util.TimeToSecs(now)}, // up seg
			{SegID: 0x222, ConsDir: true, Timestamp: util.TimeToSecs(now)},  // down seg
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 511, ConsEgress: 0}},
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 151},
				Flyover: true, ResID: 42, Bw: 129, ResStartTime: 5, Duration: 301},
			// xover here (up->down shortcut); egress 181 is a child on sibling brC.
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 181}},
			{HopField: path.HopField{ConsIngress: 811, ConsEgress: 0}},
		},
	}

	scionL := &slayers.SCION{
		Version:      0,
		TrafficClass: 0xb8,
		FlowID:       0xdead,
		NextHdr:      slayers.L4UDP,
		PathType:     hummingbird.PathType,
		SrcIA:        addr.MustParseIA("1-ff00:0:5"),
		DstIA:        addr.MustParseIA("1-ff00:0:8"),
		Path:         dpath,
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost("172.16.5.1")); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("172.16.8.1")); err != nil {
		panic(err)
	}
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	// Reservation spans ingress 151 (incoming hop) and egress 181 (outgoing hop).
	scionMac1 := path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	dpath.HopFields[1].HopField.Mac = hbirdAggregateMACForInterfaces(
		mac, sv, scionL, dpath, 151, 181, dpath.InfoFields[0], dpath.HopFields[1], dpath.PathMeta)
	dpath.InfoFields[0].UpdateSegID(scionMac1)
	dpath.HopFields[2].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[1], dpath.HopFields[2].HopField, nil)

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: forwarded internally to sibling brC (192.168.0.13); the flyover is
	// moved from the up-seg hop to the core-seg hop and re-aggregated onto its MAC;
	// SegLens shift by 2 lines; the path advances by one regular hop.
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x1}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 0, 11}
	ip.DstIP = net.IP{192, 168, 0, 13}
	udp.SrcPort, udp.DstPort = 30001, 30003
	// De-aggregate the up-seg hop and move the flyover to the down-seg hop.
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
		mac, sv, scionL, dpath, 151, 181, dpath.InfoFields[1], dpath.HopFields[2], dpath.PathMeta)
	dpath.InfoFields[0].UpdateSegID(scionMac1)
	if err := dpath.IncPath(hummingbird.HopLines); err != nil {
		panic(err)
	}

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     "HummingbirdFlyoverXoverASTransitIngress",
		WriteTo:  "veth_151_host",
		ReadFrom: "veth_int_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdFlyoverXoverASTransitIngress"),
	}
}

// hummingbirdFlyoverXoverASTransitEgress tests the egress BR of an AS-transit
// cross-over carrying a flyover: the packet arrives internally from the sibling
// BR that handled the up segment, and this BR egresses it externally on a child.
// The flyover sits on the outgoing (down-seg) hop; the router de-aggregates it
// and moves it back to the incoming (up-seg) hop (xoverMoveFlyoverToPrevious).
// Mirrors unit test "astransit_xover_egress_flyover".
func hummingbirdFlyoverXoverASTransitEgress(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	options := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

	// Arrives internally from sibling brC (192.168.0.13).
	ethernet := &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef},
		DstMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x1},
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip := &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      64,
		SrcIP:    net.IP{192, 168, 0, 13},
		DstIP:    net.IP{192, 168, 0, 11},
		Protocol: layers.IPProtocolUDP,
		Flags:    layers.IPv4DontFragment,
	}
	udp := &layers.UDP{SrcPort: 30003, DstPort: 30001}
	_ = udp.SetNetworkLayerForChecksum(ip)

	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrINF:   1,
				CurrHF:    6, // down-seg cross-over hop (flyover), lines 6..10
				SegLen:    [3]uint8{6, 3 + 5, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   2,
			NumLines: 6 + 3 + 5,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: false, Timestamp: util.TimeToSecs(now)}, // up seg
			{SegID: 0x222, ConsDir: true, Timestamp: util.TimeToSecs(now)},  // down seg
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 511, ConsEgress: 0}},
			// up-seg hop; its real ingress is 181 (on sibling brC).
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 181}},
			// xover; down-seg hop, egress on this BR's child 141 (flyover, current).
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 141},
				Flyover: true, ResID: 42, Bw: 129, ResStartTime: 5, Duration: 301},
			{HopField: path.HopField{ConsIngress: 411, ConsEgress: 0}},
		},
	}

	scionL := &slayers.SCION{
		Version:      0,
		TrafficClass: 0xb8,
		FlowID:       0xdead,
		NextHdr:      slayers.L4UDP,
		PathType:     hummingbird.PathType,
		SrcIA:        addr.MustParseIA("1-ff00:0:5"),
		DstIA:        addr.MustParseIA("1-ff00:0:4"),
		Path:         dpath,
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost("172.16.5.1")); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("174.16.4.1")); err != nil {
		panic(err)
	}
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	// The up-seg hop keeps its plain SCION MAC (never verified or changed here).
	dpath.HopFields[1].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	// The down-seg flyover reservation spans ingress 181 (previous hop) and egress 141.
	scionMac2 := path.MAC(mac, dpath.InfoFields[1], dpath.HopFields[2].HopField, nil)
	dpath.HopFields[2].HopField.Mac = hbirdAggregateMACForInterfaces(
		mac, sv, scionL, dpath, 181, 141, dpath.InfoFields[1], dpath.HopFields[2], dpath.PathMeta)

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: egressed on child 141; the down-seg flyover is de-aggregated and
	// moved back to the up-seg hop; SegLens shift by 2 lines; the down-seg SegID is
	// updated (construction direction); the path advances past the flyover.
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x14}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 14, 2}
	ip.DstIP = net.IP{192, 168, 14, 3}
	udp.SrcPort, udp.DstPort = 50000, 40000
	// Move the flyover from the down-seg hop back to the up-seg hop; de-aggregate
	// the down-seg hop MAC. The up-seg hop keeps its (unchanged) MAC.
	dpath.HopFields[2].Flyover = false
	dpath.HopFields[2].HopField.Mac = scionMac2
	dpath.HopFields[1].Flyover = true
	dpath.HopFields[1].ResID = 42
	dpath.HopFields[1].Bw = 129
	dpath.HopFields[1].ResStartTime = 5
	dpath.HopFields[1].Duration = 301
	dpath.PathMeta.SegLen[0] += 2
	dpath.PathMeta.SegLen[1] -= 2
	dpath.InfoFields[1].UpdateSegID(scionMac2)
	if err := dpath.IncPath(hummingbird.FlyoverLines); err != nil {
		panic(err)
	}

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     "HummingbirdFlyoverXoverASTransitEgress",
		WriteTo:  "veth_int_host",
		ReadFrom: "veth_141_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdFlyoverXoverASTransitEgress"),
	}
}

// hummingbirdFlyoverChildToPeer tests a Hummingbird packet with a flyover on a
// peering hop, entering on a child link and leaving on a peering link from the
// same BR (against construction direction). Analogue of ChildToPeer; mirrors
// unit test "brtransit_peering_non_consdir_flyover". At a peering hop the SegID
// is not updated and the reservation interfaces are the plain (swapped) hop
// interfaces.
func hummingbirdFlyoverChildToPeer(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	options := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

	// Injected at A's child 151 as if coming from AS 5.
	ethernet := &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef},
		DstMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x15},
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip := &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      64,
		SrcIP:    net.IP{192, 168, 15, 3},
		DstIP:    net.IP{192, 168, 15, 2},
		Protocol: layers.IPProtocolUDP,
		Flags:    layers.IPv4DontFragment,
	}
	udp := &layers.UDP{SrcPort: 40000, DstPort: 50000}
	_ = udp.SetNetworkLayerForChecksum(ip)

	// Up seg ends at the peering hop (HF[1]); a one-hop down seg follows.
	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    3, // peering hop (flyover), lines 3..7
				CurrINF:   0,
				SegLen:    [3]uint8{3 + 5, 3, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   2,
			NumLines: 3 + 5 + 3,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: false, Peer: true, Timestamp: util.TimeToSecs(now)},
			{SegID: 0x222, ConsDir: true, Peer: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 511, ConsEgress: 0}}, // at AS 5
			{HopField: path.HopField{ConsIngress: 121, ConsEgress: 151}, // peering hop at A
				Flyover: true, ResID: 42, Bw: 129, ResStartTime: 5, Duration: 301},
			{HopField: path.HopField{ConsIngress: 211, ConsEgress: 0}}, // at AS 2
		},
	}

	scionL := &slayers.SCION{
		Version:      0,
		TrafficClass: 0xb8,
		FlowID:       0xdead,
		NextHdr:      slayers.L4UDP,
		PathType:     hummingbird.PathType,
		SrcIA:        addr.MustParseIA("1-ff00:0:5"),
		DstIA:        addr.MustParseIA("1-ff00:0:2"),
		Path:         dpath,
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost("172.16.5.1")); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("174.16.2.1")); err != nil {
		panic(err)
	}
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	// HF[0] and HF[2] are signed by other ASes and never checked here.
	dpath.HopFields[0].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[0].HopField, nil)
	dpath.HopFields[2].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[1], dpath.HopFields[2].HopField, nil)
	// The peering-hop MAC uses the up-seg info; reservation spans ingress 151,
	// egress 121 (the plain non-consdir swap; peering skips the xover adjustment).
	scionMac1 := path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	dpath.HopFields[1].HopField.Mac = hbirdAggregateMAC(
		mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[1], dpath.PathMeta)

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: forwarded out the peering link 121; the peering-hop MAC is
	// de-aggregated; the path crosses into the down segment; SegID is NOT updated.
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x12}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 12, 2}
	ip.DstIP = net.IP{192, 168, 12, 3}
	udp.SrcPort, udp.DstPort = udp.DstPort, udp.SrcPort
	dpath.HopFields[1].HopField.Mac = scionMac1
	if err := dpath.IncPath(hummingbird.FlyoverLines); err != nil {
		panic(err)
	}

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     "HummingbirdFlyoverChildToPeer",
		WriteTo:  "veth_151_host",
		ReadFrom: "veth_121_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdFlyoverChildToPeer"),
	}
}

// hummingbirdFlyoverPeerToChild tests a Hummingbird packet with a flyover on a
// peering hop, entering on a peering link and leaving on a child link (in
// construction direction). Analogue of PeerToChild; mirrors unit test
// "brtransit_peering_consdir_flyover". The peering hop is the first hop of the down
// segment; SegID is not updated.
func hummingbirdFlyoverPeerToChild(
	artifactsDir string,
	mac hash.Hash,
	sv []byte,
) runner.Case {
	options := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

	// Injected at A's peering 121 as if coming from AS 2.
	ethernet := &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef},
		DstMAC:       net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x12},
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip := &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      64,
		SrcIP:    net.IP{192, 168, 12, 3},
		DstIP:    net.IP{192, 168, 12, 2},
		Protocol: layers.IPProtocolUDP,
		Flags:    layers.IPv4DontFragment,
	}
	udp := &layers.UDP{SrcPort: 40000, DstPort: 50000}
	_ = udp.SetNetworkLayerForChecksum(ip)

	// One-hop up seg; the peering hop (HF[1]) is the first hop of the down seg.
	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    3, // peering hop (flyover), lines 3..7
				CurrINF:   1,
				SegLen:    [3]uint8{3, 3 + 5, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   2,
			NumLines: 3 + 3 + 5,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: false, Timestamp: util.TimeToSecs(now)},
			{SegID: 0x222, ConsDir: true, Peer: true, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 211, ConsEgress: 0}}, // at AS 2
			{HopField: path.HopField{ConsIngress: 121, ConsEgress: 151}, // peering hop at A
				Flyover: true, ResID: 42, Bw: 129, ResStartTime: 5, Duration: 301},
			{HopField: path.HopField{ConsIngress: 511, ConsEgress: 0}}, // at AS 5
		},
	}

	scionL := &slayers.SCION{
		Version:      0,
		TrafficClass: 0xb8,
		FlowID:       0xdead,
		NextHdr:      slayers.L4UDP,
		PathType:     hummingbird.PathType,
		SrcIA:        addr.MustParseIA("1-ff00:0:2"),
		DstIA:        addr.MustParseIA("1-ff00:0:5"),
		Path:         dpath,
	}
	if err := scionL.SetSrcAddr(addr.MustParseHost("172.16.2.1")); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("174.16.5.1")); err != nil {
		panic(err)
	}
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	dpath.HopFields[0].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[0].HopField, nil)
	dpath.HopFields[2].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[1], dpath.HopFields[2].HopField, nil)
	// The peering-hop MAC uses the down-seg info; reservation spans ingress 121,
	// egress 151 (consdir, no swap).
	scionMac1 := path.MAC(mac, dpath.InfoFields[1], dpath.HopFields[1].HopField, nil)
	dpath.HopFields[1].HopField.Mac = hbirdAggregateMAC(
		mac, sv, scionL, dpath, dpath.InfoFields[1], dpath.HopFields[1], dpath.PathMeta)

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	input := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(input, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	// Expected: forwarded out the child link 151; the peering-hop MAC is
	// de-aggregated; the path advances past the flyover; SegID is NOT updated.
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x15}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 15, 2}
	ip.DstIP = net.IP{192, 168, 15, 3}
	udp.SrcPort, udp.DstPort = udp.DstPort, udp.SrcPort
	dpath.HopFields[1].HopField.Mac = scionMac1
	if err := dpath.IncPath(hummingbird.FlyoverLines); err != nil {
		panic(err)
	}

	if err := gopacket.SerializeLayers(want, options,
		ethernet, ip, udp, scionL, scionudp, gopacket.Payload(payload),
	); err != nil {
		panic(err)
	}

	return runner.Case{
		Name:     "HummingbirdFlyoverPeerToChild",
		WriteTo:  "veth_121_host",
		ReadFrom: "veth_151_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdFlyoverPeerToChild"),
	}
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
