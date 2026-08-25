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
	"time"

	"connectrpc.com/connect"
	"github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/marketplace/storage"
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
)

const redemptionChannelSize = 128

type RedemptionServerHandler struct {
	// variables of the redemption server handler
	ia                     addr.IA
	remoteCancelF          context.CancelFunc
	store                  *storage.MarketplaceStorage
	localRedemptionService *RedemptionService

	// A channel to signal when a remote redemption server connects.
	// It ensures that if another connection is still open, it gets closed.
	// Whenever writing to this channel, the writer must immediately read
	// from the remoteConnectionOpenResultChannel channel afterwards.
	remoteConnectionOpenChannel       chan context.CancelFunc
	remoteConnectionOpenResultChannel chan struct{}
	// Channel used to signal the closure of a redemption server connection
	// to the handler.
	remoteConnectionCloseChannel chan struct{}

	// channels responsible to receive and forward redemption requests
	requestInChannel  chan RedemptionRequest
	responseInChannel chan *hummingbird.RedeemAssetFromASResponse
	requestOutChannel chan *hummingbird.RedeemAssetFromASRequest

	// unbuffered channel
	delegationInChannel chan *RedemptionDelegationUpdate
	// unbuffered channel, must be read after sending a redemption delegation
	// update to the delegationInChannel
	delegationOutChannel chan error
}

type RedemptionRequest struct {
	Request      *hummingbird.RedeemAssetFromASRequest
	ReplyChannel chan *hummingbird.RedeemAssetFromASResponse
}

func NewRedemptionServerHandler(ia addr.IA, store *storage.MarketplaceStorage) *RedemptionServerHandler {
	handler := &RedemptionServerHandler{
		ia:                                ia,
		remoteConnectionOpenChannel:       make(chan context.CancelFunc),
		remoteConnectionOpenResultChannel: make(chan struct{}),
		remoteConnectionCloseChannel:      make(chan struct{}),
		requestInChannel:                  make(chan RedemptionRequest, redemptionChannelSize),
		responseInChannel:                 make(chan *hummingbird.RedeemAssetFromASResponse, redemptionChannelSize),
		delegationInChannel:               make(chan *RedemptionDelegationUpdate),
		delegationOutChannel:              make(chan error),
		requestOutChannel:                 make(chan *hummingbird.RedeemAssetFromASRequest, redemptionChannelSize),
		store:                             store,
	}
	go handler.run(context.Background())
	return handler
}

func (h *RedemptionServerHandler) QueueRedemptionRequest(req *hummingbird.RedeemAssetFromASRequest) <-chan *hummingbird.RedeemAssetFromASResponse {
	replyChannel := make(chan *hummingbird.RedeemAssetFromASResponse, 1)
	h.requestInChannel <- RedemptionRequest{
		Request:      req,
		ReplyChannel: replyChannel,
	}
	return replyChannel
}

