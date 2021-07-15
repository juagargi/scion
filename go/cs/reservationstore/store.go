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
	colpath "github.com/scionproto/scion/go/lib/slayers/path/colibri"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/spath"
	"github.com/scionproto/scion/go/lib/topology"
	"github.com/scionproto/scion/go/lib/util"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
)

// Store is the reservation store.
type Store struct {
	// TODO(juagargi) bind the logger to use the localIA in messages
	localIA    addr.IA
	isCore     bool
	db         backend.DB                      // aka reservation map
	admitter   admission.Admitter              // the chosen admission entity
	operator   *coliquic.ServiceClientOperator // dials next colibri service
	colibriKey []byte                          // colibri secret key
}

var _ reservationstorage.Store = (*Store)(nil)

// NewStore creates a new reservation store.
func NewStore(topo topology.Topology, router snet.Router, dialer coliquic.GRPCClientDialer,
	db backend.DB, admitter admission.Admitter, masterKey []byte) (*Store, error) {

	// check that the admitter is well configured
	cap := admitter.Capacities()
	for _, ifid := range append(topo.InterfaceIDs(), 0) {
		log.Info("colibri admission capacity", "ifid", ifid,
			"ingress", cap.CapacityIngress(uint16(ifid)),
			"egress", cap.CapacityEgress(uint16(ifid)))
	}
	operator, err := coliquic.NewServiceClientOperator(topo, router, dialer)
	if err != nil {
		return nil, err
	}
	colibriKey, err := scrypto.DeriveColibriMacKey(masterKey)
	if err != nil {
		return nil, err
	}
	return &Store{
		localIA:    topo.IA(),
		isCore:     topo.Core(),
		db:         db,
		admitter:   admitter,
		operator:   operator,
		colibriKey: colibriKey,
	}, nil
}

