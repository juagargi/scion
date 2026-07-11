// Copyright 2025 SCION Association
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

// This file contains the Hummingbird (path type 5) border-router acceptance
// cases. They are the wire-level analogue of the unit tests in
// router/dataplane_hbird_test.go: a crafted Hummingbird packet is injected on
// one veth and the emitted packet is compared byte-for-byte against the
// expected result.
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
// The router derives its Hummingbird secret value unconditionally from the AS
// master key (router/control/conf.go). The cases receive that secret value
// (sv) from main.go via loadHbirdSV and use it to build the flyover MAC, so no
// router configuration change is required.
//
// The flyover MAC helpers below are ports of computeAggregateMac /
// packetLenFromRouterView from router/dataplane_hbird_test.go. They must stay
// in sync with the router's MAC computation.

const hbirdPayload = "actualpayloadbytes"

// hbirdScionUDPPayloadLen is the SCION/UDP header (8B) plus the payload. It is
// used to fix the SCION PayloadLen before computing the flyover MAC, which
// depends on the total packet length as seen by the router.
const hbirdScionUDPPayloadLen = 8 + len(hbirdPayload)

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
	scionMac := path.MAC(mac, info, hf.HopField, nil)

	block, err := aes.NewCipher(sv)
	if err != nil {
		panic(err)
	}
	// Reservations are made in construction direction; against it, ingress and
	// egress are swapped relative to the hop field.
	ingress, egress := hf.HopField.ConsIngress, hf.HopField.ConsEgress
	if !info.ConsDir {
		ingress, egress = egress, ingress
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

// HummingbirdBestEffortChildToParent tests transit of a best-effort (non-flyover)
// Hummingbird packet over the same BR host, from a child to a parent. It is the
// Hummingbird analogue of ChildToParent and proves the router decodes and
// forwards the Hummingbird wire format and verifies the plain SCION hop MAC.
func HummingbirdBestEffortChildToParent(artifactsDir string, mac hash.Hash) runner.Case {
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

	// Up segment (against construction direction): enter on child 141, exit on
	// parent 131. Current hop is the middle hop (line 3).
	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    3,
				SegLen:    [3]uint8{9, 0, 0},
				BaseTS:    util.TimeToSecs(now),
				HighResTS: 500 << 22,
			},
			NumINF:   1,
			NumLines: 9,
		},
		InfoFields: []path.InfoField{
			{SegID: 0x111, ConsDir: false, Timestamp: util.TimeToSecs(now)},
		},
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: path.HopField{ConsIngress: 411, ConsEgress: 0}},
			{HopField: path.HopField{ConsIngress: 131, ConsEgress: 141}},
			{HopField: path.HopField{ConsIngress: 0, ConsEgress: 311}},
		},
	}
	dpath.HopFields[1].HopField.Mac =
		path.MAC(mac, dpath.InfoFields[0], dpath.HopFields[1].HopField, nil)
	dpath.InfoFields[0].UpdateSegID(dpath.HopFields[1].HopField.Mac)

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

	// Expected: forwarded to parent 131, path advanced by one regular hop.
	want := gopacket.NewSerializeBuffer()
	ethernet.SrcMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0x00, 0x13}
	ethernet.DstMAC = net.HardwareAddr{0xf0, 0x0d, 0xca, 0xfe, 0xbe, 0xef}
	ip.SrcIP = net.IP{192, 168, 13, 2}
	ip.DstIP = net.IP{192, 168, 13, 3}
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
		Name:     "HummingbirdBestEffortChildToParent",
		WriteTo:  "veth_141_host",
		ReadFrom: "veth_131_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdBestEffortChildToParent"),
	}
}

// HummingbirdFlyoverChildToParent tests transit of a Hummingbird packet whose
// current hop carries a flyover (reservation), in construction direction from a
// parent to a child. The router must verify the aggregate MAC (flyover XOR
// SCION), de-aggregate it back to the plain SCION MAC and forward, advancing the
// path by a flyover hop (5 lines).
func HummingbirdFlyoverChildToParent(
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
		Name:     "HummingbirdFlyoverChildToParent",
		WriteTo:  "veth_131_host",
		ReadFrom: "veth_141_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdFlyoverChildToParent"),
	}
}

