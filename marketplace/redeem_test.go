// Copyright 2026 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package marketplace_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/marketplace"
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/hummingbird/bwencoding"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
)

// TestRedeemWithoutRedemptionServer checks that without a delegation and without a
// connected redemption server, the request is refused.
func TestRedeemWithoutRedemptionServer(t *testing.T) {
	h := newHandler()
	resp, err := h.Redeem(context.Background(), &hummingbird.RedeemAssetFromASRequest{})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetError())
}

// TestRedeemReachesTheRedemptionServer checks that with a non-delegated AS redemption
// server connected, the request is queued and stays unanswered until that server
// replies with the matching request id.
func TestRedeemReachesTheRedemptionServer(t *testing.T) {
	h := newHandler()
	conn := h.AttachRemote(func() {})

	pending := redeemInBackground(t, h, context.Background(),
		&hummingbird.RedeemAssetFromASRequest{Bandwidth: 100})
	req := waitForRequest(t, conn)
	assert.NotZero(t, req.RequestId, "the handler must assign a request id to correlate the reply")
	assert.Equal(t, uint32(100), req.Bandwidth)

	select {
	case <-pending:
		t.Fatal("the request was answered before the redemption server replied")
	case <-time.After(50 * time.Millisecond):
	}

	h.DeliverRedemptionResponse(&hummingbird.RedeemAssetFromASResponse{
		RequestId: req.RequestId,
		Result: &hummingbird.RedeemAssetFromASResponse_ResInfo{
			ResInfo: &hummingbird.ReservationInfo{ReservationId: 7},
		},
	})
	resp, err := waitForResult(t, pending)
	require.NoError(t, err)
	assert.Equal(t, uint32(7), resp.GetResInfo().GetReservationId())
}

// TestDeliverUnknownRedemptionResponse checks that a response nobody is waiting for is
// dropped rather than blocking the caller.
func TestDeliverUnknownRedemptionResponse(t *testing.T) {
	h := newHandler()
	h.DeliverRedemptionResponse(&hummingbird.RedeemAssetFromASResponse{RequestId: 42})
}

// TestRedeemCancelledWhileWaiting checks that a caller which gives up before the
// redemption server answers gets its context error, and that the abandoned request no
// longer occupies a slot: a later response for it is simply unknown.
func TestRedeemCancelledWhileWaiting(t *testing.T) {
	h := newHandler()
	conn := h.AttachRemote(func() {})

	ctx, cancel := context.WithCancel(context.Background())
	pending := redeemInBackground(t, h, ctx, &hummingbird.RedeemAssetFromASRequest{})
	req := waitForRequest(t, conn)

	cancel()
	_, err := waitForResult(t, pending)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "expected the context error, got %v", err)

	// The request was dropped, so its late answer belongs to nobody. Delivering it
	// must not block or panic.
	h.DeliverRedemptionResponse(&hummingbird.RedeemAssetFromASResponse{RequestId: req.RequestId})

	// A cancelled request must not disturb the next one.
	next := redeemInBackground(t, h, context.Background(), &hummingbird.RedeemAssetFromASRequest{})
	nextReq := waitForRequest(t, conn)
	assert.NotEqual(t, req.RequestId, nextReq.RequestId, "request ids must not be reused")
	h.DeliverRedemptionResponse(&hummingbird.RedeemAssetFromASResponse{
		RequestId: nextReq.RequestId,
		Result: &hummingbird.RedeemAssetFromASResponse_ResInfo{
			ResInfo: &hummingbird.ReservationInfo{ReservationId: 9},
		},
	})
	resp, err := waitForResult(t, next)
	require.NoError(t, err)
	assert.Equal(t, uint32(9), resp.GetResInfo().GetReservationId())
}

// TestRedeemWithAnAlreadyCancelledContext checks that a caller that has already given
// up does not get its request handed to the redemption server.
func TestRedeemWithAnAlreadyCancelledContext(t *testing.T) {
	h := newHandler()
	conn := h.AttachRemote(func() {})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := h.Redeem(ctx, &hummingbird.RedeemAssetFromASRequest{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "expected the context error, got %v", err)

	// Either the hand-over lost the race with the cancellation, or the request was
	// queued and then abandoned. Either way nothing must be waiting for an answer.
	select {
	case <-conn.Out():
	default:
	}
}

// TestDetachRemoteRefusesPendingRequests checks that when the AS redemption server
// disconnects, the requests it has not answered yet are refused.
func TestDetachRemoteRefusesPendingRequests(t *testing.T) {
	h := newHandler()
	conn := h.AttachRemote(func() {})
	pending := redeemInBackground(t, h, context.Background(),
		&hummingbird.RedeemAssetFromASRequest{})
	waitForRequest(t, conn)

	h.DetachRemote(conn)
	resp, err := waitForResult(t, pending)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetError())

	// With nothing connected any more, later requests are refused too.
	resp, err = h.Redeem(context.Background(), &hummingbird.RedeemAssetFromASRequest{})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetError())
}