func (s *Store) deletemePrintAllRsvs(ctx context.Context) {

	allRsvs, err := s.db.GetAllSegmentRsvs(ctx)
	if err != nil {
		panic(err)
	}
	for _, r := range allRsvs {
		log.Info("deleteme FOUND reservation", "id", r.ID.String(),
			"spath_type", r.PathAtSource.Spath.Type,
			"direction", r.PathType,
			"src", r.PathAtSource.SrcIA(), "dst", r.PathAtSource.DstIA())
	}
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

func (s *Store) GetReservationsAtSource(ctx context.Context, dstIA addr.IA) (
	[]*segment.Reservation, error) {

	return s.db.GetSegmentRsvsFromSrcDstIA(ctx, s.localIA, dstIA, reservation.UnknownPath)
}

func (s *Store) ListReservations(ctx context.Context, dstIA addr.IA,
	pathType reservation.PathType) ([]*colibri.ReservationLooks, error) {
	log.Info("---------------------- vvvvv ------------")
	s.deletemePrintAllRsvs(ctx)
	log.Info("---------------------- ^^^^^ ------------")
	rsvs, err := s.db.GetSegmentRsvsFromSrcDstIA(ctx, s.localIA, dstIA, pathType)
	if err != nil {
		log.Error("listing reservations", "err", err)
		return nil, s.err(err)
	}
	return reservationsToLooks(rsvs), nil
}

// ListStitchableSegments will first get the rsv. segments starting from this store.
// It may dial two times more to two external AS colibri services, to get core and down
// segments.
func (s *Store) ListStitchableSegments(ctx context.Context, dst addr.IA) (
	*colibri.StitchableSegments, error) {

	log.Info("deleteme list stitchables called", "dst", dst.String())
	// The function obtains first all the up segments to core (if the local AS is non-core).
	// If core, it adds itself to the local ISD reachable core ASes.
	// The function then finds all core segments from the reachable local ISD core ASes to
	// the core ISD of the destination.
	// The function then finds all the down segments from the reachable remote core ISD to the
	// destination.
	// Additionally, if the local ISD is the same as the remote ISD, the function tries to find
	// up segments to the destination.
	response := &colibri.StitchableSegments{
		Up:   make([]*colibri.ReservationLooks, 0),
		Core: make([]*colibri.ReservationLooks, 0),
		Down: make([]*colibri.ReservationLooks, 0),
	}
	var err error

	localIsdCores := make(map[addr.IA]struct{}) // set of reachable local ISD core ASes
	localCore := addr.IA{I: s.localIA.I, A: 0}
	log.Info("deleteme list", "core", s.isCore)
	if !s.isCore {
		response.Up, err = s.obtainRsvs(ctx, s.localIA, localCore, reservation.UpPath)
		log.Info("deleteme list", "err", err)
		if err != nil {
			return nil, serrors.WrapStr("listing stitchable segments, up", err,
				"src", "local", "dst", localCore.String())
		}
		for _, r := range response.Up {
			localIsdCores[r.DstIA] = struct{}{}
		}
	} else {
		localIsdCores[s.localIA] = struct{}{}
	}
	log.Info("deleteme list", "local_cores", localIsdCores)

	// from core of local ISD to core of destination ISD:
	// TODO(juagargi) run all this in parallel with go routines.
	remoteIsdCore := addr.IA{I: dst.I, A: 0}
	for core := range localIsdCores {
		cores, err := s.obtainRsvs(ctx, core, remoteIsdCore, reservation.CorePath)
		if err != nil {
			return nil, serrors.WrapStr("listing stitchable segments, core", err,
				"src", core.String(), "dst", remoteIsdCore.String())
		}
		response.Core = append(response.Core, cores...)
	}
	farIsdCores := make(map[addr.IA]struct{}) // set of reachable remote ISD core ASes
	for _, r := range response.Core {
		farIsdCores[r.DstIA] = struct{}{}
	}
	if s.localIA.I == dst.I {
		// if the ISD is the same, farIsdCores is a superset of localIsdCores
		for localCore := range localIsdCores {
			farIsdCores[localCore] = struct{}{}
		}
	}
	// from core of destination ISD to final destination:
	for remoteCore := range farIsdCores {
		down, err := s.obtainRsvs(ctx, remoteCore, dst, reservation.DownPath)
		if err != nil {
			return nil, serrors.WrapStr("listing stitchable segments, down", err,
				"src", remoteCore.String(), "dst", dst.String())
		}
		response.Down = append(response.Down, down...)
	}

	// additionally, if the ISD is the same, and we didn't find an up segment when trying to
	// reach the local ISD core, it means that the destination is non core, and that maybe we can
	// reach it directly with an up segment: look for an up segment to the destination
	if _, ok := localIsdCores[dst]; !ok && s.localIA.I == dst.I {
		up, err := s.obtainRsvs(ctx, s.localIA, dst, reservation.UpPath)
		if err != nil {
			return nil, serrors.WrapStr("listing stitchable segments, up direct", err,
				"src", "local", "dst", localCore.String())
		}
		// note: we couldn't possibly find these up segments before: the dst is non-core.
		response.Up = append(response.Up, up...)
	}

	// TODO(juagargi) we could use a local DB to cache the results, like the path query does.
	return response, nil
}

// InitSegmentReservation will start a new segment reservation request. The source of
// the request will have this very AS as source.
func (s *Store) InitSegmentReservation(ctx context.Context, req *segment.SetupReq) error {
	if req.IsLastAS() {
		return s.errNew("cannot initiate a reservation with this AS only in the path")
	}
	newSetup := false
	log.Info("deleteme path current step", "curr.step", req.Path.CurrentStep)
	log.Info("deleteme spath", "type", req.Path.Spath.Type, "raw_len", len(req.Path.Spath.Raw))
	if req.Path.Spath.Type == colpath.PathType {
		log.Info("deleteme deleteme deleteme !!!! COLIBRI path type in renewal")
		colp := colpath.ColibriPath{}
		err := colp.DecodeFromBytes(req.Path.Spath.Raw)
		log.Info("deleteme decoding colibri path", "err", err, "tick*4", colp.InfoField.ExpTick*4,
			"exptime", util.SecsToTime(colp.InfoField.ExpTick*4), "infofield", colp.InfoField)
		for i, hf := range colp.HopFields {
			s := fmt.Sprintf("%d>%d [%x]", hf.IngressId, hf.EgressId, hf.Mac)
			log.Info("deleteme HopField", "i", i, "hf", s)
		}
	}
	log.Info("deleteme PATHATSOURCE", "path_at_source", req.PathAtSource)
	log.Info("deleteme path", "type", req.PathType,
		"src", req.PathAtSource.SrcIA(), "dst", req.PathAtSource.DstIA())
	if req.ID.IsEmpty() {
		return serrors.New("bad empty ID")
	}
	if req.ID.ASID != s.localIA.A {
		return s.errNew("bad reservation id", "as", req.ID.ASID)
	}
	if req.ID.IsEmptySuffix() {
		newSetup = true
	}

	rsv, err := s.db.GetSegmentRsvFromID(ctx, &req.ID)
	if err != nil {
		return s.errWrapStr("cannot obtain segment reservation", err, "id", req.ID.String())
	}
	if rsv != nil && newSetup {
		return s.errNew("found existing reservation in db for a new setup", "id", req.ID.String())
	} else if rsv == nil && !newSetup {
		return s.errNew("reservation not found for a renewal", "id", req.ID.String())
	}
	log.Info("COLIBRI requesting setup/renewal", "new_setup", newSetup,
		"id", req.ID.String(), "idx", req.Index)

	origPath := req.Request.Path.Copy()
	rollbackChanges := func(setupRes segment.SegmentSetupResponse) {
		if failure, ok := setupRes.(*segment.SegmentSetupResponseFailure); ok {
			log.Info("deleteme setting the path for the cleanup/teardown",
				"reverse_traveling", req.ReverseTraveling,
				"trail", failure.FailedRequest.AllocTrail, "path_steps before", origPath.Steps)
			if !req.ReverseTraveling {
				// shorten the path to exclude those nodes the request never transited
				origPath.Steps = origPath.Steps[:len(failure.FailedRequest.AllocTrail)]
			}
			log.Info("deleteme after", "steps", origPath.Steps)
		}
		// uses the `req` that will have the new ID and index, but the original path
		req := &base.Request{
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
		log.Debug("deleteme cleaning reservations down the path", "new_setup", newSetup,
			"res", res, "err", err)
		if err != nil {
			log.Info("while cleaning reservations down the path an error occurred",
				"new_setup", newSetup, "err", err, "res", res)
		} else if _, ok := res.(*base.ResponseSuccess); !ok {
			log.Info("while cleaning reservations down the path, received failure response",
				"new_setup", newSetup, "res", res)
		}
		log.Debug("reservation has been rollback", "new_setup", newSetup)
	}
	// create new reservation in DB
	if rsv == nil { // new setup
		rsv = segment.NewReservation(req.ID.ASID)
		rsv.ID = req.ID
		rsv.Ingress = req.Ingress()
		rsv.Egress = req.Egress()
		rsv.PathType = req.PathType
		rsv.PathEndProps = req.PathProps
		rsv.TrafficSplit = req.SplitCls
		rsv.PathAtSource = req.Path

		if err := s.db.NewSegmentRsv(ctx, rsv); err != nil {
			return s.errWrapStr("initial reservation creation", err, "dst", req.Path.DstIA())
		}
		log.Info("deleteme 5.9 source path is set", "path_at_source", rsv.PathAtSource)
		req.ID = rsv.ID // the DB created a new suffix for the rsv.; copy it to the request
	}

	var res segment.SegmentSetupResponse
	if req.PathType == reservation.DownPath {
		// reverse_traveling must be true if this is a down rsv. and this AS is non core.
		// It must be false otherwise.
		// The flag indicates the admission to send the request to
		// the last AS of the path to re-start the request process from there, as the
		// admission must be computed in the direction of the reservation.
		req.ReverseTraveling = !s.isCore
		res, err = s.sendUpstreamForAdmission(ctx, req)
	} else {
		res, err = s.admitSegmentReservation(ctx, req)
	}
	if err != nil {
		log.Info("deleteme admit segment returned error", "err", err)
		rollbackChanges(res)
		return err
	}
	if _, ok := res.(*segment.SegmentSetupResponseSuccess); !ok {
		log.Info("deleteme admit segment returned failure",
			"msg", res.(*segment.SegmentSetupResponseFailure).Message)
		rollbackChanges(res)
		return serrors.New("failure in setup", "response", res)
	}
	rsv = req.Reservation
	suc := res.(*segment.SegmentSetupResponseSuccess)
	log.Info("deleteme $$$$$$$$$ TOKEN $$$$$$$$$ TOKEN $$$$$$$$$", "token", suc.Token)
	log.Info("deleteme $$$$$$$$$", "srcia", rsv.PathAtSource.SrcIA(),
		"dstia", rsv.PathAtSource.DstIA())
	log.Info("deleteme $$$$$$$$$", "active", rsv.ActiveIndex())
	log.Info("deleteme $$$$$$$$$", "req.path", req.Path)

	return nil
}

// AdmitSegmentReservation receives a setup/renewal request to admit a segment reservation.
// It is expected that this AS is not the reservation initiator.
func (s *Store) AdmitSegmentReservation(ctx context.Context, req *segment.SetupReq) (
	segment.SegmentSetupResponse, error) {

	if err := s.validateAuthenticators(&req.Request); err != nil {
		return nil, s.errWrapStr("error validating request", err, "id", req.ID.String())
	}

	log.Info("deleteme", "reverse_traveling", req.ReverseTraveling)
	if req.ReverseTraveling {
		return s.sendUpstreamForAdmission(ctx, req)
	}
	return s.admitSegmentReservation(ctx, req)
}

// ConfirmSegmentReservation changes the state of an index from temporary to confirmed.
func (s *Store) ConfirmSegmentReservation(ctx context.Context, req *base.Request) (
	base.Response, error) {

	log.Info("deleteme confirming index", "id", req.ID, "idx", req.Index)
	if err := s.validateAuthenticators(req); err != nil {
		return nil, s.errWrapStr("error validating request", err, "id", req.ID.String())
	}

	failedResponse := s.prepareFailureResp("failed to confirm index")

	if err := req.Validate(); err != nil {
		failedResponse.Message = "request validation failed: " + s.err(err).Error()
		return failedResponse, nil
	}

	tx, err := s.db.BeginTransaction(ctx, nil)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot create transaction", err, "id", req.ID.String())
	}
	defer tx.Rollback()

	rsv, err := tx.GetSegmentRsvFromID(ctx, &req.ID)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot obtain segment reservation", err,
			"id", req.ID.String())
	}
	if rsv == nil {
		failedResponse.Message = "no reservation found"
		return failedResponse, nil
	}
	if err := rsv.SetIndexConfirmed(req.Index); err != nil {
		return failedResponse, s.errWrapStr("cannot set index to confirmed", err,
			"id", req.ID.String())
	}
	if err = tx.PersistSegmentRsv(ctx, rsv); err != nil {
		return failedResponse, s.errWrapStr("cannot persist segment reservation", err,
			"id", req.ID.String())
	}
	if err := tx.Commit(); err != nil {
		return failedResponse, s.errWrapStr("cannot commit transaction", err,
			"id", req.ID.String())
	}

	log.Info("deleteme rsv confirmed here", "id", req.ID, "is_last_as", req.IsLastAS())
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

