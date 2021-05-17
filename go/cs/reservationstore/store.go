// Copyright 2020 ETH Zurich, Anapaya Systems
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

package reservationstore

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	base "github.com/scionproto/scion/go/cs/reservation"
	"github.com/scionproto/scion/go/cs/reservation/e2e"
	"github.com/scionproto/scion/go/cs/reservation/segment"
	"github.com/scionproto/scion/go/cs/reservation/segment/admission"
	"github.com/scionproto/scion/go/cs/reservation/translate"
	"github.com/scionproto/scion/go/cs/reservationstorage"
	"github.com/scionproto/scion/go/cs/reservationstorage/backend"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri"
	"github.com/scionproto/scion/go/lib/colibri/coliquic"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/scrypto"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/topology"
	libgrpc "github.com/scionproto/scion/go/pkg/grpc"
)

// Store is the reservation store.
type Store struct {
	// TODO(juagargi) bind the logger to use the localIA in messages
	localIA    addr.IA
	db         backend.DB                      // aka reservation map
	admitter   admission.Admitter              // the chosen admission entity
	operator   *coliquic.ServiceClientOperator // dials next colibri service
	colibriKey []byte                          // colibri secret key
}

var _ reservationstorage.Store = (*Store)(nil)

// NewStore creates a new reservation store.
func NewStore(topo topology.Topology, router snet.Router, arw libgrpc.AddressRewriter,
	dialer coliquic.GRPCClientDialer, db backend.DB, admitter admission.Admitter,
	masterKey []byte) (*Store, error) {

	// check that the admitter is well configured
	cap := admitter.Capacities()
	for _, ifid := range append(topo.InterfaceIDs(), 0) {
		log.Info("colibri admission capacity", "ifid", ifid,
			"ingress", cap.CapacityIngress(uint16(ifid)), "egress", cap.CapacityEgress(uint16(ifid)))
	}
	operator, err := coliquic.NewServiceClientOperator(topo, router, arw, dialer)
	if err != nil {
		return nil, err
	}
	colibriKey, err := scrypto.DeriveColibriMacKey(masterKey)
	if err != nil {
		return nil, err
	}
	return &Store{
		localIA:    topo.IA(),
		db:         db,
		admitter:   admitter,
		operator:   operator,
		colibriKey: colibriKey,
	}, nil
}

func (s *Store) GetSegmentRsvsFromSrcDstIA(ctx context.Context, src, dst addr.IA) (
	[]*segment.Reservation, error) {

	return s.db.GetSegmentRsvsFromSrcDstIA(ctx, src, dst)
}

func (s *Store) err(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(fmt.Sprintf("@%s: ", s.localIA) + err.Error())
}

func (s *Store) errNew(msg string, params ...interface{}) error {
	return s.err(serrors.New(msg, params...))
}

func (s *Store) errWrapStr(msg string, err error, params ...interface{}) error {
	return s.err(serrors.WrapStr(msg, err, params...))
}

