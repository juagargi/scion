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

package client

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri"
	"github.com/scionproto/scion/go/lib/colibri/client/sorting"
	ct "github.com/scionproto/scion/go/lib/colibri/coltest"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/mocks/net/mock_net"
	"github.com/scionproto/scion/go/lib/sciond"
	"github.com/scionproto/scion/go/lib/sciond/mock_sciond"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
	snetpath "github.com/scionproto/scion/go/lib/snet/path"
	"github.com/scionproto/scion/go/lib/sock/reliable/mock_reliable"
	"github.com/scionproto/scion/go/lib/spath"
	"github.com/scionproto/scion/go/lib/xtest"
)

func TestNewReservation(t *testing.T) {
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
	ctrl, network, daemon := mockNetwork(t, srcAddr.IA,
		ct.NewStitchableSegments("1-ff00:0:111", "1-ff00:0:112",
			ct.WithUpSegs(1),
			ct.WithDownSegs(0),
		), false)
	defer ctrl.Finish()

	rsv, err := NewReservation(ctx, network, daemon, dstAddr, 11, 0, sorting.ByExpiration)
	require.NoError(t, err)
	require.True(t, rsv.request.Id.IsE2EID())
	require.Equal(t, dstAddr, rsv.dstAddr)
	require.NotSame(t, rsv.dstAddr, dstAddr) // a copy
	require.Nil(t, rsv.connection)           // connectionless
	require.Nil(t, rsv.colibriPath)          // not negotiated yet

	require.NotNil(t, rsv.request) // should be populated
	require.Equal(t, dstAddr.IA, rsv.request.DstIA)
	require.Equal(t, srcAddr.IA, rsv.request.SrcIA)
	require.Greater(t, len(rsv.request.Segments), 0)
}

func TestReservationOpen(t *testing.T) {
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
	ctrl, network, daemon := mockNetwork(t, srcAddr.IA,
		ct.NewStitchableSegments("1-ff00:0:111", "1-ff00:0:112",
			ct.WithUpSegs(1),
			ct.WithDownSegs(0),
		), true)
	defer ctrl.Finish()

	rsv, err := NewReservation(ctx, network, daemon, dstAddr, 11, 0, sorting.ByExpiration)
	require.NoError(t, err)

	// modify the global task duration for the test
	rsv.e2eRenewalTaskDuration = reservation.TicksInE2ERsv * 4 * time.Millisecond / 2 // 16 millisecs

	testPaths := []*snetpath.Path{
		{SPath: spath.Path{Raw: xtest.MustParseHexString("01")}},
		{SPath: spath.Path{Raw: xtest.MustParseHexString("02")}},
		{SPath: spath.Path{Raw: xtest.MustParseHexString("03")}},
	}
	timesCalled := 0
	daemon.EXPECT().ColibriSetupRsv(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(_ context.Context, req *colibri.E2EReservationSetup) (snet.Path, error) {
			// check that the index increments monotonically
			timesAsIndex := reservation.IndexNumber(0).Add(reservation.IndexNumber(timesCalled))
			require.Equal(t, timesAsIndex, req.Index)
			// return an identifiable path for the test
			p := testPaths[len(testPaths)-1]
			if timesCalled < len(testPaths) {
				p = testPaths[timesCalled]
			}
			timesCalled++
			return p, nil

		})

	err = rsv.Open(ctx, srcAddr.Host, func(r *Reservation, err error) *colibri.FullTrip {
		require.Fail(t, "should not fail")
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, timesCalled, 1)
	require.Equal(t, testPaths[0], rsv.colibriPath)

	// now wait e2eRenewalTaskDuration + a bit
	time.Sleep(rsv.e2eRenewalTaskDuration * 3)
	require.Greater(t, timesCalled, 1)
	require.Equal(t, testPaths[2], rsv.colibriPath) // last path available before the error

	// stop and check
	daemon.EXPECT().ColibriCleanupRsv(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, id *reservation.ID, idx reservation.IndexNumber) error {
			require.Equal(t, rsv.request.Id, *id)
			require.Equal(t, reservation.NewIndexNumber(timesCalled-1), idx)
			return nil
		})
	err = rsv.Close(ctx)
	require.NoError(t, err)
}

