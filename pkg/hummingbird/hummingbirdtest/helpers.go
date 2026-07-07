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

// Package hummingbirdtest contains shared helpers for Hummingbird live tests
// and the matching acceptance test.

package hummingbirdtest

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/tls"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/quic-go/quic-go"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/daemon"
	"github.com/scionproto/scion/pkg/daemon/types"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/slayers"
	hummlib "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	dpscion "github.com/scionproto/scion/pkg/slayers/path/scion"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
	"github.com/scionproto/scion/private/keyconf"
)

const (
	// HbirdTestResID is the synthetic reservation ID used by the tests.
	HbirdTestResID = uint32(1)
	// HbirdTestBandwidth is the synthetic bandwidth class used by the tests.
	HbirdTestBandwidth = uint16(1024 - 1)
	// HbirdTestDuration is the reservation lifetime in seconds.
	HbirdTestDuration = uint16(9)
	// HbirdTestStartOffset backdates the reservation slightly so it is already
	// valid when the client sends the first packet.
	HbirdTestStartOffset = -2 * time.Second
	// QUICTestMessageSize keeps the client payload large enough to span multiple
	// packets in the live path.
	QUICTestMessageSize = 20 * 1024
	// QUICTestMessageReply is the fixed server response for the round trip.
	QUICTestMessageReply = "pong over scion"
	// PacketTestMessageSize keeps the packet payload comfortably below the tiny
	// topology MTU while still exercising non-trivial Hummingbird traffic.
	PacketTestMessageSize = 1024
	// PacketTestMessageReply is the fixed datagram reply for the round trip.
	PacketTestMessageReply = "pong over hummingbird"
)

// ReservationParams configures synthetic Hummingbird reservation values used by
// live tests.
type ReservationParams struct {
	ResID       uint32
	Bandwidth   uint16
	Duration    uint16
	StartOffset time.Duration
}

// DefaultReservationParams returns the baseline reservation used by existing
// Hummingbird live tests.
func DefaultReservationParams() ReservationParams {
	return ReservationParams{
		ResID:       HbirdTestResID,
		Bandwidth:   HbirdTestBandwidth,
		Duration:    HbirdTestDuration,
		StartOffset: HbirdTestStartOffset,
	}
}

// QUICTestMessageClient is the fixed client payload used by the round-trip
// tests. It is kept at or above 20 KiB so the exchange exercises multiple
// packets instead of succeeding on only a few packets.
var QUICTestMessageClient = bytes.Repeat([]byte("ping over hummingbird|"), 1024)[:QUICTestMessageSize]

// PacketTestMessageClient is the fixed datagram payload used by the packet
// round-trip tests.
// var PacketTestMessageClient = bytes.Repeat([]byte("packet over hummingbird|"), 64)[:PacketTestMessageSize]
var PacketTestMessageClient = ([]byte)("packet over hummingbird")

//go:embed tls.pem
var tlsPEM []byte

//go:embed tls.key
var tlsKey []byte

// Logger matches testing-style logging functions such as t.Logf and log.Printf.
type Logger func(string, ...any)

// FixedReplyPather always returns a preselected dataplane reply path.
type FixedReplyPather struct {
	Path snet.DataplanePath
}

// ReplyPath implements snet.ReplyPather.
func (p FixedReplyPather) ReplyPath(snet.RawPath) (snet.DataplanePath, error) {
	return p.Path, nil
}

type ignoreSCMP struct{}

func (ignoreSCMP) Handle(*snet.Packet) error {
	return nil
}

// NewTLSConfig returns the embedded test-only TLS configuration used by both
// the live test and the acceptance helper binary.
func NewTLSConfig() (*tls.Config, error) {
	cert, err := tls.X509KeyPair(tlsPEM, tlsKey)
	if err != nil {
		return nil, serrors.Wrap("loading embedded test certificate", err)
	}
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
		NextProtos:         []string{"SCION"},
	}, nil
}

// FindTinyTopologyAssets verifies that root contains the generated tiny-topology
// files needed to derive Hummingbird reservation keys.
func FindTinyTopologyAssets(root string) (string, error) {
	if HasTinyTopologyAssets(root) {
		return root, nil
	}
	return "", serrors.New("tiny topology assets not found", "root", root)
}