// InitSegmentReservation will start a new segment reservation request. The source of
// the request will have this very AS as source.
func (s *Store) InitSegmentReservation(ctx context.Context, req *segment.SetupReq) error {
	if req.IsLastAS() {
		return s.errNew("cannot initiate a reservation with this AS only in the path")
	}
	newSetup := true
	log.Info("deleteme path current step", "curr.step", req.Path.CurrentStep)
	if req.ID.IsEmpty() {
		return serrors.New("bad empty ID")
	}
	if req.ID.ASID != s.localIA.A {
		return s.errNew("bad reservation id", "as", req.ID.ASID)
	}
	if !req.ID.IsEmptySuffix() {
		newSetup = false
	}

	rsv, err := s.db.GetSegmentRsvFromID(ctx, &req.ID)
	if err != nil {
		return s.errWrapStr("cannot obtain segment reservation", err, "id", req.ID)
	}
	if rsv != nil && newSetup {
		return s.errNew("found existing reservation in db for a new setup", "id", req.ID)
	} else if rsv == nil && !newSetup {
		return s.errNew("reservation not found for a renewal", "id", req.ID)
	}

	origPath := req.Request.Path.Copy()
	rollbackChanges := func() {
		// uses the `req` that will have the new ID and index, but the original path
		req := &segment.Request{
			MsgId: req.MsgId,
			Path:  origPath,
		}
		var res base.Response
		var err error
		if newSetup {
			res, err = s.TearDownSegmentReservation(ctx, req)
		} else {
			res, err = s.CleanupSegmentReservation(ctx, req)
		}
		log.Debug("cleaning reservations down the path", "new_setup", newSetup,
			"res", res, "err", err)
		if err != nil {
			log.Error("while cleaning reservations down the path an error occurred",
				"new_setup", newSetup, "err", err, "res", res)
		} else if _, ok := res.(*base.ResponseSuccess); !ok {
			log.Error("while cleaning reservations down the path, received failure response",
				"new_setup", newSetup, "res", res)
		}
	}
	res, err := s.admitSegmentReservation(ctx, req)
	if err != nil {
		log.Info("deleteme admit segment returned error", "err", err)
		rollbackChanges()
		return err
	}
	if _, ok := res.(*segment.SegmentSetupResponseSuccess); !ok {
		log.Info("deleteme admit segment returned failure", "res", res)
		rollbackChanges()
		return serrors.New("failure in setup", "response", res)
	}
	suc := res.(*segment.SegmentSetupResponseSuccess)
	log.Info("deleteme $$$$$$$$$ TOKEN $$$$$$$$$ TOKEN $$$$$$$$$", "token", suc.Token)

	return nil
}

// AdmitSegmentReservation receives a setup/renewal request to admit a segment reservation.
// It is expected that this AS is not the reservation initiator.
func (s *Store) AdmitSegmentReservation(ctx context.Context, req *segment.SetupReq) (
	segment.SegmentSetupResponse, error) {

	if err := s.validateAuthenticators(&req.Request); err != nil {
		return nil, s.errWrapStr("error validating request", err, "id", req.ID)
	}
	return s.admitSegmentReservation(ctx, req)
}

// ConfirmSegmentReservation changes the state of an index from temporary to confirmed.
func (s *Store) ConfirmSegmentReservation(ctx context.Context, req *segment.Request) (
	base.Response, error) {

	if err := s.validateAuthenticators(req); err != nil {
		return nil, s.errWrapStr("error validating request", err, "id", req.ID)
	}

	failedResponse := s.prepareFailureResp("failed to confirm index")

	if err := req.Validate(); err != nil {
		failedResponse.Message = "request validation failed: " + err.Error()
		return failedResponse, nil
	}

	tx, err := s.db.BeginTransaction(ctx, nil)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot create transaction", err, "id", req.ID)
	}
	defer tx.Rollback()

	rsv, err := tx.GetSegmentRsvFromID(ctx, &req.ID)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot obtain segment reservation", err,
			"id", req.ID)
	}
	if rsv == nil {
		failedResponse.Message = "no reservation found"
		return failedResponse, nil
	}
	if err := rsv.SetIndexConfirmed(req.Index); err != nil {
		return failedResponse, s.errWrapStr("cannot set index to confirmed", err,
			"id", req.ID)
	}
	if err = tx.PersistSegmentRsv(ctx, rsv); err != nil {
		return failedResponse, s.errWrapStr("cannot persist segment reservation", err,
			"id", req.ID)
	}
	if err := tx.Commit(); err != nil {
		return failedResponse, s.errWrapStr("cannot commit transaction", err,
			"id", req.ID)
	}

	if req.IsLastAS() {
		return &base.ResponseSuccess{}, nil
	}
	// forward to next colibri service
	client, err := s.operator.ColibriClient(ctx, req.Path)
	if err != nil {
		return failedResponse, s.errWrapStr("while finding a colibri service client", err)
	}

	pbRes, err := client.ConfirmSegmentIndex(ctx, translate.PBufRequest(req))
	if err != nil {
		return failedResponse, s.errWrapStr("forwarded request failed", err)
	}
	return translate.Response(pbRes), nil
}