// TestAttachRemoteReplacesTheOpenConnection checks that a reconnecting redemption
// server replaces the open connection: the requests of the old one are refused, and
// detaching that now stale connection afterwards leaves the new one in place.
func TestAttachRemoteReplacesTheOpenConnection(t *testing.T) {
	h := newHandler()
	firstCancelled := make(chan struct{})
	first := h.AttachRemote(func() { close(firstCancelled) })
	pending := redeemInBackground(t, h, context.Background(),
		&hummingbird.RedeemAssetFromASRequest{})
	waitForRequest(t, first)

	second := h.AttachRemote(func() {})
	resp, err := waitForResult(t, pending)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetError(),
		"a request handed to the replaced connection can never be answered")
	select {
	case <-firstCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the replaced connection was not cancelled")
	}

	// The stale connection must not detach the one that replaced it.
	h.DetachRemote(first)
	redeemInBackground(t, h, context.Background(), &hummingbird.RedeemAssetFromASRequest{})
	waitForRequest(t, second)
}

// TestCloseRedemptionServerConnection checks that CloseRedemptionServerConnection does
// not deadlock against the handler. It must return, and cancel the stream serving the
// connection.
func TestCloseRedemptionServerConnection(t *testing.T) {
	h := newHandler()
	cancelled := make(chan struct{})
	conn := h.AttachRemote(func() { close(cancelled) })
	pending := redeemInBackground(t, h, context.Background(),
		&hummingbird.RedeemAssetFromASRequest{})
	waitForRequest(t, conn)

	returned := make(chan struct{})
	go func() {
		h.CloseRedemptionServerConnection()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("CloseRedemptionServerConnection did not return")
	}

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was not cancelled")
	}
	resp, err := waitForResult(t, pending)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetError())

	// Closing again, and detaching the closed connection, must both be harmless.
	h.CloseRedemptionServerConnection()
	h.DetachRemote(conn)
}

// TestRedeemOnAFullQueueOfAClosingConnection checks that a request waiting to be handed
// over is released when the connection goes away, instead of blocking its caller.
func TestRedeemOnAFullQueueOfAClosingConnection(t *testing.T) {
	h := newHandler()
	conn := h.AttachRemote(func() {})
	fillQueue(t, h, conn)
	blocked := redeemInBackground(t, h, context.Background(),
		&hummingbird.RedeemAssetFromASRequest{})
	waitForPending(t, h, conn.QueueCap()+1)

	// Nothing drains the queue, so the hand-over is still waiting. Detaching the
	// connection has to release it.
	h.DetachRemote(conn)
	resp, err := waitForResult(t, blocked)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetError())
}

// TestRedeemOnAFullQueueHonoursCancellation checks that the hand-over itself, not only
// the wait for the answer, gives up with the caller.
func TestRedeemOnAFullQueueHonoursCancellation(t *testing.T) {
	h := newHandler()
	conn := h.AttachRemote(func() {})
	fillQueue(t, h, conn)
	ctx, cancel := context.WithCancel(context.Background())
	blocked := redeemInBackground(t, h, ctx, &hummingbird.RedeemAssetFromASRequest{})
	// The queue is full, so this request is waiting in the hand-over select.
	waitForPending(t, h, conn.QueueCap()+1)
	cancel()
	_, err := waitForResult(t, blocked)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "expected the context error, got %v", err)
}