// HasTinyTopologyAssets reports whether root contains the minimum generated
// tiny-topology files required by the Hummingbird tests.
func HasTinyTopologyAssets(root string) bool {
	required := []string{
		filepath.Join(root, "ASff00_0_110", "keys", "master0.key"),
		filepath.Join(root, "ASff00_0_111", "keys", "master0.key"),
		filepath.Join(root, "ASff00_0_112", "keys", "master0.key"),
		filepath.Join(root, "ASff00_0_111", "topology.json"),
		filepath.Join(root, "ASff00_0_112", "topology.json"),
	}
	for _, path := range required {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

// ConnectDaemon establishes a daemon connector and verifies that it is usable.
func ConnectDaemon(ctx context.Context, daemonAddr string) (daemon.Connector, error) {
	conn, err := daemon.NewService(daemonAddr).Connect(ctx)
	if err != nil {
		return nil, serrors.Wrap("connecting to daemon", err, "addr", daemonAddr)
	}
	if _, err := conn.LocalIA(ctx); err != nil {
		_ = conn.Close()
		return nil, serrors.Wrap("probing daemon", err, "addr", daemonAddr)
	}
	return conn, nil
}

// BasePath looks up a baseline SCION path between srcIA and dstIA and returns
// the first result after validating the tiny-topology expectations used by the
// tests.
func BasePath(
	ctx context.Context,
	conn daemon.Connector,
	srcIA, dstIA addr.IA,
	log Logger,
) (snet.Path, error) {
	paths, err := conn.Paths(ctx, dstIA, srcIA, types.PathReqFlags{})
	if err != nil {
		return nil, serrors.Wrap("looking up paths", err, "src", srcIA, "dst", dstIA)
	}
	if len(paths) == 0 {
		return nil, serrors.New("no paths available", "src", srcIA, "dst", dstIA)
	}
	path := paths[0]
	meta := path.Metadata()
	if meta == nil {
		return nil, serrors.New("selected path metadata is nil", "src", srcIA, "dst", dstIA)
	}
	if len(meta.Interfaces) != 4 {
		return nil, serrors.New("unexpected interface count",
			"src", srcIA, "dst", dstIA, "count", len(meta.Interfaces))
	}
	if log != nil {
		log("selected base path %s -> %s: %v", srcIA, dstIA, formatInterfaces(meta.Interfaces))
	}
	return path, nil
}

// NewSCIONConn creates a listening SCION UDP connection with the test-specific
// SCMP handling required by the Hummingbird client and server.
func NewSCIONConn(
	ctx context.Context,
	topology snet.Topology,
	local *net.UDPAddr,
	replyPather snet.ReplyPather,
	ignoreServerSCMP bool,
) (*snet.Conn, error) {
	var handler snet.SCMPHandler = snet.SCMPPropagationStopper{
		Handler: ignoreSCMP{},
		Log: func(string, ...any) {
		},
	}
	if ignoreServerSCMP {
		// The one-shot test server should not fail just because the network emits
		// an SCMP packet while the client is still establishing the flow.
		handler = ignoreSCMP{}
	}
	network := &snet.SCIONNetwork{
		Topology:    topology,
		ReplyPather: replyPather,
		SCMPHandler: handler,
	}
	conn, err := network.Listen(ctx, "udp", local)
	if err != nil {
		return nil, serrors.Wrap("listening on scion network", err, "local", local)
	}
	return conn, nil
}

// BuildHummingbirdRemote turns a plain remote address into one that carries a
// Hummingbird reservation path and the matching next hop.
func BuildHummingbirdRemote(
	ctx context.Context,
	conn daemon.Connector,
	clientLocal *snet.UDPAddr,
	serverRemote *snet.UDPAddr,
	keysRoot string,
	log Logger,
) (*snet.UDPAddr, error) {
	return BuildHummingbirdRemoteWithParams(
		ctx,
		conn,
		clientLocal,
		serverRemote,
		keysRoot,
		DefaultReservationParams(),
		log,
	)
}

// BuildHummingbirdRemoteWithParams turns a plain remote address into one that
// carries a Hummingbird reservation path and the matching next hop with
// caller-provided reservation parameters.
func BuildHummingbirdRemoteWithParams(
	ctx context.Context,
	conn daemon.Connector,
	clientLocal *snet.UDPAddr,
	serverRemote *snet.UDPAddr,
	keysRoot string,
	params ReservationParams,
	log Logger,
) (*snet.UDPAddr, error) {
	basePath, err := BasePath(ctx, conn, clientLocal.IA, serverRemote.IA, log)
	if err != nil {
		return nil, err
	}
	return BuildHummingbirdRemoteWithPath(basePath, serverRemote, keysRoot, params, log)
}

// BuildHummingbirdRemoteWithPath turns a plain remote address into one that
// carries a Hummingbird reservation over the provided base path.
func BuildHummingbirdRemoteWithPath(
	basePath snet.Path,
	serverRemote *snet.UDPAddr,
	keysRoot string,
	params ReservationParams,
	log Logger,
) (*snet.UDPAddr, error) {
	now := time.Now()
	reservation, err := NewHummingbirdReservationWithParams(basePath, keysRoot, now, params, log)
	if err != nil {
		return nil, err
	}
	res, ok := reservation.(*snetpath.Reservation)
	if !ok {
		return nil, serrors.New("unexpected reservation path type", "type", reflect.TypeOf(reservation))
	}
	if err := ValidateReservationWindow(res, now); err != nil {
		return nil, err
	}

	remote := serverRemote.Copy()
	remote.Path = reservation
	remote.NextHop = basePath.UnderlayNextHop()
	if log != nil {
		log("hummingbird reservation path type: %s", reflect.TypeOf(remote.Path))
		log("hummingbird next hop: %v", remote.NextHop)
	}
	return remote, nil
}

// NewHummingbirdReservationWithParams derives one flyover reservation per hop
// using caller-provided reservation parameters and wraps them into a reservation
// dataplane path.
func NewHummingbirdReservationWithParams(
	basePath snet.Path,
	keysRoot string,
	now time.Time,
	params ReservationParams,
	log Logger,
) (snet.DataplanePath, error) {
	baseHops := snetpath.InterfacesToBaseHops(basePath.Metadata().Interfaces)
	if len(baseHops) == 0 {
		return nil, serrors.New("base path does not contain any hops")
	}

	// Convert the path to a scion raw path.
	scionPath, ok := basePath.Dataplane().(snetpath.SCION)
	if !ok {
		return nil, serrors.New("provided path must be of type scion")
	}

	return newHummingbirdReservationFromBaseHops(
		scionPath,
		basePath.Destination(),
		baseHops,
		keysRoot,
		now,
		params,
		log,
	)
}

func newHummingbirdReservationFromBaseHops(
	scionPath snetpath.SCION,
	dstIA addr.IA,
	baseHops []snetpath.BaseHop,
	keysRoot string,
	now time.Time,
	params ReservationParams,
	log Logger,
) (*snetpath.Reservation, error) {
	if len(baseHops) == 0 {
		return nil, serrors.New("base path does not contain any hops")
	}

	startTime := uint32(now.Add(params.StartOffset).Unix())
	aesByIA := make(map[addr.IA]cipher.Block)
	buffer := make([]byte, hummlib.AkBufferSize)
	flyovers := make([]*snetpath.Hop, 0, len(baseHops))
	for _, baseHop := range baseHops {
		block, ok := aesByIA[baseHop.IA]
		if !ok {
			sv, err := SecretValue(keysRoot, baseHop.IA, log)
			if err != nil {
				return nil, err
			}
			block, err = aes.NewCipher(sv)
			if err != nil {
				return nil, serrors.Wrap("creating aes cipher", err, "ia", baseHop.IA)
			}
			aesByIA[baseHop.IA] = block
		}
		// The test derives the exact AK each AS would expect for this synthetic
		// reservation so the routers can validate every flyover hop.
		akRaw := hummlib.DeriveAuthKey(
			block,
			params.ResID,
			params.Bandwidth,
			baseHop.Ingress,
			baseHop.Egress,
			startTime,
			params.Duration,
			buffer,
		)
		var ak [hummlib.AkBufferSize]byte
		copy(ak[:], akRaw)
		if log != nil {
			log("reservation inputs ia=%s in=%d eg=%d res_id=%d bw=%d start=%d dur=%d ak=%s",
				baseHop.IA, baseHop.Ingress, baseHop.Egress, params.ResID,
				params.Bandwidth, startTime, params.Duration, hex.EncodeToString(ak[:]))
		}
		flyovers = append(flyovers, &snetpath.Hop{
			BaseHop: baseHop,
			Flyover: &snetpath.FlyoverData{
				ResID:     params.ResID,
				Ak:        ak,
				Bw:        params.Bandwidth,
				StartTime: startTime,
				Duration:  params.Duration,
			},
		})
	}

	reservation, err := snetpath.NewReservation(
		snetpath.WithNow(func() time.Time { return now }),
		snetpath.WithDataplanePath(scionPath, dstIA, flyovers),
	)
	if err != nil {
		return nil, serrors.Wrap("building reservation path", err)
	}
	return reservation, nil
}

func serializedReverseReservationState(
	basePath snet.Path,
	keysRoot string,
	now time.Time,
	params ReservationParams,
	log Logger,
) ([]byte, error) {
	scionPath, ok := basePath.Dataplane().(snetpath.SCION)
	if !ok {
		return nil, serrors.New("provided path must be of type scion")
	}
	reverseScionPath, err := reverseSCIONPath(scionPath)
	if err != nil {
		return nil, err
	}
	reverseHops := reverseBaseHops(snetpath.InterfacesToBaseHops(basePath.Metadata().Interfaces))
	reservation, err := newHummingbirdReservationFromBaseHops(
		reverseScionPath,
		basePath.Source(),
		reverseHops,
		keysRoot,
		now,
		params,
		log,
	)
	if err != nil {
		return nil, err
	}
	state := make([]byte, reservation.SerializedLen())
	if err := reservation.Serialize(state); err != nil {
		return nil, err
	}
	return state, nil
}

func reverseSCIONPath(scionPath snetpath.SCION) (snetpath.SCION, error) {
	var dec dpscion.Decoded
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
		return snetpath.SCION{}, serrors.New("unexpected reversed path type", "type", reflect.TypeOf(reversed))
	}
	return snetpath.NewSCIONFromDecoded(*reversedDecoded)
}

func reverseBaseHops(hops []snetpath.BaseHop) []snetpath.BaseHop {
	reversed := make([]snetpath.BaseHop, len(hops))
	for i, hop := range hops {
		reversed[len(hops)-1-i] = snetpath.BaseHop{
			IA:      hop.IA,
			Ingress: hop.Egress,
			Egress:  hop.Ingress,
		}
	}
	return reversed
}

// SecretValue loads the AS master key from keysRoot and derives the
// Hummingbird secret value used for AK derivation.
func SecretValue(keysRoot string, ia addr.IA, log Logger) ([]byte, error) {
	keysDir := filepath.Join(
		keysRoot,
		addr.FormatAS(ia.AS(), addr.WithDefaultPrefix(), addr.WithFileSeparator()),
		"keys",
	)
	master, err := keyconf.LoadMaster(keysDir)
	if err != nil {
		return nil, serrors.Wrap("loading master key", err, "dir", keysDir, "ia", ia)
	}
	sv := hummlib.DeriveSecretValue(master.Key0)
	if log != nil {
		log("secret value ia=%s dir=%s sv=%s", ia, keysDir, hex.EncodeToString(sv))
	}
	return sv, nil
}

// ValidateReservationWindow checks that all flyover reservations are currently
// valid at the given time.
func ValidateReservationWindow(reservation *snetpath.Reservation, now time.Time) error {
	for _, hop := range reservation.Hops {
		if hop == nil || hop.Flyover == nil {
			continue
		}
		start := time.Unix(int64(hop.Flyover.StartTime), 0)
		end := start.Add(time.Duration(hop.Flyover.Duration) * time.Second)
		if now.Before(start) {
			return serrors.New("reservation not yet valid", "ia", hop.IA, "start", start, "now", now)
		}
		if now.After(end) {
			return serrors.New("reservation already expired", "ia", hop.IA, "end", end, "now", now)
		}
	}
	return nil
}

// RunQUICServerOnce accepts one QUIC connection, validates the fixed client
// payload, replies once, and then returns.
func RunQUICServerOnce(ctx context.Context, listener *quic.Listener) error {
	conn, err := listener.Accept(ctx)
	if err != nil {
		return serrors.Wrap("accepting quic connection", err)
	}
	defer conn.CloseWithError(0, "")

	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		return serrors.Wrap("accepting quic stream", err)
	}

	buf := make([]byte, len(QUICTestMessageClient))
	n, err := io.ReadFull(stream, buf)
	if err != nil {
		return serrors.Wrap("reading client payload", err)
	}
	if !bytes.Equal(buf[:n], QUICTestMessageClient) {
		return serrors.New("unexpected client payload")
	}
	if _, err := stream.Write([]byte(QUICTestMessageReply)); err != nil {
		return serrors.Wrap("writing server payload", err)
	}
	if err := stream.Close(); err != nil {
		return serrors.Wrap("closing server stream", err)
	}
	// Give quic-go a moment to flush the reply before this short-lived helper
	// process exits. Without this, the detached acceptance helper can win a race
	// against the transport and the client may time out waiting for the reply.
	time.Sleep(500 * time.Millisecond)
	return nil
}