// CleanupSegmentReservation deletes an index from a segment reservation.
func (s *Store) CleanupSegmentReservation(ctx context.Context, req *segment.Request) (
	base.Response, error) {

	if err := s.validateAuthenticators(req); err != nil {
		return nil, s.errWrapStr("error validating request", err, "id", req.ID)
	}

	failedResponse := s.prepareFailureResp("failed to cleanup index")

	if err := req.Validate(); err != nil {
		failedResponse.Message = "request validation failed: " + err.Error()
		return failedResponse, nil
	}

	tx, err := s.db.BeginTransaction(ctx, nil)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot create transaction", err, "id", req.ID)
	}
	defer tx.Rollback()

	rsv, err := tx.GetSegmentRsvFromID(ctx, &req.ID)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot obtain segment reservation", err,
			"id", req.ID)
	}
	if rsv == nil {
		failedResponse.Message = "no reservation found"
		return failedResponse, nil
	}

	if err := rsv.RemoveIndex(req.Index); err != nil {
		return failedResponse, s.errWrapStr("cannot delete segment reservation index", err,
			"id", req.ID, "index", req.Index)
	}
	if err = tx.PersistSegmentRsv(ctx, rsv); err != nil {
		return failedResponse, s.errWrapStr("cannot persist segment reservation", err,
			"id", req.ID)
	}
	if err := tx.Commit(); err != nil {
		return failedResponse, s.errWrapStr("cannot commit transaction", err,
			"id", req.ID)
	}

	if req.IsLastAS() {
		return &base.ResponseSuccess{}, nil
	}
	// forward to next colibri service
	client, err := s.operator.ColibriClient(ctx, req.Path)
	if err != nil {
		return failedResponse, s.errWrapStr("while finding a colibri service client", err)
	}

	pbRes, err := client.CleanupSegmentIndex(ctx, translate.PBufRequest(req))
	if err != nil {
		return failedResponse, s.errWrapStr("forwarded request failed", err)
	}
	return translate.Response(pbRes), nil
}

// TearDownSegmentReservation removes a whole segment reservation.
func (s *Store) TearDownSegmentReservation(ctx context.Context, req *segment.Request) (
	base.Response, error) {

	log.Info("deleteme deleteme 1")

	if err := s.validateAuthenticators(req); err != nil {
		return nil, s.errWrapStr("error validating request", err, "id", req.ID)
	}
	log.Info("deleteme deleteme 2")

	failedResponse := s.prepareFailureResp("failed to teardown segment")

	if err := req.Validate(); err != nil {
		failedResponse.Message = "request validation failed: " + err.Error()
		return failedResponse, nil
	}
	log.Info("deleteme deleteme 3")

	tx, err := s.db.BeginTransaction(ctx, nil)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot create transaction", err, "id", req.ID)
	}
	defer tx.Rollback()

	log.Info("deleteme deleteme 4")

	if err := tx.DeleteSegmentRsv(ctx, &req.ID); err != nil {
		return failedResponse, s.errWrapStr("cannot teardown reservation", err,
			"id", req.ID)
	}
	log.Info("deleteme deleteme 5")

	if err := tx.Commit(); err != nil {
		return failedResponse, s.errWrapStr("cannot commit transaction", err,
			"id", req.ID)
	}

	log.Info("deleteme deleteme 6")

	if req.IsLastAS() {
		log.Info("deleteme deleteme 7")

		return &base.ResponseSuccess{}, nil
	}
	// forward to next colibri service
	client, err := s.operator.ColibriClient(ctx, req.Path)
	log.Info("deleteme deleteme 8", "err", err)
	if err != nil {
		return failedResponse, s.errWrapStr("while finding a colibri service client", err)
	}
	log.Info("deleteme deleteme 9")

	pbRes, err := client.TeardownSegment(ctx, translate.PBufRequest(req))
	log.Info("deleteme deleteme 10", "pbres", pbRes)
	if err != nil {
		log.Info("deleteme deleteme 11", "err", err)
		return failedResponse, s.errWrapStr("forwarded request failed", err)
	}
	return translate.Response(pbRes), nil
}