// fillQueue leaves the queue of conn full, so that the next hand-over is certain to
// block. Spawning the requests is not enough: their goroutines may not have run yet.
func fillQueue(
	t *testing.T,
	h *marketplace.RedemptionServerHandler,
	conn *marketplace.RemoteConn,
) {
	t.Helper()
	for i := 0; i < conn.QueueCap(); i++ {
		redeemInBackground(t, h, context.Background(), &hummingbird.RedeemAssetFromASRequest{})
	}
	deadline := time.Now().Add(5 * time.Second)
	for conn.QueueLen() < conn.QueueCap() {
		if time.Now().After(deadline) {
			t.Fatalf("the queue never filled up: %d of %d", conn.QueueLen(), conn.QueueCap())
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForPending waits until n requests are registered as waiting for an answer.
func waitForPending(t *testing.T, h *marketplace.RedemptionServerHandler, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.PendingLen() < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d requests were registered", h.PendingLen(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// completeEncodingPoints is an encoding table of the size a delegation must carry.
func completeEncodingPoints() []uint32 {
	points := make([]uint32, bwencoding.Codepoints)
	for i := range points {
		points[i] = uint32(i + 1)
	}
	return points
}

// TestApplyDelegationRejectsBadParams checks that a delegation the marketplace could
// not serve is refused, both for an incomplete encoding table and for a key that AES
// does not accept.
//
// The handler has a nil store, so the test also pins the ordering: if the validation
// did not come first, ApplyDelegation would reach the store and panic instead.
func TestApplyDelegationRejectsBadParams(t *testing.T) {
	testCases := map[string]struct {
		points []uint32
		key    []byte
		reason string
	}{
		"nil points":         {points: nil, key: make([]byte, 16), reason: "encoding points"},
		"empty points":       {points: []uint32{}, key: make([]byte, 16), reason: "encoding points"},
		"one point":          {points: []uint32{100}, key: make([]byte, 16), reason: "encoding points"},
		"one point short":    {points: make([]uint32, bwencoding.Codepoints-1), key: make([]byte, 16), reason: "encoding points"},
		"one point too many": {points: make([]uint32, bwencoding.Codepoints+1), key: make([]byte, 16), reason: "encoding points"},
		"nil key":            {points: completeEncodingPoints(), key: nil, reason: "key"},
		"empty key":          {points: completeEncodingPoints(), key: []byte{}, reason: "key"},
		"key of 7 bytes":     {points: completeEncodingPoints(), key: make([]byte, 7), reason: "key"},
		"key of 15 bytes":    {points: completeEncodingPoints(), key: make([]byte, 15), reason: "key"},
		"key of 33 bytes":    {points: completeEncodingPoints(), key: make([]byte, 33), reason: "key"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			h := newHandler()
			err := h.ApplyDelegation(context.Background(), &marketplace.RedemptionDelegationUpdate{
				ExpirationTime: time.Now().Add(time.Hour).Unix(),
				IdLimitHigh:    1000,
				Key:            tc.key,
				EncodingPoints: tc.points,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.reason)
		})
	}
}

// TestApplyDelegationAcceptsEveryAESKeySize checks that the key check rejects only what
// AES rejects, so the three legal sizes must pass validation. The nil store means the
// call still fails, but at the store rather than at the validation.
func TestApplyDelegationAcceptsEveryAESKeySize(t *testing.T) {
	for _, size := range []int{16, 24, 32} {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			_, err := marketplace.NewRedemptionService(&marketplace.RedemptionDelegationUpdate{
				ExpirationTime: time.Now().Add(time.Hour).Unix(),
				IdLimitHigh:    1000,
				Key:            make([]byte, size),
				EncodingPoints: completeEncodingPoints(),
			}, nil)
			require.NoError(t, err)
		})
	}
}

// TestNewRedemptionServiceRejectsIncompleteEncodingPoints checks that the invariant
// belongs to the type, not only to the callers that happen to validate.
func TestNewRedemptionServiceRejectsIncompleteEncodingPoints(t *testing.T) {
	_, err := marketplace.NewRedemptionService(&marketplace.RedemptionDelegationUpdate{
		ExpirationTime: time.Now().Add(time.Hour).Unix(),
		IdLimitHigh:    1000,
		Key:            make([]byte, 16),
		EncodingPoints: []uint32{100, 200},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encoding points")
}

// TestNewRedemptionServiceAcceptsACompleteTable is the positive case,
// so the checks above cannot pass merely by rejecting everything.
func TestNewRedemptionServiceAcceptsACompleteTable(t *testing.T) {
	s, err := marketplace.NewRedemptionService(&marketplace.RedemptionDelegationUpdate{
		ExpirationTime: time.Now().Add(time.Hour).Unix(),
		IdLimitHigh:    1000,
		Key:            make([]byte, 16),
		EncodingPoints: completeEncodingPoints(),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, s)
}

// newHandler builds a handler without a store. Only ApplyDelegation reaches the store,
// and these tests exercise the routing towards the redemption server of the AS instead.
func newHandler() *marketplace.RedemptionServerHandler {
	return marketplace.NewRedemptionServerHandler(addr.MustParseIA("1-ff00:0:110"), nil)
}

// redemptionResult is what one Redeem call returned.
type redemptionResult struct {
	resp *hummingbird.RedeemAssetFromASResponse
	err  error
}

// redeemInBackground calls Redeem, which blocks until the request is answered, from a
// goroutine, so that the test can meanwhile act as the redemption server.
func redeemInBackground(
	t *testing.T,
	h *marketplace.RedemptionServerHandler,
	ctx context.Context,
	req *hummingbird.RedeemAssetFromASRequest,
) <-chan redemptionResult {
	t.Helper()
	done := make(chan redemptionResult, 1)
	go func() {
		resp, err := h.Redeem(ctx, req)
		done <- redemptionResult{resp: resp, err: err}
	}()
	return done
}

// waitForRequest returns the request the handler routed to a connection.
func waitForRequest(
	t *testing.T,
	conn *marketplace.RemoteConn,
) *hummingbird.RedeemAssetFromASRequest {
	t.Helper()
	select {
	case req := <-conn.Out():
		require.NotNil(t, req)
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not route the request to this connection")
		return nil
	}
}

func waitForResult(
	t *testing.T,
	done <-chan redemptionResult,
) (*hummingbird.RedeemAssetFromASResponse, error) {
	t.Helper()
	select {
	case res := <-done:
		return res.resp, res.err
	case <-time.After(5 * time.Second):
		t.Fatal("Redeem never returned")
		return nil, nil
	}
}