// RunQUICClientRoundTrip opens one QUIC stream to remote, sends the fixed test
// payload, and verifies the fixed reply.
func RunQUICClientRoundTrip(
	ctx context.Context,
	clientConn *snet.Conn,
	remote *snet.UDPAddr,
	tlsConfig *tls.Config,
) error {
	clientTransport := &quic.Transport{Conn: clientConn}
	defer clientTransport.Close()

	session, err := clientTransport.Dial(ctx, remote, tlsConfig, nil)
	if err != nil {
		return serrors.Wrap("dialing quic session", err)
	}
	defer session.CloseWithError(0, "")

	stream, err := session.OpenStreamSync(ctx)
	if err != nil {
		return serrors.Wrap("opening quic stream", err)
	}
	defer stream.Close()

	if _, err := stream.Write(QUICTestMessageClient); err != nil {
		return serrors.Wrap("writing client payload", err)
	}

	reply := make([]byte, len(QUICTestMessageReply))
	n, err := io.ReadFull(stream, reply)
	if err != nil {
		return serrors.Wrap("reading server reply", err)
	}
	if string(reply[:n]) != QUICTestMessageReply {
		return serrors.New("unexpected server reply", "reply", string(reply[:n]))
	}
	return nil
}

// RunPacketServerOnce reads one datagram, validates the fixed client payload,
// replies once, and then returns.
func RunPacketServerOnce(ctx context.Context, serverConn *snet.Conn) error {
	if err := ctx.Err(); err != nil {
		return serrors.Wrap("packet server context expired", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := serverConn.SetDeadline(deadline); err != nil {
			return serrors.Wrap("setting packet server deadline", err)
		}
		defer serverConn.SetDeadline(time.Time{})
	}

	buf := make([]byte, len(PacketTestMessageClient))
	n, remote, err := serverConn.ReadFrom(buf)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return serrors.Wrap("packet server context expired", ctxErr)
		}
		return serrors.Wrap("reading client datagram", err)
	}
	if !bytes.Equal(buf[:n], PacketTestMessageClient) {
		return serrors.New("unexpected client datagram payload")
	}
	if _, err := serverConn.WriteTo([]byte(PacketTestMessageReply), remote); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return serrors.Wrap("packet server context expired", ctxErr)
		}
		return serrors.Wrap("writing server datagram reply", err)
	}
	return nil
}