// AdmitE2EReservation will attempt to admit an e2e reservation.
func (s *Store) AdmitE2EReservation(ctx context.Context, request e2e.SetupRequest) (
	base.Response, error) {

	req := request.GetCommonSetupReq()
	failedResponse := s.prepareFailureResp("cannot admit e2e reservation")

	// sanity check: all successful requests are SetupReqSuccess. Failed ones are SetupReqFailure.
	if request.IsSuccessful() {
		if _, ok := request.(*e2e.SetupReqSuccess); !ok {
			return failedResponse, s.errNew("logic error, successful request can be casted")
		}
	} else {
		if _, ok := request.(*e2e.SetupReqFailure); !ok {
			return failedResponse, s.errNew("logic error, failed request can be casted")
		}
	}

	if len(req.SegmentRsvs) == 0 || len(req.SegmentRsvs) > 3 {
		return failedResponse, s.errNew("invalid number of segment reservations for an e2e one",
			"count", len(req.SegmentRsvs))
	}

	tx, err := s.db.BeginTransaction(ctx, nil)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot create transaction", err,
			"id", req.ID.String())
	}
	defer tx.Rollback()

	rsv, err := tx.GetE2ERsvFromID(ctx, &req.ID)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot obtain e2e reservation", err,
			"id", req.ID.String())
	}

	segRsvIDs := req.SegmentRsvIDsForThisAS()
	if rsv != nil {
		// renewal
		if index := rsv.Index(req.Index); index != nil {
			return failedResponse, s.errNew("already existing e2e index", "id", req.ID.String(),
				"idx", req.Index)
		}
	} else {
		// new setup
		rsv = &e2e.Reservation{
			ID:                  req.ID,
			SegmentReservations: make([]*segment.Reservation, len(segRsvIDs)),
		}
		for i, id := range segRsvIDs {
			r, err := tx.GetSegmentRsvFromID(ctx, &id)
			if err != nil || r == nil {
				return failedResponse, s.errWrapStr("cannot get segment rsv for e2e admission",
					err, "e2e_id", req.ID.String(), "seg_id", id.String())
			}
			rsv.SegmentReservations[i] = r
		}
	}
	if len(rsv.SegmentReservations) == 0 {
		return failedResponse, s.errNew("there is no segment rsv. associated to this e2e rsv.",
			"id", req.ID.String(), "idx", req.Index)
	} else {
		for i, r := range rsv.SegmentReservations {
			if r == nil {
				return failedResponse, s.errNew("there is no segment rsv. associated to "+
					"this e2e rsv.", "id", req.ID.String(), "seg_id", segRsvIDs[i].String())
			}
			if r.ActiveIndex() == nil {
				return failedResponse, s.errNew("seg. rsv. for e2e rsv has no active index",
					"id", req.ID.String(), "seg_id", r.ID.String())
			}
		}
	}

	idx, err := rsv.NewIndex(req.Timestamp)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot create index in e2e admission", err,
			"e2e_id", req.ID.String())
	}
	index := rsv.Index(idx)
	index.AllocBW = req.RequestedBW
	if request.IsSuccessful() {
		index.Token = &request.(*e2e.SetupReqSuccess).Token
	}

	free, err := freeInSegRsv(ctx, tx, rsv.SegmentReservations[0])
	if err != nil {
		return failedResponse, s.errWrapStr("cannot compute free bw for e2e admission", err,
			"e2e_id", rsv.ID.String())
	}
	free = free + rsv.AllocResv() // don't count this E2E request in the used BW

	if req.Transfer() {
		// this AS must stitch two segment rsvs. according to the request
		if len(segRsvIDs) == 1 {
			return failedResponse, s.errNew("e2e setup request with transfer inconsistent",
				"e2e_id", req.ID.String(), "req_sgmt_rsvs_count", req.SegmentRsvASCount,
				"trail_len", len(req.AllocationTrail))
		}
		freeOutgoing, err := freeAfterTransfer(ctx, tx, rsv)
		if err != nil {
			return failedResponse, s.errWrapStr("cannot compute transfer", err,
				"id", req.ID.String())
		}
		freeOutgoing += rsv.AllocResv() // do not count this rsv's BW
		if free > freeOutgoing {
			free = freeOutgoing
		}
	}

	// TODO(juagargi) fix response type
	// if !request.IsSuccessful() || req.RequestedBW.ToKbps() > free {
	// 	maxWillingToAlloc := reservation.BWClsFromBW(free)
	// 	if req.Location() == e2e.Destination {
	// 		asAResponse := failedResponse.(*e2e.ResponseSetupFailure)
	// 		asAResponse.MaxBWs = append(asAResponse.MaxBWs, maxWillingToAlloc)
	// 	} else {
	// 			asARequest := &e2e.SetupReqFailure{
	// 				SetupReq:  *req,
	// 				ErrorCode: 1,
	// 			}
	// 			asARequest.AllocationTrail = append(asARequest.AllocationTrail, maxWillingToAlloc)
	// 			failedResponse = asARequest
	// 	}
	// 	return failedResponse, s.errWrapStr("e2e not admitted", err, "id", req.ID.String(),
	// 		"index", req.Index)
	// }

	// // admitted so far
	// // TODO(juagargi) update token here
	// if err := tx.PersistE2ERsv(ctx, rsv); err != nil {
	// 	return failedResponse, s.errWrapStr("cannot persist e2e reservation", err,
	// 		"id", req.ID.String())
	// }

	// if err := tx.Commit(); err != nil {
	// 	return failedResponse, s.errWrapStr("cannot commit transaction", err,
	// 		"id", req.ID.String())
	// }

	// var msg base.MessageWithPath
	// if req.Location() == e2e.Destination {
	// 	asAResponse := failedResponse.(*e2e.ResponseSetupFailure)
	// 	msg = &e2e.ResponseSetupSuccess{
	// 		Response: *morphE2EResponseToSuccess(&asAResponse.Response),
	// 		Token:    *index.Token,
	// 	}
	// } else {
	// 	msg = &e2e.SetupReqSuccess{
	// 		SetupReq: *req,
	// 		Token:    *index.Token,
	// 	}
	// }
	// return msg, nil
	return &base.ResponseSuccess{}, nil
}

