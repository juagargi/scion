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

package marketplace

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/marketplace/storage"
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
)

const redemptionChannelSize = 128

// redemptionTimeout bounds how long a client waits for the redemption service of
// an AS to answer. It is a deadline on top of the request context, so a client
// that disconnects earlier is noticed immediately.
const redemptionTimeout = 30 * time.Second

// RedemptionServerHandler routes the redemption requests of one AS,
// either to a redemption delegation held by the marketplace,
// or to the redemption server that the AS runs itself and connects over RedeemASAsset.
//
// Its state is guarded by mu.
// Every operation is short-lived, and none of them blocks while holding the lock,
// so a mutex serializes them just as a single owning goroutine would,
// without needing a message for every interaction.
type RedemptionServerHandler struct {
	ia    addr.IA
	store *storage.MarketplaceStorage

	mu sync.Mutex
	// The redemption delegation of this AS, if it granted one.
	// It may have expired, which is checked per request.
	local *RedemptionService
	// The connected redemption server of this AS, nil if none is connected.
	remote *RemoteConn
	// Requests handed to the remote redemption server and not answered yet,
	// keyed by the request id that correlates the two.
	pending       map[uint64]chan *hummingbird.RedeemAssetFromASResponse
	nextReqID     uint64
	reprovisioner Reprovisioner
}

// RemoteConn is one connection of an AS redemption server.
// Its queue belongs to the connection rather than to the handler,
// so that it is never closed or replaced while another goroutine still holds it:
// a connection's channels die with the connection.
type RemoteConn struct {
	// cancel stops the stream serving this connection.
	cancel context.CancelFunc
	// out carries the requests waiting to be sent to the redemption server.
	out chan *hummingbird.RedeemAssetFromASRequest
	// done is closed once the connection is no longer the active one.
	done chan struct{}
	once sync.Once // Used by close()
}

// close cancels the connection's stream and releases everyone waiting on it.
// It is safe to call more than once.
func (c *RemoteConn) close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		close(c.done)
	})
}

func NewRedemptionServerHandler(
	ia addr.IA,
	store *storage.MarketplaceStorage,
) *RedemptionServerHandler {
	return &RedemptionServerHandler{
		ia:      ia,
		store:   store,
		pending: make(map[uint64]chan *hummingbird.RedeemAssetFromASResponse),
	}
}

func unavailableResponse() *hummingbird.RedeemAssetFromASResponse {
	return &hummingbird.RedeemAssetFromASResponse{
		Result: &hummingbird.RedeemAssetFromASResponse_Error{
			Error: "Asset redemption currently not possible. Try again later.",
		},
	}
}

// Redeem asks the redemption service of this AS for a reservation and waits for
// the answer. It returns ctx.Err() if the caller gives up before the answer arrives,
// which also stops this request from occupying a slot in the pending requests.
// Cancelling ctx abandons only this request: the stream to the redemption server is
// shared by every request of this AS, and its lifetime belongs to RedeemASAsset.
func (h *RedemptionServerHandler) Redeem(
	ctx context.Context,
	req *hummingbird.RedeemAssetFromASRequest,
) (*hummingbird.RedeemAssetFromASResponse, error) {
	h.mu.Lock()
	if h.local != nil && h.local.expiration.After(time.Now()) {
		// The marketplace holds a delegation, so it redeems the asset itself.
		// Redeem only touches memory, so the lock is held across it.
		resp := h.local.Redeem(req)
		h.mu.Unlock()
		return resp, nil
	}

	// Using the AS redemption service.
	conn := h.remote
	if conn == nil {
		h.mu.Unlock()
		return unavailableResponse(), nil
	}

	h.nextReqID++
	id := h.nextReqID
	req.RequestId = id
	// Buffered, so that DeliverRedemptionResponse never blocks on a reply channel
	// whose reader has given up in the meantime.
	replyCh := make(chan *hummingbird.RedeemAssetFromASResponse, 1)
	h.pending[id] = replyCh
	h.mu.Unlock()

	// Hand the request over to the connection. Waiting on done means a connection
	// that goes away while its queue is full cannot block this caller forever.
	select {
	case conn.out <- req:
	case <-conn.done:
		h.dropPending(id)
		return unavailableResponse(), nil
	case <-ctx.Done():
		h.dropPending(id)
		return nil, ctx.Err()
	}

	// The redemption server of the AS answers, which arrives through
	// DeliverRedemptionResponse. A connection that goes away answers the pending
	// requests itself, through DetachRemote, so done needs no case of its own here.
	select {
	case resp := <-replyCh:
		return resp, nil
	case <-ctx.Done():
		h.dropPending(id)
		return nil, ctx.Err()
	}
}

