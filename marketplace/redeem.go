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
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/scionproto/scion/marketplace/db"
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
)

func (s *Service) newRedemptionServerPeer(ia addr.IA) *RedemptionServerPeer {
	peer := &RedemptionServerPeer{
		ia:      ia,
		sendCh:  make(chan *hummingbird.RedeemAssetFromASRequest, 64),
		pending: make(map[uint64]chan *hummingbird.RedeemAssetFromASResponse),
	}
	s.redemptionServerPeers[ia] = peer
	return peer
}

type RedemptionServerPeer struct {
	delegatedServer     *RedemptionService
	ia                  addr.IA
	sendCh              chan *hummingbird.RedeemAssetFromASRequest
	pending             map[uint64]chan *hummingbird.RedeemAssetFromASResponse
	requestID           uint64
	cancelOldConnection context.CancelFunc
	mtx                 sync.Mutex
}

func (c *RedemptionServerPeer) Send(req *hummingbird.RedeemAssetFromASRequest) <-chan *hummingbird.RedeemAssetFromASResponse {
	respCh := make(chan *hummingbird.RedeemAssetFromASResponse, 1)
	c.mtx.Lock()
	req.RequestId = c.requestID
	c.requestID++
	c.pending[req.RequestId] = respCh
	c.mtx.Unlock()
	c.sendCh <- req
	return respCh
}

func (c *RedemptionServerPeer) ReturnResponse(msg *hummingbird.RedeemAssetFromASResponse) {
	reqID := msg.RequestId
	c.mtx.Lock()
	defer c.mtx.Unlock()
	ch, ok := c.pending[reqID]
	if ok {
		ch <- msg
		close(ch)
		delete(c.pending, reqID)
	}
}