// CleanupE2EReservation will remove an index from an e2e reservation.
func (s *Store) CleanupE2EReservation(ctx context.Context, req *e2e.CleanupReq) (
	base.Response, error) {

	// if err := s.validateAuthenticators(&req.RequestMetadata); err != nil {
	// 	return nil, s.errWrapStr("error validating request", err, "id", req.ID)
	// }
	failedResponse := s.prepareFailureResp("cannot cleanup e2e reservation")

	tx, err := s.db.BeginTransaction(ctx, nil)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot create transaction", err, "id", req.ID)
	}
	defer tx.Rollback()

	rsv, err := tx.GetE2ERsvFromID(ctx, &req.ID)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot obtain e2e reservation", err,
			"id", req.ID)
	}
	if err := rsv.RemoveIndex(req.Index); err != nil {
		return failedResponse, s.errWrapStr("cannot delete e2e reservation index", err,
			"id", req.ID, "index", req.Index)
	}
	if err := tx.PersistE2ERsv(ctx, rsv); err != nil {
		return failedResponse, s.errWrapStr("cannot persist e2e reservation", err,
			"id", req.ID)
	}
	if err := tx.Commit(); err != nil {
		return failedResponse, s.errWrapStr("cannot commit transaction", err,
			"id", req.ID)
	}

	// if req.Request.IsLastAS() {
	// 	return &e2e.ResponseCleanupSuccess{
	// 		Response: *morphE2EResponseToSuccess(response),
	// 	}, nil
	// }

	return &base.ResponseSuccess{}, nil
}

