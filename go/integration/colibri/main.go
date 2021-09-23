// Copyright 2021 ETH Zurich
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

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/scionproto/scion/go/integration"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri"
	libcol "github.com/scionproto/scion/go/lib/colibri"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/common"
	libint "github.com/scionproto/scion/go/lib/integration"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/sciond"
	"github.com/scionproto/scion/go/lib/serrors"
	colpath "github.com/scionproto/scion/go/lib/slayers/path/colibri"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/sock/reliable"
	"github.com/scionproto/scion/go/lib/util"
)

func main() {
	os.Exit(realMain())
}

func realMain() int {
	defer log.HandlePanic()
	defer log.Flush()

	var remote snet.UDPAddr
	var timeout = util.DurWrap{Duration: 5 * time.Second}
	addFlags(&remote, &timeout)
	integration.Setup()

	closeTracer, err := integration.InitTracer("end2end-" + integration.Mode)
	if err != nil {
		log.Error("Tracer initialization failed", "err", err)
		return 1
	}
	defer closeTracer()

	if integration.Mode == integration.ModeServer {
		server{}.run()
		return 0
	}
	c := client{}
	return c.run(&remote)
}

func addFlags(remote *snet.UDPAddr, timeout *util.DurWrap) {
	flag.Var(remote, "remote", "(Mandatory for clients) address to connect to")
	flag.Var(timeout, "timeout", "The timeout for each attempt")
}

type server struct{}

func (s server) run() {
	log.Info("Starting server", "isd_as", integration.Local.IA)
	defer log.Info("Finished server", "isd_as", integration.Local.IA)

	dispatcher := reliable.NewDispatcher(reliable.DefaultDispPath)
	scionNet := snet.NewNetwork(integration.Local.IA, dispatcher, sciond.RevHandler{
		Connector: integration.SDConn()})
	conn, err := scionNet.Listen(context.Background(), "udp", integration.Local.Host, addr.SvcNone)
	if err != nil {
		integration.LogFatal("Error listening", "err", err)
	}
	log.Info("Listening", "local", conn.LocalAddr().String())
	if len(os.Getenv(libint.GoIntegrationEnv)) > 0 {
		// Needed for integration test ready signal.
		addr, err := net.ResolveUDPAddr("udp", conn.LocalAddr().String())
		if err != nil {
			log.Error("unable to parse listening address", "err", err)
		}
		fmt.Printf("Port=%d\n", addr.Port)
		fmt.Printf("%s%s\n\n", libint.ReadySignal, integration.Local.IA)
	}

	go func() {
		defer log.HandlePanic()
		s.allowAdmission(integration.SDConn(), integration.Local.Host.IP)
	}()
	for {
		buffer := make([]byte, 16384)
		if err := s.accept(conn, buffer); err != nil {
			log.Error("accepting connection", "err", err)
		}
	}
}

func (s server) allowAdmission(daemon sciond.Connector, serverIP net.IP) {
	for {
		ctx, cancelF := context.WithTimeout(context.Background(), 3*time.Second)
		entry := &colibri.AdmissionEntry{
			DstHost:         serverIP, // could be empty to detect it automatically
			ValidUntil:      time.Now().Add(time.Minute),
			RegexpIA:        "", // from any AS
			RegexpHost:      "", // from any host
			AcceptAdmission: true,
		}
		log.Debug("server, adding admission entry", "ip", serverIP)
		validUntil, err := daemon.ColibriAddAdmissionEntry(ctx, entry)
		if err != nil {
			integration.LogFatal("establishing admission from server", "err", err)
		}
		if time.Until(validUntil).Seconds() < 45 {
			integration.LogFatal("too short validity, something went wrong",
				"requested", entry.ValidUntil, "got", validUntil)
		}
		cancelF()
		time.Sleep(30 * time.Second)
	}
}

func (s server) accept(conn *snet.Conn, buffer []byte) error {
	n, from, err := conn.ReadFrom(buffer)
	if err != nil {
		return err
	}
	fromScion, ok := from.(*snet.UDPAddr)
	if !ok {
		return serrors.New("not a scion address", "addr", from)
	}

	data := buffer[:n]
	if !strings.HasPrefix(string(data), "colibri test") {
		return serrors.New("unknown received pattern", "pattern", string(data),
			"hex", hex.EncodeToString(data))
	}
	log.Info("received pattern", "sender", fromScion.String(), "str", string(data))
	n2, err := conn.WriteTo(data, from)
	if err != nil {
		return serrors.WrapStr("writing echo response from server", err)
	}
	if n2 != len(data) {
		return serrors.New("wrong size writing", "data_len", len(data), "written", n2)
	}
	return nil
}