// RunPacketClientRoundTrip sends one datagram to remote and verifies the fixed
// server reply.
func RunPacketClientRoundTrip(
	ctx context.Context,
	clientConn *snet.Conn,
	remote *snet.UDPAddr,
) error {
	if err := ctx.Err(); err != nil {
		return serrors.Wrap("packet client context expired", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := clientConn.SetDeadline(deadline); err != nil {
			return serrors.Wrap("setting packet client deadline", err)
		}
		defer clientConn.SetDeadline(time.Time{})
	}

	if _, err := clientConn.WriteTo(PacketTestMessageClient, remote); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return serrors.Wrap("packet client context expired", ctxErr)
		}
		return serrors.Wrap("writing client datagram", err)
	}

	reply := make([]byte, len(PacketTestMessageReply))
	n, _, err := clientConn.ReadFrom(reply)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return serrors.Wrap("packet client context expired", ctxErr)
		}
		return serrors.Wrap("reading server datagram reply", err)
	}
	if string(reply[:n]) != PacketTestMessageReply {
		return serrors.New("unexpected server datagram reply", "reply", string(reply[:n]))
	}
	return nil
}

func RunPacketClientWithE2eRoundTrip(
	ctx context.Context,
	srcAddr snet.UDPAddr,
	dstAddr snet.UDPAddr,
	clientConn *snet.Conn,
	remote *snet.UDPAddr,
	reversePathState []byte,
) error {
	if err := ctx.Err(); err != nil {
		return serrors.Wrap("packet client context expired", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := clientConn.SetDeadline(deadline); err != nil {
			return serrors.Wrap("setting packet client deadline", err)
		}
		defer clientConn.SetDeadline(time.Time{})
	}

	useRawPacket := true
	if useRawPacket {
		fmt.Println("!!! deleteme sending client packet as raw layers")
		srcIP, _ := netip.AddrFromSlice(srcAddr.Host.IP)
		dstIP, _ := netip.AddrFromSlice(dstAddr.Host.IP)
		scn := &slayers.SCION{
			SrcIA: srcAddr.IA,
			DstIA: dstAddr.IA,
		}
		if err := scn.SetSrcAddr(addr.HostIP(srcIP)); err != nil {
			return err
		}
		if err := scn.SetDstAddr(addr.HostIP(dstIP)); err != nil {
			return err
		}

		// scn.NextHdr = slayers.L4UDP
		scn.NextHdr = slayers.End2EndClass

		//
		e2e := &slayers.EndToEndExtn{
			Options: []*slayers.EndToEndOption{
				{
					OptType: slayers.OptTypeReversePath,
					OptData: reversePathState,
				},
			},
		}
		e2e.NextHdr = slayers.L4UDP

		// We don't set the payload, as we are using gopacket directly.
		// But we need the payload length.
		udp := &slayers.UDP{
			// Use the same port where the clientConn is listening:
			SrcPort: uint16(clientConn.LocalAddr().(*snet.UDPAddr).Host.Port),
			DstPort: uint16(dstAddr.Host.Port),
		}
		udp.SetNetworkLayerForChecksum(scn)

		// Serialize the payload:
		upperLayer := gopacket.NewSerializeBuffer()
		err := gopacket.SerializeLayers(upperLayer,
			gopacket.SerializeOptions{
				ComputeChecksums: true,
				FixLengths:       true,
			},
			e2e, udp, gopacket.Payload(PacketTestMessageClient),
		)
		if err != nil {
			return err
		}
		scn.PayloadLen = uint16(len(upperLayer.Bytes()))

		// With the correct size, we can now set the path and path type:
		if err := remote.Path.SetPath(scn); err != nil {
			return err
		}
		//
		//

		//
		// Write message.
		//

		// With raw UDP and slayers:
		buf := gopacket.NewSerializeBuffer()
		err = gopacket.SerializeLayers(buf,
			gopacket.SerializeOptions{
				ComputeChecksums: true,
				FixLengths:       true,
			},
			// scn, udp, gopacket.Payload(PacketTestMessageClient),
			scn, e2e, udp, gopacket.Payload(PacketTestMessageClient),
		)
		if err != nil {
			return err
		}
		rawBuf := buf.Bytes()

		udpSock, err := net.ListenUDP("udp", srcAddr.Host)
		if err != nil {
			return err
		}
		defer udpSock.Close()

		n, err := udpSock.WriteToUDP(rawBuf, remote.NextHop)
		fmt.Printf("deleteme written %d bytes to UDP socket\n", n)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return serrors.Wrap("packet client context expired", ctxErr)
			}
			return err
		}
	} else {
		// With regular snet:
		fmt.Printf("deleteme local socket address is: %s\n", clientConn.LocalAddr().String())
		if _, err := clientConn.WriteTo(PacketTestMessageClient, remote); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return serrors.Wrap("packet client context expired", ctxErr)
			}
			return serrors.Wrap("writing client datagram", err)
		}
	}

	reply := make([]byte, len(PacketTestMessageReply))
	n, _, err := clientConn.ReadFrom(reply)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return serrors.Wrap("packet client context expired", ctxErr)
		}
		return serrors.Wrap("reading server datagram reply", err)
	}
	if string(reply[:n]) != PacketTestMessageReply {
		return serrors.New("unexpected server datagram reply", "reply", string(reply[:n]))
	}
	return nil
}