// DeleteExpiredIndices will just call the DB's method to delete the expired indices.
func (s *Store) DeleteExpiredIndices(ctx context.Context) (int, time.Time, error) {
	n, err := s.db.DeleteExpiredIndices(ctx, time.Now())
	if err != nil {
		return 0, time.Time{}, err
	}
	exp, err := s.db.NextExpirationTime(ctx)
	return n, exp, err
}

// validateAuthenticators checks that the authenticators are correct.
func (s *Store) validateAuthenticators(req *segment.Request) error {
	// TODO(juagargi) validate request
	// DRKey authentication of request (will be left undone for later)
	return nil
}

// prepareFailureResp will create a failure response, which
// is sent in the reverse path that the request had.
func (s *Store) prepareFailureResp(message string) *base.ResponseFailure {
	return &base.ResponseFailure{
		Message: message,
	}
}

func (s *Store) admitSegmentReservation(ctx context.Context, req *segment.SetupReq) (
	segment.SegmentSetupResponse, error) {

	log.Info("deleteme 1 admit segment reservation")
	failedResponse := &segment.SegmentSetupResponseFailure{
		MsgId: base.MsgId{
			ID:        req.ID,
			Index:     req.Index,
			Timestamp: time.Now(),
		},
		FailedRequest: req,
	}

	if err := req.Validate(); err != nil {
		failedResponse.Message = "request failed validation: " + err.Error()
		return failedResponse, nil
	}
	log.Info("deleteme 2 admit segment reservation", "id", req.ID, "curr_step", req.Path.CurrentStep)

	if req.ID.IsEmptySuffix() && req.Path.CurrentStep != 0 {
		failedResponse.Message = "empty suffix not allowed if not at source AS"
		return failedResponse, nil
	}

	log.Info("deleteme 3 admit segment reservation")

	tx, err := s.db.BeginTransaction(ctx, nil)
	if err != nil {
		failedResponse.Message = "cannot create transaction: " + err.Error()
		return failedResponse, s.errWrapStr("cannot create transaction", err, "id", req.ID)
	}
	defer tx.Rollback()

	log.Info("deleteme 4 admit segment reservation")

	rsv, err := tx.GetSegmentRsvFromID(ctx, &req.ID)
	if err != nil {
		failedResponse.Message = "looking for reservation: " + err.Error()
		return failedResponse, s.errWrapStr("looking for reservation", err, "id", req.ID)
	}

	log.Info("deleteme 5 admit segment reservation", "req_id", req.ID, "rsv", rsv)

	if rsv != nil { // renewal, ensure index is not used
		if rsv.Index(req.Index) != nil {
			failedResponse.Message = fmt.Sprintf("index from setup already in use: %d", req.Index)
			return failedResponse, nil
		}
	} else {
		rsv = segment.NewReservation(req.ID.ASID)
		rsv.ID = req.ID
		rsv.Ingress = req.Ingress()
		rsv.Egress = req.Egress()
		rsv.PathType = req.PathType
		rsv.PathEndProps = req.PathProps
		rsv.TrafficSplit = req.SplitCls
	}
	req.Reservation = rsv
	log.Info("deleteme 6 admit segment reservation")

	if err := req.ValidateForReservation(rsv); err != nil {
		failedResponse.Message = "error validating request with reservation: " + err.Error()
		return failedResponse, nil
	}

	log.Debug("deleteme 10")
	// compute admission max BW
	err = s.admitter.AdmitRsv(ctx, tx, req)
	if err != nil {
		failedResponse.Message = "segment not admitted: " + err.Error()
		return failedResponse, nil
	}
	// admitted; the request contains already the value inside the "allocation beads" of the rsv
	allocBW := req.AllocTrail[len(req.AllocTrail)-1].AllocBW
	log.Info("deleteme 12", "req.Reservation.Pathtype", req.Reservation.PathType, "req.pathtype", req.PathType)

	idx, err := rsv.NewIndex(req.ExpirationTime, req.MinBW, req.MaxBW, allocBW,
		req.RLC, req.Reservation.PathType)
	if err != nil {
		failedResponse.Message = "cannot create new index: " + err.Error()
		return failedResponse, nil
	}
	index := rsv.Index(idx)
	log.Info("deleteme 13")
	log.Info("deleteme token inside index", "token", index.Token)
	log.Info("deleteme", "id", rsv.ID)

	if req.ID.IsEmptySuffix() && req.Path.CurrentStep == 0 {
		log.Info("deleteme 14")
		if err = tx.NewSegmentRsv(ctx, rsv); err != nil { // get a new suffix right now
			failedResponse.Message = "error creating new reservation at source: " + err.Error()
			return failedResponse, s.err(err)
		}
		req.ID = rsv.ID
	} else if err = tx.PersistSegmentRsv(ctx, rsv); err != nil {
		failedResponse.Message = "cannot persist segment reservation: " + err.Error()
		return failedResponse, s.errWrapStr("persisting segment reservation", err)
	}
	if err := tx.Commit(); err != nil {
		log.Info("deleteme 15")
		failedResponse.Message = "cannot commit transaction: " + err.Error()
		return failedResponse, s.errWrapStr("cannot commit transaction", err)
	}

	log.Debug("deleteme 16", "id", rsv.ID)
	var token *reservation.Token
	if req.IsLastAS() {
		token = index.Token
	} else {
		// forward the request to the next COLIBRI service
		log.Info("deleteme dialing grpc")
		client, err := s.operator.ColibriClient(ctx, req.Path)
		if err != nil {
			failedResponse.Message = "error forwarding request: " + err.Error()
			return failedResponse, s.errWrapStr("while finding a colibri service client", err)
		}

		log.Debug("deleteme 19", "id", req.ID)
		pbRes, err := client.SetupSegment(ctx, translate.PBufSetupReq(req))
		log.Info("deleteme store received a response to the setup request", "pbres", pbRes, "err", err)
		if err != nil {
			failedResponse.Message = "error in forwarded request: " + err.Error()
			return failedResponse, s.errWrapStr("forwarded request failed", err)
		}
		res, err := translate.SetupResponse(pbRes)
		log.Info("deleteme response after translation", "res", res, "err", err)
		if suc, ok := res.(*segment.SegmentSetupResponseSuccess); ok {
			token = &suc.Token
		} else {
			log.Debug("failure from downstream, returning it as well")
			return res, nil
		}
	}
	// update token
	currStep := req.Path.Steps[req.Path.CurrentStep]
	log.Info("deleteme $$$$$$$$$ TOKEN updated", "curr_step", req.Path.CurrentStep)
	// TODO(juagargi) compute MAC for token
	token.HopFields = append(token.HopFields, reservation.HopField{
		Ingress: currStep.Ingress,
		Egress:  currStep.Egress,
	})
	mac, err := s.computeMAC(rsv.ID.Suffix[:], token, req.Path.SrcIA().A, req.Path.DstIA().A)
	if err != nil {
		failedResponse.Message = "cannot compute MAC: " + err.Error()
		return failedResponse, s.errWrapStr("cannot compute MAC", err)
	}
	log.Info("deleteme MAC MAC MAC", "mac", hex.EncodeToString(mac))
	copy(token.HopFields[len(token.HopFields)-1].Mac[:], mac)
	log.Info("deleteme 220 rsv index token", "index.token", index.Token)
	// store token
	index.Token = token
	log.Info("deleteme 221 rsv index token", "index.token", index.Token)
	tx, err = s.db.BeginTransaction(ctx, nil)
	if err != nil {
		failedResponse.Message = "storing token, cannot create transaction: " + err.Error()
		return failedResponse, s.errWrapStr("storing token, cannot create transaction", err)
	}
	defer tx.Rollback()
	if err := tx.PersistSegmentRsv(ctx, rsv); err != nil {
		failedResponse.Message = "storing token, cannot persist rsv: " + err.Error()
		return failedResponse, s.errWrapStr("storing token, cannot persist rsv", err)
	}
	if err := tx.Commit(); err != nil {
		failedResponse.Message = "storing token, cannot commit transaction: " + err.Error()
		return failedResponse, s.errWrapStr("storing token, cannot commit transaction", err)
	}
	return &segment.SegmentSetupResponseSuccess{
		MsgId: failedResponse.MsgId,
		Token: *token,
	}, nil
}

