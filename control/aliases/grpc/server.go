// Copyright 2025 ETH Zurich
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

package grpc

import (
	"context"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/alias"
	libgrpc "github.com/scionproto/scion/pkg/grpc"
	"github.com/scionproto/scion/pkg/private/util"
	cppb "github.com/scionproto/scion/pkg/proto/control_plane"
	"github.com/scionproto/scion/private/aliasdb"
)

type AliasesServer struct {
	DB     aliasdb.DB
	Dialer libgrpc.Dialer // To request other ASes' control services.
}

func NewAliasesServer(
	db aliasdb.DB,
	dialer libgrpc.Dialer,
) *AliasesServer {
	return &AliasesServer{
		DB:     db,
		Dialer: dialer,
	}
}

func (s *AliasesServer) RegisterAlias(
	ctx context.Context,
	req *cppb.RegisterAliasRequest,
) (*cppb.RegisterAliasResponse, error) {
	// Register in local DB.
	replicas := make([]alias.Replica, 0, len(req.Replicas))
	for _, rep := range req.Replicas {
		replicas = append(replicas, alias.Replica{
			IA:       addr.IA(rep.IsdAs),
			Hostname: rep.Host,
			NotAfter: util.SecsToTime(rep.NotAfter),
		})
	}

	err := s.DB.AddReplicas(ctx, req.Host, replicas)
	return &cppb.RegisterAliasResponse{}, err
}

func (s *AliasesServer) GetAliases(
	ctx context.Context,
	req *cppb.GetAliasesRequest,
) (*cppb.GetAliasesResponse, error) {
	// Return local database entries.
	replicas, err := s.DB.Get(ctx, req.Host)
	reps := make([]*cppb.Replica, 0)
	for _, rep := range replicas {
		reps = append(reps, &cppb.Replica{
			IsdAs:    uint64(rep.IA),
			Host:     rep.Hostname,
			NotAfter: util.TimeToSecs(rep.NotAfter),
		})
	}
	if err != nil {
		return nil, err
	}
	return &cppb.GetAliasesResponse{
		Replicas: reps,
	}, nil
}