func CreatePacketServerConn(
	ctx context.Context,
	daemonAddr string,
	localAddr *snet.UDPAddr,
	peerIA addr.IA,
	log Logger,
	withBidirectionalReplier bool,
) (*snet.Conn, error) {
	serverDaemon, err := ConnectDaemon(ctx, daemonAddr)
	if err != nil {
		return nil, err
	}
	defer serverDaemon.Close()

	serverTopo, err := daemon.LoadTopology(ctx, serverDaemon)
	if err != nil {
		return nil, serrors.Wrap("loading server topology", err)
	}
	var replyPather snet.ReplyPather
	if withBidirectionalReplier {
		replyPather = snetpath.NewHummReplyPather()
	} else {
		replyPather = &snet.DefaultReplyPather{}
	}

	return NewSCIONConn(ctx, serverTopo, localAddr.Host, replyPather, true)
}

// RunQuicServer runs the one-shot QUIC server side of the Hummingbird test against
// the provided daemon and local address.
func RunQuicServer(
	ctx context.Context,
	daemonAddr string,
	localAddr *snet.UDPAddr,
	peerIA addr.IA,
	log Logger,
) error {
	serverConn, err := CreatePacketServerConn(ctx, daemonAddr, localAddr, peerIA, log, false)
	if err != nil {
		return err
	}
	defer serverConn.Close()

	tlsConfig, err := NewTLSConfig()
	if err != nil {
		return err
	}
	listener, err := quic.Listen(serverConn, tlsConfig, nil)
	if err != nil {
		return serrors.Wrap("creating quic listener", err)
	}
	defer listener.Close()

	return RunQUICServerOnce(ctx, listener)
}