// DeliverRedemptionResponse hands a response of the AS redempt. srv. to whoever is waiting for it.
// A response nobody waits for is dropped.
func (h *RedemptionServerHandler) DeliverRedemptionResponse(
	resp *hummingbird.RedeemAssetFromASResponse,
) {
	h.mu.Lock()
	reply, found := h.pending[resp.RequestId]
	delete(h.pending, resp.RequestId)
	h.mu.Unlock()
	if !found {
		log.Debug("no request waiting for this redemption response",
			"ia", h.ia, "request_id", resp.RequestId)
		return
	}
	reply <- resp
	close(reply)
}

// dropPending forgets a request without answering it, for when its caller is gone.
func (h *RedemptionServerHandler) dropPending(id uint64) {
	h.mu.Lock()
	delete(h.pending, id)
	h.mu.Unlock()
}

// takePendingLocked empties the pending requests and returns them, so that they
// can be answered without holding the lock.
// The caller must hold mu.
func (h *RedemptionServerHandler) takePendingLocked() (
	stale map[uint64]chan *hummingbird.RedeemAssetFromASResponse,
) {
	if len(h.pending) == 0 {
		return nil
	}
	stale = h.pending
	h.pending = make(map[uint64]chan *hummingbird.RedeemAssetFromASResponse)
	return stale
}

func failAll(pending map[uint64]chan *hummingbird.RedeemAssetFromASResponse) {
	for _, reply := range pending {
		reply <- unavailableResponse()
		close(reply)
	}
}

// AttachRemote makes a newly connected redemption server the one this handler routes to,
// closing whichever connection was there before.
// Cancelling the returned connection stops the stream that serves it.
func (h *RedemptionServerHandler) AttachRemote(cancel context.CancelFunc) *RemoteConn {
	conn := &RemoteConn{
		cancel: cancel,
		out:    make(chan *hummingbird.RedeemAssetFromASRequest, redemptionChannelSize),
		done:   make(chan struct{}),
	}
	h.mu.Lock()
	previous := h.remote
	h.remote = conn
	// The requests already handed to the previous connection will never be answered,
	// because only that connection knows their request ids.
	stale := h.takePendingLocked()
	h.mu.Unlock()

	if previous != nil {
		log.Debug("replacing an open redemption server connection", "ia", h.ia)
		previous.close()
	}
	failAll(stale)
	return conn
}

// DetachRemote stops routing to conn, if it is still the connection this handler routes to.
// A connection that has already been replaced leaves the newer one alone.
func (h *RedemptionServerHandler) DetachRemote(conn *RemoteConn) {
	h.mu.Lock()
	var stale map[uint64]chan *hummingbird.RedeemAssetFromASResponse
	if h.remote == conn {
		h.remote = nil
		stale = h.takePendingLocked()
	}
	h.mu.Unlock()
	conn.close()
	failAll(stale)
}

// CloseRedemptionServerConnection closes the connection of the redemption server of this AS,
// if one is open. The stream serving it ends on its own once its context is cancelled.
func (h *RedemptionServerHandler) CloseRedemptionServerConnection() {
	h.mu.Lock()
	conn := h.remote
	h.remote = nil
	stale := h.takePendingLocked()
	h.mu.Unlock()
	if conn != nil {
		conn.close()
	}
	failAll(stale)
}

// ApplyDelegation stores a redemption delegation of this AS and starts using it.
// The database work and the validation happen outside the lock, so that only
// installing the result is serialized against the redemption requests.
func (h *RedemptionServerHandler) ApplyDelegation(
	ctx context.Context,
	state *RedemptionDelegationUpdate,
) error {
	// Validated before anything is stored, so that a delegation the marketplace
	// cannot use never reaches the database. A stored one would be replayed by
	// NewService on every start, and would keep the marketplace from starting.
	if err := validateDelegationParams(state); err != nil {
		return err
	}
	dbDelegation := &db.RedemptionDelegation{
		IA:         h.ia,
		Expiration: state.ExpirationTime,
		ResIdLow:   state.IdLimitLow,
		ResIdHigh:  state.IdLimitHigh,
		Key:        state.Key,
	}
	dbDelegation.EncodeInts(state.EncodingPoints)
	_, err := h.store.CreateOrUpdateRedemptionDelegations(ctx, dbDelegation)
	if err != nil {
		return err
	}
	res, err := h.store.FindUsedReservations(ctx, &db.UsedReservationsQuery{
		IA:         h.ia,
		Limit_low:  state.IdLimitLow,
		Limit_high: state.IdLimitHigh,
	})
	if err != nil {
		return err
	}
	service, err := NewRedemptionService(state, res)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.local = service
	h.mu.Unlock()
	return nil
}

type Reprovisioner struct {
	mu        sync.Mutex
	operation *ReprovisionOperation
}

