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

	cppb "github.com/scionproto/scion/pkg/proto/control_plane"
)

type AliasesServer struct{}

func NewAliasesServer() *AliasesServer {
	return nil
}

func (s *AliasesServer) RegisterAlias(
	ctx context.Context,
	req *cppb.RegisterAliasRequest,
) (*cppb.RegisterAliasResponse, error) {
	return nil, nil
}

func (s *AliasesServer) GetAliases(
	ctx context.Context,
	req *cppb.GetAliasesRequest,
) (*cppb.GetAliasesResponse, error) {
	return nil, nil
}