type client struct{}

func (c client) run(remote *snet.UDPAddr) int {
	pair := fmt.Sprintf("%s -> %s", integration.Local.IA, remote.IA)
	log.Info("Starting", "pair", pair)
	defer log.Info("Finished", "pair", pair)
	defer integration.Done(integration.Local.IA, remote.IA)

	ctx, cancelF := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelF()
	deadline, _ := ctx.Deadline()

	// find a path to the destination
	pathquerier := sciond.Querier{
		Connector: integration.SDConn(),
		IA:        integration.Local.IA,
	}
	pathsToDst, err := pathquerier.Query(ctx, remote.IA)
	if err != nil {
		integration.LogFatal("obtaining paths", "err", err)
	}
	if len(pathsToDst) == 0 {
		integration.LogFatal("no paths found")
	}
	pathToDst := pathsToDst[0]
	log.Debug("found path to destination", "path", pathToDst)
	remote.Path = pathToDst.Path()
	remote.NextHop = pathToDst.UnderlayNextHop()
	// dial to destination using the first path
	dispatcher := reliable.NewDispatcher(reliable.DefaultDispPath)
	scionNet := snet.NewNetwork(integration.Local.IA, dispatcher, sciond.RevHandler{
		Connector: integration.SDConn()})
	log.Debug("dialing with best effort", "addr", remote.String(), "path", remote.Path)
	conn, err := scionNet.Dial(ctx, "udp", integration.Local.Host, remote, addr.SvcNone)
	if err != nil {
		integration.LogFatal("dialing", "err", err)
	}
	err = conn.SetDeadline(deadline)
	if err != nil {
		integration.LogFatal("setting deadline", "err", err)
	}
	_, err = conn.WriteTo([]byte("colibri test best effort"), remote)
	if err != nil {
		integration.LogFatal("writing data with best effort", "err", err)
	}
	// read echo back
	buff := make([]byte, 1500)
	_, err = conn.Read(buff)
	if err != nil {
		integration.LogFatal("reading data", "err", err)
	}
	stitchable, err := c.listRsvs(ctx, integration.SDConn(), remote.IA)
	if err != nil {
		integration.LogFatal("listing reservations", "err", err)
	}
	log.Debug("listed reservations", "list", stitchable.String())
	trips := libcol.CombineAll(stitchable)
	log.Info("computed full trips", "count", len(trips))
	if len(trips) == 0 {
		integration.LogFatal("no trips found")
	}
	// obtain an reservation
	p, err := c.createRsv(ctx, integration.SDConn(), integration.Local.IA, remote, trips[0].Segments(), 1)
	if err != nil {
		integration.LogFatal("creating reservation", "err", err)
	}
	// use the reservation
	remote.Path = p.Path()
	remote.NextHop = p.UnderlayNextHop()
	_, err = conn.WriteTo([]byte("colibri test colibri path"), remote)
	if err != nil {
		integration.LogFatal("writing data with colibri", "err", err)
	}
	// read echo back again
	_, raddr, err := conn.ReadFrom(buff)
	if err != nil {
		integration.LogFatal("reading data", "err", err)
	}
	sraddr, ok := raddr.(*snet.UDPAddr)
	if !ok {
		integration.LogFatal("sender of response is not scion", "raddr", raddr,
			"type", common.TypeOf(raddr))
	}
	if sraddr.Path.Type != colpath.PathType {
		integration.LogFatal("non-colibri path type", "type", sraddr.Path.Type)
	}
	return 0
}

func (c client) listRsvs(ctx context.Context, daemon sciond.Connector, remote addr.IA) (
	*libcol.StitchableSegments, error) {
	for {
		stitchable, err := daemon.ColibriListRsvs(ctx, remote)
		if err != nil {
			return nil, err
		}
		if stitchable != nil {
			return stitchable, nil
		}
		time.Sleep(time.Second)
	}
}

func (c client) createRsv(ctx context.Context, daemon sciond.Connector, localIA addr.IA,
	remoteAddr *snet.UDPAddr, segments []reservation.ID, requestBW reservation.BWCls) (
	snet.Path, error) {

	setupReq := &libcol.E2EReservationSetup{
		Id: reservation.ID{
			ASID:   localIA.A,
			Suffix: make([]byte, 12),
		},
		SrcIA:       localIA,
		DstIA:       remoteAddr.IA,
		DstHost:     remoteAddr.Host.IP,
		Index:       0, // new index
		Segments:    segments,
		RequestedBW: requestBW,
	}
	rand.Read(setupReq.Id.Suffix) // random suffix
	return daemon.ColibriSetupRsv(ctx, setupReq)
}