type ReprovisionOperation struct {
	cancelF   context.CancelFunc
	done      chan struct{}
	total     int64
	processed atomic.Int64
	status    hummingbird.ReprovisionState
	err       error
}

func (h *RedemptionServerHandler) ReprovisionCancel() error {
	h.reprovisioner.mu.Lock()
	op := h.reprovisioner.operation
	defer h.reprovisioner.mu.Unlock()

	if op == nil || op.status != hummingbird.ReprovisionState_Running {
		return serrors.New("no running reprovision operation")
	}
	op.cancelF()
	return nil
}

func (h *RedemptionServerHandler) ReprovisionReservationStatus() *hummingbird.ReprovisionStatusResponse {
	h.reprovisioner.mu.Lock()
	defer h.reprovisioner.mu.Unlock()
	if h.reprovisioner.operation == nil {
		return &hummingbird.ReprovisionStatusResponse{
			State:     hummingbird.ReprovisionState_Idle,
			Total:     0,
			Processed: 0,
		}
	}
	var err_string *string
	if h.reprovisioner.operation.err != nil {
		tmp := h.reprovisioner.operation.err.Error()
		err_string = &tmp
	}
	return &hummingbird.ReprovisionStatusResponse{
		State:     h.reprovisioner.operation.status,
		Total:     uint64(h.reprovisioner.operation.total),
		Processed: uint64(h.reprovisioner.operation.processed.Load()),
		Error:     err_string,
	}
}

func (h *RedemptionServerHandler) ReprovisionReservations() error {
	h.reprovisioner.mu.Lock()
	if h.reprovisioner.operation != nil &&
		h.reprovisioner.operation.status == hummingbird.ReprovisionState_Running {
		h.reprovisioner.mu.Unlock()
		return serrors.New("reprovision already in progress")
	}
	// the operation should not cancel if the requester disconnects
	ctx, cancelF := context.WithCancel(context.Background())
	defer cancelF()
	earliestStop := time.Now().Format(time.RFC3339)
	reservations, err := h.store.FetchReservations(ctx, &db.ReservationQuery{
		IA:      &h.ia,
		StopsAt: &earliestStop,
	})
	if err != nil {
		return err
	}
	log.Debug("reprovision", "ia", h.ia, "loaded reservations", len(reservations))
	op := &ReprovisionOperation{
		cancelF: cancelF,
		status:  hummingbird.ReprovisionState_Running,
		done:    make(chan struct{}),
		total:   int64(len(reservations)),
	}
	op.processed.Store(0)
	h.reprovisioner.operation = op
	h.reprovisioner.mu.Unlock()

	go func() {
		defer close(op.done)

		handleResponse := func(dbID int64, resp *hummingbird.RedeemAssetFromASResponse) error {
			switch result := resp.Result.(type) {
			case *hummingbird.RedeemAssetFromASResponse_ResInfo:
				n, err := h.store.UpdateReservation(ctx, dbID, &db.DBReservation{
					ID:               dbID,
					ReservationID:    result.ResInfo.ReservationId,
					Bandwidth:        result.ResInfo.BandwithRounded,
					EncodedBandwidth: uint16(result.ResInfo.BwDataplaneEncoding),
					Key:              result.ResInfo.AuthenticationKey,
				})
				if err != nil {
					return err
				}
				if n != 1 {
					return serrors.New("invalid number of rows modified")
				}
			case *hummingbird.RedeemAssetFromASResponse_Error:
				return serrors.New(result.Error)
			}
			return nil
		}
		g := errgroup.Group{}
		g.SetLimit(redemptionChannelSize)
	loop:
		for _, reservation := range reservations {
			select {
			case <-ctx.Done():
				break loop
			default:
			}
			g.Go(func() error {
				resp, err := h.Redeem(ctx, &hummingbird.RedeemAssetFromASRequest{
					IngressId: reservation.Ingress,
					EgressId:  reservation.Egress,
					Bandwidth: reservation.Bandwidth,
					StartsAt:  timestamppb.New(reservation.StartsAt),
					StopsAt:   timestamppb.New(reservation.StopsAt),
				})
				if err != nil {
					log.Debug("error while reprovisioning reservation", "err", err)
					return err
				}
				if err = handleResponse(reservation.ID, resp); err != nil {
					log.Debug("error while reprovisioning reservation", "err", err)
					return err
				}
				h.reprovisioner.operation.processed.Add(1)
				return nil
			})
		}
		err = g.Wait()

		h.reprovisioner.mu.Lock()
		defer h.reprovisioner.mu.Unlock()
		if err != nil {
			op.err = err
			if errors.Is(err, context.Canceled) {
				op.status = hummingbird.ReprovisionState_Canceled
			} else {
				op.status = hummingbird.ReprovisionState_Failed
			}
		} else {
			op.status = hummingbird.ReprovisionState_Completed
		}
	}()

	<-op.done
	return op.err
}