// func (s *Store) computeMAC(id reservation.SegmentID, expTick uint32) ([]byte, error) {
func (s *Store) computeMAC(suffix []byte, tok *reservation.Token, srcAS, dstAS addr.AS) (
	[]byte, error) {

	buff := make([]byte, colibri.LengthInputDataRound16)
	hf := tok.HopFields[len(tok.HopFields)-1]
	err := colibri.MACInput(buff, suffix, uint32(tok.InfoField.ExpirationTick), tok.BWCls, tok.RLC,
		true, false, tok.Idx, srcAS, dstAS, hf.Ingress, hf.Egress)
	if err != nil {
		return nil, err
	}
	return colibri.StaticMAC(s.colibriKey, buff)
}

func sumAllBW(rsvs []*e2e.Reservation) uint64 {
	var accum uint64
	for _, r := range rsvs {
		accum += r.AllocResv()
	}
	return accum
}

func freeInSegRsv(ctx context.Context, tx backend.Transaction, segRsv *segment.Reservation) (
	uint64, error) {

	rsvs, err := tx.GetE2ERsvsOnSegRsv(ctx, &segRsv.ID)
	if err != nil {
		return 0, serrors.WrapStr("cannot obtain e2e reservations to compute free bw",
			err, "segment_id", segRsv.ID)
	}
	free := float64(segRsv.ActiveIndex().AllocBW.ToKbps())*float64(segRsv.TrafficSplit) -
		float64(sumAllBW(rsvs))
	return uint64(free), nil
}