// RunClient runs the client side of the Hummingbird test, including reservation
// construction, QUIC dial, and round-trip verification.
func RunClient(
	ctx context.Context,
	daemonAddr string,
	localAddr *snet.UDPAddr,
	remoteAddr *snet.UDPAddr,
	keysRoot string,
	log Logger,
) error {
	return RunClientWithParams(
		ctx,
		daemonAddr,
		localAddr,
		remoteAddr,
		keysRoot,
		DefaultReservationParams(),
		log,
	)
}

// RunClientWithParams runs the client side with caller-provided reservation
// parameters for Hummingbird path construction.
func RunClientWithParams(
	ctx context.Context,
	daemonAddr string,
	localAddr *snet.UDPAddr,
	remoteAddr *snet.UDPAddr,
	keysRoot string,
	params ReservationParams,
	log Logger,
) error {
	clientDaemon, err := ConnectDaemon(ctx, daemonAddr)
	if err != nil {
		return err
	}
	defer clientDaemon.Close()

	clientTopo, err := daemon.LoadTopology(ctx, clientDaemon)
	if err != nil {
		return serrors.Wrap("loading client topology", err)
	}
	clientConn, err := NewSCIONConn(ctx, clientTopo, localAddr.Host, nil, false)
	if err != nil {
		return err
	}
	defer clientConn.Close()

	remote, err := BuildHummingbirdRemoteWithParams(
		ctx, clientDaemon, localAddr, remoteAddr, keysRoot, params, log)
	if err != nil {
		return err
	}
	if _, ok := remote.Path.(*snetpath.Reservation); !ok {
		return serrors.New("expected hummingbird reservation path", "type", reflect.TypeOf(remote.Path))
	}

	tlsConfig, err := NewTLSConfig()
	if err != nil {
		return err
	}
	return RunQUICClientRoundTrip(ctx, clientConn, remote, tlsConfig)
}

