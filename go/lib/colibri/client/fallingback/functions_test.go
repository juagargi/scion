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

package fallingback

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	rt "github.com/scionproto/scion/go/co/reservation/test"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri"
	"github.com/scionproto/scion/go/lib/colibri/client"
	ct "github.com/scionproto/scion/go/lib/colibri/coltest"
	"github.com/scionproto/scion/go/lib/mocks/net/mock_net"
	"github.com/scionproto/scion/go/lib/sciond"
	"github.com/scionproto/scion/go/lib/sciond/mock_sciond"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/sock/reliable/mock_reliable"
	"github.com/scionproto/scion/go/lib/xtest"
	"github.com/stretchr/testify/require"
)

func TestCaptureTrips(t *testing.T) {
	ctx, cancelF := context.WithTimeout(context.Background(), time.Second)
	defer cancelF()

	srcAddr := &snet.UDPAddr{
		IA:   xtest.MustParseIA("1-ff00:0:111"),
		Host: xtest.MustParseUDPAddr(t, "127.0.0.1:12346"),
	}
	dstAddr := &snet.UDPAddr{
		IA:   xtest.MustParseIA("1-ff00:0:112"),
		Host: xtest.MustParseUDPAddr(t, "127.0.0.1:12345"),
	}
	stitchables := ct.NewStitchableSegments("1-ff00:0:111", "1-ff00:0:112",
		// 1 direct trip, + 2 thru core
		ct.WithCoreASes("1-ff00:0:110", "1-ff00:0:100"),

		ct.WithUpSegs(1, 2, 3),

		ct.WithDownSegs(2, 3),
	)
	ctrl, network, daemon := mockNetwork(t, srcAddr.IA, stitchables, false)
	defer ctrl.Finish()

	capturedTrips := make([]*colibri.FullTrip, 0)
	_, err := client.NewReservation(ctx, network, daemon, dstAddr, 11, 0,
		CaptureTrips(&capturedTrips))
	require.NoError(t, err)
	require.Len(t, capturedTrips, 3) // three trips?
	// check the order is the same
	fullTrips := colibri.CombineAll(stitchables)
	sort.SliceStable(fullTrips, func(i, j int) bool {
		return false
	})
	require.Equal(t, fullTrips, capturedTrips)
}

func TestToNext(t *testing.T) {
	stitchables := ct.NewStitchableSegments("1-ff00:0:111", "1-ff00:0:112",
		// 1 direct trip, + 2 thru core
		ct.WithCoreASes("1-ff00:0:110", "1-ff00:0:100"),

		ct.WithUpSegs(1, 2, 3),

		ct.WithDownSegs(2, 3),
	)
	capturedTrips := colibri.CombineAll(stitchables)

	fallbackFcn := ToNext(capturedTrips)
	require.Equal(t, capturedTrips[1], fallbackFcn(nil, nil))
	require.Equal(t, capturedTrips[2], fallbackFcn(nil, nil))
}

func TestSkipInterface(t *testing.T) {
	stitchables := ct.NewStitchableSegments("1-ff00:0:111", "1-ff00:0:112",
		ct.WithCoreASes("1-ff00:0:110", "1-ff00:0:100"),

		ct.WithUpSegs(1, 2, 3), // 1 direct trip, + 2 thru core (need down segments)
		ct.WithPath(ct.Up, 0, rt.NewPath(0, "1-ff00:0:111", 2, 2, "1-ff00:0:112", 0)),
		ct.WithPath(ct.Up, 1, rt.NewPath(0, "1-ff00:0:111", 2, 1, "1-ff00:0:110", 0)),
		ct.WithPath(ct.Up, 2, rt.NewPath(0, "1-ff00:0:111", 3, 1, "1-ff00:0:100", 0)),

		ct.WithDownSegs(2, 3),
		ct.WithPath(ct.Down, 0, rt.NewPath(0, "1-ff00:0:110", 2, 1, "1-ff00:0:112", 0)),
		ct.WithPath(ct.Down, 1, rt.NewPath(0, "1-ff00:0:100", 2, 1, "1-ff00:0:112", 0)),
	)
	capturedTrips := colibri.CombineAll(stitchables)

	require.Len(t, capturedTrips, 3)
	require.Len(t, *capturedTrips[0], 1)
	require.Len(t, *capturedTrips[1], 2)
	require.Len(t, *capturedTrips[2], 2)

	fallbackFcn := SkipInterface(capturedTrips)
	rsv := client.NewReservationForTesting(nil, time.Hour, nil, nil, nil, nil,
		capturedTrips[0], nil, nil, nil)
	admissionFailure := &colibri.E2ESetupError{
		E2EResponseError: colibri.E2EResponseError{
			Message:  "mock",
			FailedAS: 0, // the first step failed,in: 0, eg:2 of 1-ff00:0:111
		},
	}
	nextTrip := fallbackFcn(rsv, admissionFailure)
	require.Equal(t, capturedTrips[2], nextTrip)
}

func mockNetwork(t *testing.T, srcIA addr.IA, stitchables *colibri.StitchableSegments,
	willOpenConnection bool) (
	*gomock.Controller, *snet.SCIONNetwork, *mock_sciond.MockConnector) {

	t.Helper()

	ctrl := gomock.NewController(t)

	dispatcher := mock_reliable.NewMockDispatcher(ctrl)
	daemon := mock_sciond.NewMockConnector(ctrl)

	if willOpenConnection {
		mockConn := mock_net.NewMockPacketConn(ctrl)
		mockConn.EXPECT().Close()
		dispatcher.EXPECT().Register(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(
			mockConn, uint16(0), nil)
	}
	daemon.EXPECT().ColibriListRsvs(gomock.Any(), gomock.Any()).Return(stitchables, nil)
	network := snet.NewNetwork(srcIA, dispatcher, sciond.RevHandler{Connector: daemon})

	return ctrl, network, daemon
}
