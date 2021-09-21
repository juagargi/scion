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
	"strings"
	"time"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/integration"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/util"
	"github.com/scionproto/scion/go/pkg/app/feature"
)

var (
	features string
	test     string
)

func main() {
	os.Exit(realMain())
}

func realMain() int {
	addFlags()
	if err := integration.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init: %s\n", err)
		return 1
	}
	defer log.HandlePanic()
	defer log.Flush()
	if len(features) != 0 {
		if _, err := feature.ParseDefault(strings.Split(features, ",")); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing features: %s\n", err)
			return 1
		}
	}

	clientArgs := []string{
		"--timeout", "4s",
		"--sciond", integration.SCIOND,
		"--remote", integration.DstAddrPattern + ":" + integration.ServerPortReplace,
	}
	serverArgs := []string{
		"--mode", "server",
		"--sciond", integration.SCIOND,
		"--local", integration.DstAddrPattern + ":0",
	}

	in := integration.NewBinaryIntegration("colibri_integration", "./bin/colibri",
		clientArgs, serverArgs)
	pairs, err := getPairs("all")
	if err != nil {
		log.Error("Error selecting tests", "err", err)
		return 1
	}
	if err := runTests(in, pairs); err != nil {
		log.Error("Error during tests", "err", err)
		return 1
	}
	return 0
}

func addFlags() {
	flag.StringVar(&features, "features", "",
		fmt.Sprintf("enable development features (%v)", feature.String(&feature.Default{}, "|")))
	flag.StringVar(&test, "test", "",
		"Test to run. If empty, all tests are run.")

}

func runTests(in integration.Integration, pairs []integration.IAPair) error {
	return integration.ExecuteTimed(in.Name(), func() error {
		// Make sure that all executed commands can write to the RPC server
		// after shutdown.
		defer time.Sleep(time.Second)

		// Estimating the timeout we should have is hard. CI will abort after 10
		// minutes anyway. Thus this value.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		// First run all servers
		type srvResult struct {
			cleaner func()
			err     error
		}
		log.Info("deleteme", "a", integration.ExtractUniqueDsts(pairs))
		// Start servers in parallel.
		srvResults := make(chan srvResult)
		for _, dst := range integration.ExtractUniqueDsts(pairs) {
			go func(dst *snet.UDPAddr) {
				defer log.HandlePanic()

				srvCtx, cancel := context.WithCancel(ctx)
				waiter, err := in.StartServer(srvCtx, dst)
				if err != nil {
					log.Error(fmt.Sprintf("Error in server: %s", dst.String()), "err", err)
				}
				cleaner := func() {
					cancel()
					if waiter != nil {
						waiter.Wait()
					}
				}
				srvResults <- srvResult{cleaner: cleaner, err: err}
			}(dst)
		}
		// Wait for all servers being started.
		var errs serrors.List
		for range integration.ExtractUniqueDsts(pairs) {
			res := <-srvResults
			// We need to register a cleanup for all servers.
			// Do not short-cut exit here.
			if res.err != nil {
				errs = append(errs, res.err)
			}
			defer res.cleaner()
		}
		if err := errs.ToError(); err != nil {
			return err
		}

		return errs.ToError()
	})
}

// getPairs returns the pairs to test according to the specified subset.
func getPairs(subset string) ([]integration.IAPair, error) {
	pairs := integration.IAPairs(integration.DispAddr)
	if subset == "all" {
		return pairs, nil
	}
	parts := strings.Split(subset, "#")
	if len(parts) != 2 {
		return nil, serrors.New("Invalid subset", "subset", subset)
	}
	return filter(parts[0], parts[1], pairs, integration.ASList), nil
}

// filter returns the list of ASes that are part of the desired subset.
func filter(src, dst string, pairs []integration.IAPair, ases *util.ASList) []integration.IAPair {
	var res []integration.IAPair
	s, err1 := addr.IAFromString(src)
	d, err2 := addr.IAFromString(dst)
	if err1 == nil && err2 == nil {
		for _, pair := range pairs {
			if pair.Src.IA.Equal(s) && pair.Dst.IA.Equal(d) {
				res = append(res, pair)
				return res
			}
		}
	}
	for _, pair := range pairs {
		filter := !contains(ases, src != "noncore", pair.Src.IA)
		filter = filter || !contains(ases, dst != "noncore", pair.Dst.IA)
		if dst == "localcore" {
			filter = filter || pair.Src.IA.I != pair.Dst.IA.I
		}
		if !filter {
			res = append(res, pair)
		}
	}
	return res
}

func contains(ases *util.ASList, core bool, ia addr.IA) bool {
	l := ases.Core
	if !core {
		l = ases.NonCore
	}
	for _, as := range l {
		if ia.Equal(as) {
			return true
		}
	}
	return false
}