// RunPacketClient runs the client side of the packet-based Hummingbird test,
// including reservation construction and a single datagram round trip.
func RunPacketClient(
	ctx context.Context,
	daemonAddr string,
	localAddr *snet.UDPAddr,
	remoteAddr *snet.UDPAddr,
	keysRoot string,
	log Logger,
) error {
	return RunPacketClientWithParams(
		ctx,
		daemonAddr,
		localAddr,
		remoteAddr,
		keysRoot,
		DefaultReservationParams(),
		log,
	)
}

// RunPacketClientWithParams runs the packet-based client side with
// caller-provided reservation parameters.
func RunPacketClientWithParams(
	ctx context.Context,
	daemonAddr string,
	localAddr *snet.UDPAddr,
	remoteAddr *snet.UDPAddr,
	keysRoot string,
	params ReservationParams,
	log Logger,
) error {
	clientDaemon, err := ConnectDaemon(ctx, daemonAddr)
	if err != nil {
		return err
	}
	defer clientDaemon.Close()

	clientTopo, err := daemon.LoadTopology(ctx, clientDaemon)
	if err != nil {
		return serrors.Wrap("loading client topology", err)
	}
	clientConn, err := NewSCIONConn(ctx, clientTopo, localAddr.Host, nil, false)
	if err != nil {
		return err
	}
	defer clientConn.Close()

	remote, err := BuildHummingbirdRemoteWithParams(
		ctx, clientDaemon, localAddr, remoteAddr, keysRoot, params, log)
	if err != nil {
		return err
	}
	if _, ok := remote.Path.(*snetpath.Reservation); !ok {
		return serrors.New("expected hummingbird reservation path", "type", reflect.TypeOf(remote.Path))
	}

	return RunPacketClientRoundTrip(ctx, clientConn, remote)
}