func (s *Service) startOrUpdateRedemptionDelegation(ctx context.Context, clientID addr.IA, state *RedemptionDelegationUpdate, dbSync bool) error {
	fmt.Println("startOrUpdateRedemptionDelegation", clientID)
	s.mtx.Lock()
	defer s.mtx.Unlock()
	client, found := s.redemptionServerPeers[clientID]
	var err error
	if !found {
		// new peer
		client = s.newRedemptionServerPeer(clientID)
		client.mtx.Lock()
		defer client.mtx.Unlock()
		s.redemptionServerPeers[clientID] = client
		if dbSync {
			dbDelegation := &db.RedemptionDelegation{
				IA:         clientID,
				Expiration: state.ExpirationTime,
				ResIdLow:   state.IdLimitLow,
				ResIdHigh:  state.IdLimitHigh,
				Key:        state.Key,
			}
			dbDelegation.EncodeInts(state.EncodingPoints)
			_, err = s.store.CreateOrUpdateRedemptionDelegations(ctx, dbDelegation)
			if err != nil {
				return err
			}
		}
		client.delegatedServer, err = NewRedemptionService(ctx, client, s.store, clientID, state, client.sendCh, client.pending)
		if err != nil {
			return err
		}

	} else if client.delegatedServer != nil {
		// the redemption server was already delegated, but we received an update request
		client.mtx.Lock()
		defer client.mtx.Unlock()
		if dbSync {
			dbDelegation := &db.RedemptionDelegation{
				IA:         clientID,
				Expiration: state.ExpirationTime,
				ResIdLow:   state.IdLimitLow,
				ResIdHigh:  state.IdLimitHigh,
				Key:        state.Key,
			}
			dbDelegation.EncodeInts(state.EncodingPoints)
			_, err = s.store.CreateOrUpdateRedemptionDelegations(ctx, dbDelegation)
			if err != nil {
				return err
			}
		}
		client.delegatedServer.UpdateChannel <- state
		err = <-client.delegatedServer.UpdateResultChannel
		if err != nil {
			return err
		}
	} else {
		// the redemption server was previously run by the AS, now it is delegated
		client.mtx.Lock()
		defer client.mtx.Unlock()
		if client.cancelOldConnection != nil {
			client.cancelOldConnection()
		}
		if dbSync {
			dbDelegation := &db.RedemptionDelegation{
				IA:         clientID,
				Expiration: state.ExpirationTime,
				ResIdLow:   state.IdLimitLow,
				ResIdHigh:  state.IdLimitHigh,
				Key:        state.Key,
			}
			dbDelegation.EncodeInts(state.EncodingPoints)
			_, err = s.store.CreateOrUpdateRedemptionDelegations(ctx, dbDelegation)
			if err != nil {
				return err
			}
		}
		client.delegatedServer, err = NewRedemptionService(ctx, client, s.store, clientID, state, client.sendCh, client.pending)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) stopRedemptionDelegation(ctx context.Context, clientID addr.IA) error {
	fmt.Println("stopRedemptionDelegation", clientID)
	s.mtx.Lock()
	defer s.mtx.Unlock()
	client, found := s.redemptionServerPeers[clientID]
	if !found {
		return serrors.New("cannot stop redemption delegation of unkown peer", "ia", clientID)
	}
	client.mtx.Lock()
	defer client.mtx.Unlock()
	if client.delegatedServer == nil {
		return serrors.New("cannot stop redemption delegation without active delegation")
	}
	client.delegatedServer.UpdateChannel <- &RedemptionDelegationUpdate{ExpirationTime: time.Time{}}
	_ = <-client.delegatedServer.UpdateResultChannel
	client.delegatedServer = nil
	_, err := s.store.CreateOrUpdateRedemptionDelegations(ctx, &db.RedemptionDelegation{
		IA:         clientID,
		Expiration: time.Time{},
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) DelegateRedemption(ctx context.Context, req *connect.Request[hummingbird.DelegateRedemptionRequest]) (*connect.Response[hummingbird.DelegateRedemptionResponse], error) {
	fmt.Println("DelegateRedemption")
	clientID, ok := ctx.Value("user").(addr.IA)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, serrors.New("ia not provided"))
	}
	expTime := req.Msg.ExpirationTime.AsTime()
	if expTime.Before(time.Now()) {
		err := s.stopRedemptionDelegation(ctx, clientID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return &connect.Response[hummingbird.DelegateRedemptionResponse]{
			Msg: &hummingbird.DelegateRedemptionResponse{},
		}, nil
	}
	err := s.startOrUpdateRedemptionDelegation(ctx, clientID, &RedemptionDelegationUpdate{
		ExpirationTime: req.Msg.ExpirationTime.AsTime(),
		IdLimitLow:     req.Msg.ReservationIdLowerBound,
		IdLimitHigh:    req.Msg.ReservationIdUpperBound,
		Key:            req.Msg.Key,
		EncodingPoints: req.Msg.EncodingPoints,
	}, true)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return &connect.Response[hummingbird.DelegateRedemptionResponse]{
		Msg: &hummingbird.DelegateRedemptionResponse{
			ExpirationTime: req.Msg.ExpirationTime,
		},
	}, nil
}

func (s *Service) RedeemASAsset(ctx context.Context, stream *connect.BidiStream[hummingbird.RedeemAssetFromASResponse, hummingbird.RedeemAssetFromASRequest]) error {
	fmt.Println("RedeemAsset (AS)")
	clientID, ok := ctx.Value("user").(addr.IA)
	if !ok {
		return connect.NewError(connect.CodePermissionDenied, serrors.New("ia not provided"))
	}
	s.mtx.Lock()
	client, found := s.redemptionServerPeers[clientID]
	if !found {
		client = s.newRedemptionServerPeer(clientID)
		s.redemptionServerPeers[clientID] = client
	}
	s.mtx.Unlock()
	s.stopRedemptionDelegation(ctx, clientID)
	client.mtx.Lock()
	if client.cancelOldConnection != nil {
		client.cancelOldConnection()
	}
	cancelCtx, cancelF := context.WithCancel(ctx)
	client.cancelOldConnection = cancelF
	client.mtx.Unlock()
	_, err := stream.Receive()
	if err != nil {
		return err
	}
	fmt.Println("AS redemption service connected:", clientID)

	go func() {
		for {
			select {
			case <-cancelCtx.Done():
				return
			default:
				msg, err := stream.Receive()
				if err != nil {
					fmt.Println("receive error", err)
					return
				}
				if client.delegatedServer != nil {
					continue
				}
				client.ReturnResponse(msg)
			}
		}
	}()
	for {
		select {
		case <-cancelCtx.Done():
			return nil
		case req := <-client.sendCh:
			if req == nil {
				return serrors.New("send channel closed")
			}
			if client.delegatedServer != nil {
				continue
			}
			if err := stream.Send(req); err != nil {
				return err
			}
		}
	}
}