func TestReservationFailOnRenewal(t *testing.T) {
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
		ct.WithUpSegs(1, 1),   // two up
		ct.WithDownSegs(0, 0), // two down
	)
	ctrl, network, daemon := mockNetwork(t, srcAddr.IA, stitchables, true)
	defer ctrl.Finish()

	trips := colibri.CombineAll(stitchables)
	rsv, err := NewReservation(ctx, network, daemon, dstAddr, 11, 0) // unsorted; will use [0]
	require.NoError(t, err)

	// modify the global task duration for the test
	rsv.e2eRenewalTaskDuration = reservation.TicksInE2ERsv * 4 * time.Millisecond / 2 // 16 millisecs

	testPaths := []*snetpath.Path{
		{SPath: spath.Path{Raw: xtest.MustParseHexString("01")}},
	}
	testPathAfterFailure := &snetpath.Path{SPath: spath.Path{Raw: xtest.MustParseHexString("01")}}
	timesCalled := 0
	everFailed := false
	timesCalledAfterFailure := 0
	doFailAgain := false
	daemon.EXPECT().ColibriSetupRsv(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(_ context.Context, req *colibri.E2EReservationSetup) (snet.Path, error) {
			timesCalled++
			if everFailed {
				timesCalledAfterFailure++
				if doFailAgain {
					return nil, serrors.New("mock error 2")
				}
				return testPathAfterFailure, nil
			}
			if timesCalled > len(testPaths) {
				return nil, serrors.New("mock error")
			}
			return testPaths[timesCalled-1], nil
		})

	waitForFallback := sync.WaitGroup{}
	waitForFallback.Add(1)
	waitForTest := sync.WaitGroup{}
	waitForTest.Add(1)
	err = rsv.Open(ctx, srcAddr.Host, func(r *Reservation, err error) *colibri.FullTrip {
		if !everFailed {
			waitForFallback.Done()
			everFailed = true
			return trips[1]
		}
		// sync with the test
		waitForTest.Wait()
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, timesCalled, 1)
	require.Equal(t, testPaths[0], rsv.colibriPath)
	require.Equal(t, 0, timesCalledAfterFailure)
	require.NotEqual(t, trips[1].Segments(), rsv.request.Segments)

	waitForFallback.Wait() // wait until the fallback function is done
	require.Equal(t, true, everFailed)
	require.Equal(t, testPaths[len(testPaths)-1], rsv.colibriPath) // last valid path before failure
	// sleep more to allow the routine to finish setting the rsv.
	time.Sleep(10 * time.Millisecond)
	require.Greater(t, timesCalledAfterFailure, 0)
	require.Equal(t, trips[1].Segments(), rsv.request.Segments)
	require.NotNil(t, rsv.connection)
	require.NotNil(t, rsv.runner)

	// unlock second part of the test, where the fallback function will fail
	waitForTest.Done()
	time.Sleep(10 * time.Millisecond) // wait a bit longer to allow the runner to finish
	require.Equal(t, testPathAfterFailure, rsv.colibriPath)

	// this is  a bit of a hack: change the onError function
	alwaysFailing := false
	rsv.onError = func(rsv *Reservation, err error) *colibri.FullTrip {
		alwaysFailing = true
		return nil
	}
	doFailAgain = true
	// and wait for 2 periods
	time.Sleep(2 * rsv.e2eRenewalTaskDuration)
	require.Equal(t, true, alwaysFailing)
	// because it failed:
	require.Nil(t, rsv.connection)
	require.Nil(t, rsv.runner)
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