// max bw in egress interface of the transfer AS
func freeAfterTransfer(ctx context.Context, tx backend.Transaction, rsv *e2e.Reservation) (
	uint64, error) {

	seg1 := rsv.SegmentReservations[0]
	seg2 := rsv.SegmentReservations[1]
	if seg1.PathType == reservation.CorePath && seg2.PathType == reservation.DownPath {
		// as if no transfer
		return math.MaxUint64, nil
	}
	// get all seg rsvs with this AS as destination, AND transfer flag set
	rsvs, err := tx.GetAllSegmentRsvs(ctx)
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, r := range rsvs {
		if r.Egress == 0 && r.PathEndProps&reservation.EndTransfer != 0 {
			total += r.ActiveIndex().AllocBW.ToKbps()
		}
	}
	ratio := float64(seg1.ActiveIndex().AllocBW.ToKbps()) / float64(total)
	// effectiveE2eTraffic is the minimum BW that e2e rsvs can use
	effectiveE2eTraffic := float64(seg2.ActiveIndex().AllocBW.ToKbps()) * ratio
	e2es, err := tx.GetE2ERsvsOnSegRsv(ctx, &seg2.ID)
	if err != nil {
		return 0, err
	}
	total = sumAllBW(e2es)
	// the available BW for this e2e rsv is the effective minus the already used
	return uint64(effectiveE2eTraffic) - total, nil
}

// type deadMansSwitch struct {
// 	cancelled    bool
// 	callWhenDead func()
// }

// // IfDead prepares a dead man's switch that takes a function to execute when dead.
// // Since we don't have execution when out of scope (or function) we return a function to
// // be called with defer to simulate the dead that triggers the switch.
// // To cancel the execution of the function, unarm the switch.
// // Returns the deferrable function and the switch object.
// func IfDead(fcn func()) (func(), *deadMansSwitch) {
// 	s := &deadMansSwitch{
// 		callWhenDead: fcn,
// 	}
// 	return s.whenImDead, s
// }

// // Unarm sets the switch not to action when dead.
// func (s *deadMansSwitch) Unarm() {
// 	s.cancelled = true
// }

// func (s *deadMansSwitch) whenImDead() {
// 	if !s.cancelled {
// 		s.callWhenDead()
// 	}
// }