func (h *RedemptionServerHandler) run(ctx context.Context) {
	pending := make(map[uint64]chan *hummingbird.RedeemAssetFromASResponse)
	currRequestID := uint64(0)
	cancelAllPending := func() {
		for _, replyChannel := range pending {
			replyChannel <- &hummingbird.RedeemAssetFromASResponse{
				Result: &hummingbird.RedeemAssetFromASResponse_Error{
					Error: "Asset redemption currently not possible. Try again later.",
				},
			}
			close(replyChannel)
		}
		clear(pending)
		close(h.requestOutChannel)
		close(h.responseInChannel)
		h.requestOutChannel = make(chan *hummingbird.RedeemAssetFromASRequest, redemptionChannelSize)
		h.responseInChannel = make(chan *hummingbird.RedeemAssetFromASResponse, redemptionChannelSize)
	}
	log.Debug("run handler", "ia", h.ia)
	for {
		select {
		case delegateRequest := <-h.delegationInChannel:
			log.Debug("delegationInChannel")
			err := h.applyRedemptionDelegationUpdate(ctx, delegateRequest)
			if err != nil {
				h.delegationOutChannel <- err
			}
			h.delegationOutChannel <- nil
		case redemptionRequest := <-h.requestInChannel:
			log.Debug("requestInChannel")
			if h.localRedemptionService != nil && h.localRedemptionService.expiration.After(time.Now()) {
				// we use redemption delegation
				redemptionRequest.ReplyChannel <- h.localRedemptionService.Redeem(redemptionRequest.Request)
			} else if h.remoteCancelF != nil {
				// we use AS redemption server
				currRequestID++
				redemptionRequest.Request.RequestId = currRequestID
				h.requestOutChannel <- redemptionRequest.Request
				// the handler cannot resolve the request, we store it as pending and wait until the
				// AS redemption server resolved it.
				pending[currRequestID] = redemptionRequest.ReplyChannel
			} else {
				// no valid redemption delegation exists and no redemption service is connected
				redemptionRequest.ReplyChannel <- &hummingbird.RedeemAssetFromASResponse{
					Result: &hummingbird.RedeemAssetFromASResponse_Error{
						Error: "Asset redemption currently not possible. Try again later.",
					},
				}
				close(redemptionRequest.ReplyChannel)
			}
		case redemptionResponse := <-h.responseInChannel:
			log.Debug("responseInChannel")
			if ch, ok := pending[redemptionResponse.RequestId]; ok {
				delete(pending, redemptionResponse.RequestId)
				ch <- redemptionResponse
				close(ch)
			}
		case cancelF := <-h.remoteConnectionOpenChannel:
			log.Debug("remoteConnectionOpenChannel")
			if h.remoteCancelF != nil {
				// some other connection is alreay open for that AS.
				// we first have to close the existing connection
				h.remoteCancelF()
				// now we just have to wait a moment until it reports closure
				_ = <-h.remoteConnectionCloseChannel
				cancelAllPending()
			}
			h.remoteCancelF = cancelF
			h.remoteConnectionOpenResultChannel <- struct{}{}
		case _ = <-h.remoteConnectionCloseChannel:
			log.Debug("remoteConnectionCloseChannel")
			if h.remoteCancelF != nil {
				h.remoteCancelF()
				h.remoteCancelF = nil
			}
			cancelAllPending()
		}
	}
}

func (h *RedemptionServerHandler) applyRedemptionDelegationUpdate(ctx context.Context, state *RedemptionDelegationUpdate) error {
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
	h.localRedemptionService = service
	return nil
}

func (s *Service) RedeemASAsset(ctx context.Context, stream *connect.BidiStream[hummingbird.RedeemAssetFromASResponse, hummingbird.RedeemAssetFromASRequest]) error {
	clientID, ok := ctx.Value("user").(addr.IA)
	if !ok {
		return connect.NewError(connect.CodePermissionDenied, serrors.New("ia not provided"))
	}
	cancelCtx, cancelF := context.WithCancel(ctx)
	handler := s.FindRedemptionServerHandler(clientID)
	log.Debug("AS redemption server connected", "ia", clientID)
	handler.remoteConnectionOpenChannel <- cancelF
	_ = <-handler.remoteConnectionOpenResultChannel
	defer func() {
		log.Debug("AS redemption server disconnected", "ia", clientID)
		handler.remoteConnectionCloseChannel <- struct{}{}
	}()

	_, err := stream.Receive()
	if err != nil {
		return err
	}
	go func() {
		for {
			select {
			case <-cancelCtx.Done():
				return
			default:
				msg, err := stream.Receive()
				if err != nil {
					log.Debug("Receive error", "err", err)
					return
				}
				select {
				case handler.responseInChannel <- msg:
				default:
					log.Debug("response from redemption server dropped due to full queue.")
				}
			}
		}
	}()
	for {
		select {
		case <-cancelCtx.Done():
			return nil
		case req := <-handler.requestOutChannel:
			if req == nil {
				return serrors.New("send channel closed")
			}
			if err := stream.Send(req); err != nil {
				return err
			}
		}
	}
}

func (s *Service) DelegateRedemption(ctx context.Context, req *connect.Request[hummingbird.DelegateRedemptionRequest]) (*connect.Response[hummingbird.DelegateRedemptionResponse], error) {
	clientID, ok := ctx.Value("user").(addr.IA)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, serrors.New("ia not provided"))
	}
	handler := s.FindRedemptionServerHandler(clientID)
	handler.delegationInChannel <- &RedemptionDelegationUpdate{
		ExpirationTime: req.Msg.ExpirationTime.AsTime(),
		IdLimitLow:     req.Msg.ReservationIdLowerBound,
		IdLimitHigh:    req.Msg.ReservationIdUpperBound,
		Key:            req.Msg.Key,
		EncodingPoints: req.Msg.EncodingPoints,
	}
	err := <-handler.delegationOutChannel
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return &connect.Response[hummingbird.DelegateRedemptionResponse]{
		Msg: &hummingbird.DelegateRedemptionResponse{
			ExpirationTime: req.Msg.ExpirationTime,
		},
	}, nil

}