func (s *Service) RedeemASAsset(
	ctx context.Context,
	stream *connect.BidiStream[
		hummingbird.RedeemAssetFromASResponse,
		hummingbird.RedeemAssetFromASRequest],
) error {
	clientID, ok := ctx.Value("user").(addr.IA)
	if !ok {
		return connect.NewError(connect.CodePermissionDenied, serrors.New("ia not provided"))
	}
	cancelCtx, cancelF := context.WithCancel(ctx)
	handler := s.FindRedemptionServerHandler(clientID)
	log.Debug("AS redemption server connected", "ia", clientID)
	conn := handler.AttachRemote(cancelF)
	defer func() {
		log.Debug("AS redemption server disconnected", "ia", clientID)
		// Stops routing to this connection and releases the sender below.
		handler.DetachRemote(conn)
	}()

	// One goroutine to send the requests to the AS. It only waits on channels,
	// which is what cancelCtx can interrupt.
	go func() {
		for {
			select {
			case <-cancelCtx.Done():
				return
			case req := <-conn.out:
				if err := stream.Send(req); err != nil {
					// The connection is broken, so the receive below fails too,
					// and tears the stream down.
					log.Debug("Send error", "err", err)
					return
				}
			}
		}
	}()

	// stream.Receive blocks on a read of the request body, cannot be cancelled from the outside.
	// Receiving here means the stream instead ends by returning from this function,
	// which closes the body and releases the sender through cancelCtx.
	for {
		msg, err := stream.Receive()
		if err != nil {
			log.Debug("Receive error", "err", err)
			return nil
		}
		handler.DeliverRedemptionResponse(msg)
	}
}

func (s *Service) DelegateRedemption(
	ctx context.Context,
	req *connect.Request[hummingbird.DelegateRedemptionRequest],
) (*connect.Response[hummingbird.DelegateRedemptionResponse], error) {
	clientID, ok := ctx.Value("user").(addr.IA)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, serrors.New("ia not provided"))
	}
	handler := s.FindRedemptionServerHandler(clientID)
	err := handler.ApplyDelegation(ctx, &RedemptionDelegationUpdate{
		ExpirationTime: req.Msg.ExpirationTime.AsTime(),
		IdLimitLow:     req.Msg.ReservationIdLowerBound,
		IdLimitHigh:    req.Msg.ReservationIdUpperBound,
		Key:            req.Msg.Key,
		EncodingPoints: req.Msg.EncodingPoints,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return &connect.Response[hummingbird.DelegateRedemptionResponse]{
		Msg: &hummingbird.DelegateRedemptionResponse{
			ExpirationTime: req.Msg.ExpirationTime,
		},
	}, nil

}

// ReprovisionReservations will depend on the operation start the reprovisioning, return its status
// or cancel it.
// When starting reprovisioning, it processed the reprovisioning and returns success or an error.
// Reprovisioning continues even if the client disconnects and will terminute on completion
// or upon cancellation.
// Status returns the state of the current reprovisioning operation and the number of elements
// that were processed and how many elements in total are processed.
// Cancellation will cancel the current reprovision operation, but will not undo already performed
// reprovisions.
func (s *Service) ReprovisionReservations(
	ctx context.Context,
	req *connect.Request[hummingbird.ReprovisionReservationRequest],
) (*connect.Response[hummingbird.ReprovisionReservationResponse], error) {
	clientID, ok := ctx.Value("user").(addr.IA)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, serrors.New("ia not provided"))
	}
	handler := s.FindRedemptionServerHandler(clientID)

	var operationResult *hummingbird.ReprovisionReservationResponse
	switch req.Msg.Operation.(type) {
	case *hummingbird.ReprovisionReservationRequest_Start:
		if err := handler.ReprovisionReservations(); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		operationResult = &hummingbird.ReprovisionReservationResponse{
			Operation: &hummingbird.ReprovisionReservationResponse_Start{
				Start: &hummingbird.ReprovisionStartResponse{},
			},
		}
	case *hummingbird.ReprovisionReservationRequest_Status:
		operationResult = &hummingbird.ReprovisionReservationResponse{
			Operation: &hummingbird.ReprovisionReservationResponse_Status{
				Status: handler.ReprovisionReservationStatus(),
			},
		}
	case *hummingbird.ReprovisionReservationRequest_Cancel:
		if err := handler.ReprovisionCancel(); err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		operationResult = &hummingbird.ReprovisionReservationResponse{
			Operation: &hummingbird.ReprovisionReservationResponse_Cancel{
				Cancel: &hummingbird.ReprovisionCancelResponse{},
			},
		}
	}
	return &connect.Response[hummingbird.ReprovisionReservationResponse]{
		Msg: operationResult,
	}, nil
}
