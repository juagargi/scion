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
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/scionproto/scion/go/integration"
	"github.com/scionproto/scion/go/lib/addr"
	libint "github.com/scionproto/scion/go/lib/integration"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/sciond"
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

	fmt.Printf("deleteme first line in colibri binary\n")
	var dstAddr snet.UDPAddr
	var timeout = util.DurWrap{Duration: 5 * time.Second}
	addFlags(&dstAddr, &timeout)
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
	return c.run()
	// colibriClient(dstAddr, timeout.Duration)
	// return 0
}

func addFlags(remote *snet.UDPAddr, timeout *util.DurWrap) {
	flag.Var(remote, "remote", "(Mandatory for clients) address to connect to")
	flag.Var(timeout, "timeout", "The timeout for each attempt")
}

type server struct{}

func (s server) run() {
	log.Info("Starting server", "isd_as", integration.Local.IA)
	defer log.Info("Finished server", "isd_as", integration.Local.IA)

	dispatcher := &snet.DefaultPacketDispatcherService{
		Dispatcher: reliable.NewDispatcher(""),
		SCMPHandler: snet.DefaultSCMPHandler{
			RevocationHandler: sciond.RevHandler{Connector: integration.SDConn()},
		},
	}
	conn, port, err := dispatcher.Register(context.Background(), integration.Local.IA,
		integration.Local.Host, addr.SvcNone)
	if err != nil {
		integration.LogFatal("Error listening", "err", err)
	}
	if len(os.Getenv(libint.GoIntegrationEnv)) > 0 {
		// Needed for integration test ready signal.
		fmt.Printf("Port=%d\n", port)
		fmt.Printf("%s%s\n\n", libint.ReadySignal, integration.Local.IA)
	}
	log.Info("Listening", "local", fmt.Sprintf("%v:%d", integration.Local.Host, port))

	for {
		if err := s.accept(conn); err != nil {
			log.Error("accepting connection", "err", err)
		}
	}
}

func (s server) accept(conn snet.PacketConn) error {
	return nil
}

type client struct{}

func (c client) run() int {
	log.Info("deleteme started client")
	return 0
}