// ActivateSegmentReservation activates a segment reservation index.
func (s *Store) ActivateSegmentReservation(ctx context.Context, req *base.Request) (
	base.Response, error) {

	log.Info("deleteme activate index", "id", req.ID.String(), "idx", req.Index)
	// TODO(juagargi) refactor these functions that share a lot of code
	if err := s.validateAuthenticators(req); err != nil {
		return nil, s.errWrapStr("error validating request", err, "id", req.ID.String())
	}

	log.Info("deleteme activating 2", "req.path", req.Path)
	failedResponse := s.prepareFailureResp("failed to confirm index")
	log.Info("deleteme activating 3")
	if err := req.Validate(); err != nil {
		failedResponse.Message = "request validation failed: " + s.err(err).Error()
		return failedResponse, nil
	}
	log.Info("deleteme activating 4")
	tx, err := s.db.BeginTransaction(ctx, nil)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot create transaction", err, "id", req.ID.String())
	}
	defer tx.Rollback()
	log.Info("deleteme activating 5")
	rsv, err := tx.GetSegmentRsvFromID(ctx, &req.ID)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot obtain segment reservation", err,
			"id", req.ID.String())
	}
	log.Info("deleteme activating 6")
	if rsv == nil {
		failedResponse.Message = "no reservation found"
		return failedResponse, nil
	}
	log.Info("deleteme activating 7")
	if err := rsv.SetIndexActive(req.Index); err != nil {
		return failedResponse, s.errWrapStr("cannot set index to confirmed", err,
			"id", req.ID.String())
	}
	log.Info("deleteme activating 8", "path_type", rsv.PathType,
		"is_last", req.IsLastAS(), "is_first", req.IsFirstAS())

	// if req.IsFirstAS() {
	if isFirstASInReservation(rsv, req) {
		log.Info("deleteme activating 9", "rsv.path", rsv.PathAtSource)
		colibriPath := rsv.DeriveColibriPathAtSource()
		rawColibriPath := make([]byte, colibriPath.Len())
		if err := colibriPath.SerializeTo(rawColibriPath); err != nil {
			log.Debug("error obtaining colibri path from reservation", "err", err)
			return nil, s.errWrapStr("error obtaining colibri path from reservation", err)
		}
		log.Info("deleteme activating 10")
		rsv.PathAtSource.Spath = spath.Path{
			Type: colpath.PathType,
			Raw:  rawColibriPath,
		}
		log.Info("deleteme stored colibri path inside reservation",
			"path", hex.EncodeToString(rawColibriPath))
	}
	log.Info("deleteme activating 11")
	if err = tx.PersistSegmentRsv(ctx, rsv); err != nil {
		return failedResponse, s.errWrapStr("cannot persist segment reservation", err,
			"id", req.ID.String())
	}
	log.Info("deleteme activating 12")
	if err := tx.Commit(); err != nil {
		return failedResponse, s.errWrapStr("cannot commit transaction", err,
			"id", req.ID.String())
	}

	//
	//
	//
	//
	//
	//
	//
	//
	log.Info("deleteme activating 13")
	// if req.IsFirstAS() {
	if isFirstASInReservation(rsv, req) {
		log.Info("deleteme activating 14")
		s.deletemePrintAllRsvs(ctx)
	}
	//
	//
	//
	//
	//
	//

	if req.IsLastAS() {
		log.Info("deleteme activating 15")
		return &base.ResponseSuccess{}, nil
	}
	log.Info("deleteme activating 16")
	// forward to next colibri service
	client, err := s.operator.ColibriClient(ctx, req.Path)
	if err != nil {
		return failedResponse, s.errWrapStr("while finding a colibri service client", err)
	}

	log.Info("deleteme activating 17")
	pbRes, err := client.ActivateSegmentIndex(ctx, translate.PBufRequest(req))
	if err != nil {
		return failedResponse, s.errWrapStr("forwarded request failed", err)
	}
	log.Info("deleteme ActivateIndex successfully finished", "active", rsv.ActiveIndex())
	return translate.Response(pbRes), nil
}

