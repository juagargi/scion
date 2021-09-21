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
	"bytes"
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/scionproto/scion/go/integration"
	"github.com/scionproto/scion/go/lib/addr"
	libint "github.com/scionproto/scion/go/lib/integration"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/sciond"
	"github.com/scionproto/scion/go/lib/serrors"
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

	for {
		buffer := make([]byte, 16384)
		if err := s.accept(conn, buffer); err != nil {
			log.Error("accepting connection", "err", err)
		}
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
	if !bytes.Equal(data, ([]byte)("test test")) {
		return serrors.New("unknown received pattern", "pattern", string(data),
			"hex", hex.EncodeToString(data))
	}
	log.Info("received pattern", "sender", fromScion.String())
	return nil
}

type client struct{}

func (c client) run(remote *snet.UDPAddr) int {
	pair := fmt.Sprintf("%s -> %s", integration.Local.IA, remote.IA)
	log.Info("Starting", "pair", pair)
	defer log.Info("Finished", "pair", pair)
	defer integration.Done(integration.Local.IA, remote.IA)

	return 0
}