func RunPacketClientWithParamsAndE2E(
	ctx context.Context,
	daemonAddr string,
	localAddr *snet.UDPAddr,
	remoteAddr *snet.UDPAddr,
	keysRoot string,
	params ReservationParams,
	log Logger,
) error {
	clientDaemon, err := ConnectDaemon(ctx, daemonAddr)
	if err != nil {
		return err
	}
	defer clientDaemon.Close()

	clientTopo, err := daemon.LoadTopology(ctx, clientDaemon)
	if err != nil {
		return serrors.Wrap("loading client topology", err)
	}
	clientConn, err := NewSCIONConn(ctx, clientTopo, localAddr.Host, nil, false)
	if err != nil {
		return err
	}
	defer clientConn.Close()
	fmt.Printf("deleteme clientConn created as: %s\n", clientConn.LocalAddr().String())

	basePath, err := BasePath(ctx, clientDaemon, localAddr.IA, remoteAddr.IA, log)
	if err != nil {
		return err
	}
	remote, err := BuildHummingbirdRemoteWithPath(basePath, remoteAddr, keysRoot, params, log)
	if err != nil {
		return err
	}
	if _, ok := remote.Path.(*snetpath.Reservation); !ok {
		return serrors.New("expected hummingbird reservation path", "type", reflect.TypeOf(remote.Path))
	}

	reversePathState, err := serializedReverseReservationState(
		basePath,
		keysRoot,
		time.Now(),
		params,
		log,
	)
	if err != nil {
		return err
	}

	return RunPacketClientWithE2eRoundTrip(
		ctx,
		*localAddr,
		*remoteAddr,
		clientConn,
		remote,
		reversePathState)
}

// MustParseUDPAddr parses a SCION UDP address string and returns a wrapped
// error with the original input on failure.
func MustParseUDPAddr(raw string) (*snet.UDPAddr, error) {
	addr, err := snet.ParseUDPAddr(raw)
	if err != nil {
		return nil, serrors.Wrap("parsing scion udp address", err, "raw", raw)
	}
	return addr, nil
}

func formatInterfaces(ifaces []snet.PathInterface) string {
	parts := make([]string, 0, len(ifaces))
	for _, intf := range ifaces {
		parts = append(parts, fmt.Sprintf("%s#%d", intf.IA, intf.ID))
	}
	return strings.Join(parts, " -> ")
}