// CleanupSegmentReservation deletes an index from a segment reservation.
func (s *Store) CleanupSegmentReservation(ctx context.Context, req *base.Request) (
	base.Response, error) {

	log.Info("deleteme cleanup request", "path", req.Path.String(), "id", req.ID.String(),
		"idx", req.Index)

	if err := s.validateAuthenticators(req); err != nil {
		return nil, s.errWrapStr("error validating request", err, "id", req.ID.String())
	}

	failedResponse := s.prepareFailureResp("failed to cleanup index")

	if err := req.Validate(); err != nil {
		failedResponse.Message = "request validation failed: " + s.err(err).Error()
		return failedResponse, nil
	}

	tx, err := s.db.BeginTransaction(ctx, nil)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot create transaction", err, "id", req.ID.String())
	}
	defer tx.Rollback()

	rsv, err := tx.GetSegmentRsvFromID(ctx, &req.ID)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot obtain segment reservation", err,
			"id", req.ID.String())
	}
	if rsv == nil {
		failedResponse.Message = "no reservation found"
		return failedResponse, nil
	}

	if err := rsv.RemoveIndex(req.Index); err != nil {
		// log error but continue
		log.Info("error cleaning segment index, continuing anyway", "err", err)
	}
	if err = tx.PersistSegmentRsv(ctx, rsv); err != nil {
		return failedResponse, s.errWrapStr("cannot persist segment reservation", err,
			"id", req.ID.String())
	}
	if err := tx.Commit(); err != nil {
		return failedResponse, s.errWrapStr("cannot commit transaction", err,
			"id", req.ID.String())
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
func (s *Store) TearDownSegmentReservation(ctx context.Context, req *base.Request) (
	base.Response, error) {

	log.Info("deleteme deleteme 1")

	if err := s.validateAuthenticators(req); err != nil {
		return nil, s.errWrapStr("error validating request", err, "id", req.ID.String())
	}
	log.Info("deleteme deleteme 2")

	failedResponse := s.prepareFailureResp("failed to teardown segment")

	if err := req.Validate(); err != nil {
		failedResponse.Message = "request validation failed: " + s.err(err).Error()
		return failedResponse, nil
	}
	log.Info("deleteme deleteme 3")

	tx, err := s.db.BeginTransaction(ctx, nil)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot create transaction", err, "id", req.ID.String())
	}
	defer tx.Rollback()

	log.Info("deleteme deleteme 4")

	if err := tx.DeleteSegmentRsv(ctx, &req.ID); err != nil {
		return failedResponse, s.errWrapStr("cannot teardown reservation", err,
			"id", req.ID.String())
	}
	log.Info("deleteme deleteme 5")

	if err := tx.Commit(); err != nil {
		return failedResponse, s.errWrapStr("cannot commit transaction", err,
			"id", req.ID.String())
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
func (s *Store) AdmitE2EReservation(ctx context.Context, req *e2e.SetupReq) (
	base.Response, error) {

	failedResponse := s.prepareFailureResp("cannot admit e2e reservation")

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
	if req.Success() {
		// index.Token = &req.(*e2e.SetupReqSuccess).Token
	}

	// Commented out because it contains ineffectual assignments:
	/*
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
	*/

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
	// 			asARequest.AllocationTrail = append(asARequest.AllocationTrail,
	//				maxWillingToAlloc)
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
func (s *Store) CleanupE2EReservation(ctx context.Context, req *base.Request) (
	base.Response, error) {

	if err := s.validateAuthenticators(req); err != nil {
		return nil, s.errWrapStr("error validating request", err, "id", req.ID.String())
	}

	failedResponse := s.prepareFailureResp("failed to confirm index")

	if err := req.Validate(); err != nil {
		failedResponse.Message = "request validation failed: " + s.err(err).Error()
		return failedResponse, nil
	}

	tx, err := s.db.BeginTransaction(ctx, nil)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot create transaction", err, "id", req.ID.String())
	}
	defer tx.Rollback()

	rsv, err := tx.GetE2ERsvFromID(ctx, &req.ID)
	if err != nil {
		return failedResponse, s.errWrapStr("cannot obtain e2e reservation", err,
			"id", req.ID.String())
	}
	if err := rsv.RemoveIndex(req.Index); err != nil {
		return failedResponse, s.errWrapStr("cannot delete e2e reservation index", err,
			"id", req.ID.String(), "index", req.Index)
	}
	if len(rsv.Indices) == 0 {
		if err := tx.DeleteE2ERsv(ctx, &rsv.ID); err != nil {
			return failedResponse, s.errWrapStr("cannot delete e2e reservation", err, "id", rsv.ID)
		}
	} else if err := tx.PersistE2ERsv(ctx, rsv); err != nil {
		return failedResponse, s.errWrapStr("cannot persist e2e reservation", err, "id", req.ID.String())
	}
	if err := tx.Commit(); err != nil {
		return failedResponse, s.errWrapStr("cannot commit transaction", err,
			"id", req.ID.String())
	}

	if req.IsLastAS() {
		return &base.ResponseSuccess{}, nil
	}
	// forward to next colibri service
	client, err := s.operator.ColibriClient(ctx, req.Path)
	if err != nil {
		return failedResponse, s.errWrapStr("while finding a colibri service client", err)
	}
	pbRes, err := client.CleanupE2EIndex(ctx, translate.PBufRequest(req))
	if err != nil {
		return failedResponse, s.errWrapStr("forwarded request failed", err)
	}
	return translate.Response(pbRes), nil
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
func (s *Store) validateAuthenticators(req *base.Request) error {
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
		failedResponse.Message = "request failed validation: " + s.err(err).Error()
		return failedResponse, nil
	}
	log.Info("deleteme 2 admit segment reservation", "id", req.ID.String(),
		"type", req.PathType, "curr_step", req.Path.CurrentStep)

	// if req.ID.IsEmptySuffix() && !req.IsFirstAS() {
	if req.ID.IsEmptySuffix() {
		failedResponse.Message = s.errNew("empty suffix not allowed").Error()
		return failedResponse, nil
	}

	log.Info("deleteme 3 admit segment reservation")

	tx, err := s.db.BeginTransaction(ctx, nil)
	if err != nil {
		failedResponse.Message = "cannot create transaction: " + s.err(err).Error()
		return failedResponse, s.errWrapStr("cannot create transaction", err,
			"id", req.ID.String())
	}
	defer tx.Rollback()

	log.Info("deleteme 4 admit segment reservation")

	rsv, err := tx.GetSegmentRsvFromID(ctx, &req.ID)
	if err != nil {
		failedResponse.Message = "looking for reservation: " + s.err(err).Error()
		return failedResponse, s.errWrapStr("looking for reservation", err, "id", req.ID.String())
	}

	log.Info("deleteme 5 admit segment reservation", "req_id", req.ID.String(), "rsv", rsv)

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
		// if req.IsFirstAS() {
		// 	rsv.PathAtSource = req.PathAtSource
		// } else {
		rsv.PathAtSource = req.Path // opaque for all AS but the source AS
		// }
		// we are going to extend a bit the information in the path of this reservation: if this
		// AS is at the beginning of the path or at the end, we can annotate the opaque path with
		// our IA id:
		log.Info("deleteme annotation", "curr_step", rsv.PathAtSource.CurrentStep, "ia", s.localIA)
		rsv.PathAtSource.Steps[rsv.PathAtSource.CurrentStep].IA = s.localIA
	}
	log.Info("deleteme 5.9 source path is set", "path_at_source", rsv.PathAtSource)

	req.Reservation = rsv
	log.Info("deleteme 6 admit segment reservation")

	if err := req.ValidateForReservation(rsv); err != nil {
		failedResponse.Message = "error validating request with reservation: " + s.err(err).Error()
		return failedResponse, nil
	}

	var token *reservation.Token
	log.Debug("deleteme 10", "trail", req.AllocTrail)
	// compute admission max BW
	err = s.admitter.AdmitRsv(ctx, tx, req)
	if err != nil {
		log.Debug("segment not admitted here", "id", req.ID.String(), "err", err)
		failedResponse.Message = "segment not admitted: " + s.err(err).Error()
		return failedResponse, nil
	}
	// admitted; the request contains already the value inside the "allocation beads" of the rsv
	allocBW := req.AllocTrail[len(req.AllocTrail)-1].AllocBW
	log.Info("COLIBRI admission successful", "id", req.ID.String(), "idx", req.Index,
		"alloc", allocBW, "trail", req.AllocTrail)
	log.Info("deleteme 12", "req.Reservation.Pathtype", req.Reservation.PathType,
		"req.pathtype", req.PathType)

	idx, err := rsv.NewIndex(req.ExpirationTime, req.MinBW, req.MaxBW, allocBW,
		req.RLC, req.Reservation.PathType)
	if err != nil {
		failedResponse.Message = "cannot create new index: " + s.err(err).Error()
		return failedResponse, nil
	}
	index := rsv.Index(idx)
	log.Info("deleteme 13")
	log.Info("deleteme token inside index", "token", index.Token)
	log.Info("deleteme", "id", rsv.ID)

	if err = tx.PersistSegmentRsv(ctx, rsv); err != nil {
		failedResponse.Message = "cannot persist segment reservation: " + s.err(err).Error()
		return failedResponse, s.errWrapStr("persisting segment reservation", err)
	}
	if err := tx.Commit(); err != nil {
		log.Info("deleteme 15")
		failedResponse.Message = "cannot commit transaction: " + s.err(err).Error()
		return failedResponse, s.errWrapStr("cannot commit transaction", err)
	}

	log.Debug("deleteme 16", "id", rsv.ID, "is_last_as", req.IsLastAS())

	if req.IsLastAS() {
		token = index.Token
	} else {
		// forward the request to the next COLIBRI service
		token, err = s.getTokenFromDownstreamAdmission(ctx, req)
		if err != nil {
			failedResponse.Message = s.err(err).Error()
			return failedResponse, nil
		}
	}

	// update token
	currStep := req.Path.Steps[req.Path.CurrentStep]
	log.Info("deleteme $$$$$$$$$ received TOKEN", "curr_step", req.Path.CurrentStep,
		"token", token.String())
	// TODO(juagargi) compute MAC for token
	token.HopFields = append([]reservation.HopField{{
		Ingress: currStep.Ingress,
		Egress:  currStep.Egress,
	}}, token.HopFields...)

	log.Info("deleteme MAC MAC MAC", "suffix", hex.EncodeToString(rsv.ID.Suffix),
		"src_as", req.ID.ASID.String(), "dst_as", req.ID.ASID.String())
	mac, err := s.computeMAC(rsv.ID.Suffix, token, req.ID.ASID, req.ID.ASID)
	if err != nil {
		failedResponse.Message = "cannot compute MAC: " + s.err(err).Error()
		return failedResponse, s.errWrapStr("cannot compute MAC", err)
	}
	log.Info("deleteme MAC MAC MAC", "mac", hex.EncodeToString(mac))
	copy(token.HopFields[0].Mac[:], mac)
	log.Info("deleteme 220 rsv index token", "index.token", index.Token, "token", token.String())
	// store token and colibri path inside reservation
	index.Token = token
	index.AllocBW = token.BWCls // could have been admitted for less downstream
	log.Info("deleteme 221 rsv index token", "index.token", index.Token)

	tx, err = s.db.BeginTransaction(ctx, nil)
	if err != nil {
		failedResponse.Message = "storing token, cannot create transaction: " + s.err(err).Error()
		return failedResponse, s.errWrapStr("storing token, cannot create transaction", err)
	}
	defer tx.Rollback()
	// TODO(juagargi) can we do with one call to PersistSegmentRsv instead of two?
	if err := tx.PersistSegmentRsv(ctx, rsv); err != nil {
		failedResponse.Message = "storing token, cannot persist rsv: " + s.err(err).Error()
		return failedResponse, s.errWrapStr("storing token, cannot persist rsv", err)
	}
	if err := tx.Commit(); err != nil {
		failedResponse.Message = "storing token, cannot commit transaction: " + s.err(err).Error()
		return failedResponse, s.errWrapStr("storing token, cannot commit transaction", err)
	}
	log.Info("deleteme all good, returning token")
	return &segment.SegmentSetupResponseSuccess{
		MsgId: failedResponse.MsgId,
		Token: *token,
	}, nil
}

func (s *Store) getTokenFromDownstreamAdmission(ctx context.Context, req *segment.SetupReq) (
	*reservation.Token, error) {

	log.Info("deleteme dialing grpc")
	client, err := s.operator.ColibriClient(ctx, req.Path)
	if err != nil {
		log.Debug("error finding a colibri service client", "err", err)
		return nil, serrors.WrapStr("while finding a colibri service client", err)
	}

	log.Debug("deleteme 19", "id", req.ID.String())
	pbRes, err := client.SetupSegment(ctx, translate.PBufSetupReq(req))
	log.Info("deleteme store received a response to the setup request",
		"pbres", pbRes, "err", err)
	if err != nil {
		return nil, serrors.WrapStr("forwarded request failed", err)
	}
	res, err := translate.SetupResponse(pbRes)
	log.Info("deleteme response after translation", "res", res, "err", err)
	if suc, ok := res.(*segment.SegmentSetupResponseSuccess); ok {
		return &suc.Token, nil
	}
	msg := res.(*segment.SegmentSetupResponseFailure).Message
	log.Debug("failure from downstream, returning it as well", "msg", msg)
	return nil, serrors.New(msg)
}

// sendUpstreamForAdmission sends the request upstream until it reaches the last node in the
// path; the request's traveling path is then reversed and a normal admission is computed from this
// node until the end node of the reversed path (which is the source of a down segment request).
func (s *Store) sendUpstreamForAdmission(ctx context.Context, req *segment.SetupReq) (
	segment.SegmentSetupResponse, error) {

	assert(req.ReverseTraveling,
		"sendUpstreamForAdmission must only be called for reverse traveling")

	failedResponse := &segment.SegmentSetupResponseFailure{
		MsgId: base.MsgId{
			ID:        req.ID,
			Index:     req.Index,
			Timestamp: time.Now(),
		},
		FailedRequest: req,
	}

	log.Info("deleteme sendupstream 1")
	if req.IsLastAS() {
		log.Info("deleteme sendupstream 2", "path", req.Path)
		req.ReverseTraveling = false
		if err := req.Path.Reverse(); err != nil {
			log.Info("deleteme sendupstream 3")
			failedResponse.Message = "cannot reverse path at first node in reverse trip: " +
				err.Error()
			return failedResponse, err
		}
		log.Info("deleteme sendupstream 4", "reversed path", req.Path)
		return s.admitSegmentReservation(ctx, req)
	}
	log.Info("deleteme sendupstream 10")
	// forward to next colibri service upstream
	// TODO(juagargi) this is very subobtimal: the response needs 2 round trips.
	client, err := s.operator.ColibriClient(ctx, req.Path)
	if err != nil {
		return failedResponse, s.errWrapStr("while finding a colibri service client", err)
	}

	log.Info("deleteme sendupstream 11")
	pbRes, err := client.SetupSegment(ctx, translate.PBufSetupReq(req))
	log.Info("deleteme sendupstream 12", "err", err, "res", pbRes)
	if err != nil {
		return failedResponse, s.errWrapStr("forwarded request failed", err)
	}
	// at this point, the reservation has been accepted. Update the request link with it:
	req.Reservation, err = s.db.GetSegmentRsvFromID(ctx, &req.ID)
	log.Info("deleteme rsv reloaded", "err", err, "rsv", req.Reservation,
		"path", req.Reservation.PathAtSource)
	if err != nil {
		log.Error("reloading the admitted reservation", "err", err)
		return nil, serrors.WrapStr("reloading the admitted reservation", err)
	}

	return translate.SetupResponse(pbRes)

}

// func (s *Store) computeMAC(id reservation.SegmentID, expTick uint32) ([]byte, error) {
func (s *Store) computeMAC(suffix []byte, tok *reservation.Token, srcAS, dstAS addr.AS) (
	[]byte, error) {

	buff := make([]byte, colibri.LengthInputDataRound16)
	hf := tok.HopFields[0]
	err := colibri.MACInput(buff, suffix, uint32(tok.InfoField.ExpirationTick), tok.BWCls, tok.RLC,
		true, false, tok.Idx, srcAS, dstAS, hf.Ingress, hf.Egress)
	if err != nil {
		return nil, err
	}
	log.Info("deleteme MAC MAC MAC MAC MAC", "privatekey", hex.EncodeToString(s.colibriKey),
		"input", hex.EncodeToString(buff))
	return colibri.StaticMAC(s.colibriKey, buff)
}

// obtainRsvs will query the local DB if the src is local, or dial the corresponding col service.
// Note that the returned slice could be empty if no segments could reach the destination.
func (s *Store) obtainRsvs(ctx context.Context, src, dst addr.IA, pathType reservation.PathType) (
	[]*colibri.ReservationLooks, error) {

	if src == s.localIA {
		segs, err := s.db.GetSegmentRsvsFromSrcDstIA(ctx, src, dst, pathType)
		if err != nil {
			return nil, serrors.WrapStr("getting reservations from db", err)
		}
		return reservationsToLooks(segs), nil
	}
	client, err := s.operator.DialSvcCOL(ctx, &src)
	log.Info("deleteme list after operator dial", "src", src.String(), "err", err)
	if err != nil {
		return nil, serrors.WrapStr("dialing to list reservations from remote to remote", err,
			"src", src.String(), "dst", dst.String())
	}
	res, err := client.ListReservations(ctx, &colpb.ListRequest{
		DstIa:    uint64(dst.IAInt()),
		PathType: uint32(pathType),
	})
	if res.GetErrorMessage() != "" {
		err = fmt.Errorf(res.ErrorMessage)
	}
	if err != nil {
		return nil, serrors.WrapStr("listing reservations from remote to remote", err,
			"src", src.String(), "dst", dst.String())
	}
	return translate.ListResponse(res)
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

func reservationsToLooks(rsvs []*segment.Reservation) []*colibri.ReservationLooks {
	looks := make([]*colibri.ReservationLooks, len(rsvs))
	for i, r := range rsvs {
		var expTime time.Time
		if r.ActiveIndex() != nil {
			expTime = r.ActiveIndex().Expiration
		}
		looks[i] = &colibri.ReservationLooks{
			Id:             r.ID,
			DstIA:          r.PathAtSource.DstIA(),
			ExpirationTime: expTime,
		}
	}
	return looks
}

// isFirstASInReservation indicates that an AS is the first AS in the path of the reservation.
// For up and core segments this is the first AS in the request as well.
// For down segments the first AS in the reservation will be the last AS in the request path,
// as the request travels in reverse until this last AS, and from there a "regular" setup is done.
func isFirstASInReservation(rsv *segment.Reservation, req *base.Request) bool {
	switch rsv.PathType {
	case reservation.UpPath, reservation.CorePath:
		return req.IsFirstAS()
	case reservation.DownPath:
		return req.IsLastAS()
	default:
		panic(fmt.Sprintf("unknown path type %v", rsv.PathType))
	}
}

// assert performs an assertion on an invariant. An assertion is part of the documentation.
func assert(cond bool, msg string) {
	if !cond {
		panic(msg)
	}
}
