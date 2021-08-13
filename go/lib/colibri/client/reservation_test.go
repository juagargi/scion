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
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/go/lib/colibri"
	"github.com/scionproto/scion/go/lib/colibri/client/sorting"
	ct "github.com/scionproto/scion/go/lib/colibri/coltest"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/sciond/mock_sciond"
	"github.com/scionproto/scion/go/lib/snet"
	snetpath "github.com/scionproto/scion/go/lib/snet/path"
	"github.com/scionproto/scion/go/lib/spath"
	"github.com/scionproto/scion/go/lib/xtest"
)

func TestNewReservation(t *testing.T) {

}

func TestReservationStartReservation(t *testing.T) {
	// modify the global task duration for the test
	e2eRenewalTaskDuration = reservation.TicksInE2ERsv * 4 * time.Millisecond / 2 // 16 millisecs

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	ctx, cancelF := context.WithTimeout(context.Background(), time.Second)
	defer cancelF()

	daemon := mock_sciond.NewMockConnector(ctrl)
	srcIA := xtest.MustParseIA("1-ff00:0:111")
	dstIA := xtest.MustParseIA("1-ff00:0:112")
	daemon.EXPECT().LocalIA(gomock.Any()).Return(srcIA, nil)
	daemon.EXPECT().ColibriListRsvs(gomock.Any(), dstIA).Return(
		ct.NewStitchableSegments("1-ff00:0:111", "1-ff00:0:112",
			ct.WithUpSegs(1),
			ct.WithDownSegs(0),
		),
		nil)
	rsv, err := NewReservation(ctx, daemon, dstIA, 11, 0, sorting.ByExpiration)
	require.NoError(t, err)
	require.True(t, rsv.request.Id.IsE2EID())

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
	err = rsv.StartReservation(ctx, func(r *Reservation, err error) {
		require.Fail(t, "should not fail")
	})
	require.NoError(t, err)
	require.Equal(t, timesCalled, 1)
	require.Equal(t, testPaths[0], rsv.colibriPath)

	// now wait e2eRenewalTaskDuration + a bit
	time.Sleep(e2eRenewalTaskDuration * 3)
	require.Greater(t, timesCalled, 1)
	require.Equal(t, testPaths[2], rsv.colibriPath)
	require.Equal(t, reservation.NewIndexNumber(timesCalled-1), rsv.request.Index)

	// stop and check
	daemon.EXPECT().ColibriCleanupRsv(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, id *reservation.ID, idx reservation.IndexNumber) error {
			require.Equal(t, rsv.request.Id, *id)
			require.Equal(t, reservation.NewIndexNumber(timesCalled-1), idx)
			return nil
		})
	err = rsv.StopReservation(ctx)
	require.NoError(t, err)
	require.Nil(t, rsv.runner)
}