// HummingbirdFlyoverInbound tests a Hummingbird packet with a flyover on the
// last (destination-AS) hop, arriving from a child and delivered to a local
// host. The router verifies the aggregate MAC, de-aggregates it and delivers
// the packet on the internal network. Analogue of ChildToInternalHost.
func HummingbirdFlyoverInbound(
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

// HummingbirdFlyoverOutbound tests a Hummingbird packet originating in this AS
// (first hop) with a flyover on the current hop, sent out to a child. The router
// verifies the aggregate MAC, de-aggregates it, advances the path by a flyover
// hop and forwards on the child link. Analogue of InternalHostToChild.
func HummingbirdFlyoverOutbound(
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
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	dpath.HopFields[0].HopField.Mac = hbirdAggregateMAC(
		mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[0], dpath.PathMeta)

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
		Name:     "HummingbirdFlyoverOutbound",
		WriteTo:  "veth_int_host",
		ReadFrom: "veth_141_host",
		Input:    input.Bytes(),
		Want:     want.Bytes(),
		StoreDir: filepath.Join(artifactsDir, "HummingbirdFlyoverOutbound"),
	}
}

// HummingbirdBestEffortChildToChildXover tests a best-effort Hummingbird packet
// that crosses over from an up segment to a down segment on the same BR, from a
// child to another child. Analogue of ChildToChildXover; exercises the
// Hummingbird cross-over handling (doHbirdXoverBestEffort).
//
// The flyover cross-over variants (which move the flyover between hops) are
// covered by the unit tests "astransit xover flyover ingress/egress" in
// router/dataplane_hbird_test.go.
func HummingbirdBestEffortChildToChildXover(artifactsDir string, mac hash.Hash) runner.Case {
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

// HummingbirdBadFlyoverMAC tests that a Hummingbird packet with a corrupted
// flyover aggregate MAC on an inbound hop triggers an SCMP ParameterProblem
// (InvalidHopFieldMAC) back to the source over a (flyover-stripped) Hummingbird
// path. Analogue of SCMPBadMAC. The router_multi configuration enables
// experimental SCMP authentication, so the reply carries an SPAO option that is
// normalized before comparison.
func HummingbirdBadFlyoverMAC(
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

	now := time.Now()
	dpath := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrHF:    6,
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
	if err := scionL.SetSrcAddr(srcA); err != nil {
		panic(err)
	}
	if err := scionL.SetDstAddr(addr.MustParseHost("192.168.0.51")); err != nil {
		panic(err)
	}
	scionL.PayloadLen = uint16(hbirdScionUDPPayloadLen)

	dpath.HopFields[2].HopField.Mac = hbirdAggregateMAC(
		mac, sv, scionL, dpath, dpath.InfoFields[0], dpath.HopFields[2], dpath.PathMeta)
	// Corrupt the aggregate MAC so verification fails.
	dpath.HopFields[2].HopField.Mac[0] ^= 0xff

	scionudp := &slayers.UDP{}
	scionudp.SrcPort = 40111
	scionudp.DstPort = 40222
	scionudp.SetNetworkLayerForChecksum(scionL)

	payload := []byte(hbirdPayload)

	// Pointer to the current Hummingbird hop line in the offending packet.
	pointer := slayers.CmnHdrLen + scionL.AddrHdrLen() +
		(hummingbird.MetaLen + path.InfoLen*dpath.NumINF +
			int(dpath.PathMeta.CurrHF)*hummingbird.LineLen)

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
			slayers.SCMPCodeInvalidHopFieldMAC),
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
		Name:            "HummingbirdBadFlyoverMAC",
		WriteTo:         "veth_141_host",
		ReadFrom:        "veth_141_host",
		Input:           input.Bytes(),
		Want:            want.Bytes(),
		StoreDir:        filepath.Join(artifactsDir, "HummingbirdBadFlyoverMAC"),
		NormalizePacket: scmpNormalizePacket,
	}
}
