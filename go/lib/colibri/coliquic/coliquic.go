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

// Package coliquic implements QUIC on top of COLIBRI.
// Inspired on squic.
package coliquic

import (
	"github.com/lucas-clemente/quic-go"

	"github.com/scionproto/scion/go/lib/slayers/path/colibri"
	"github.com/scionproto/scion/go/lib/snet"
)

// GetColibriPath returns the (last) COLIBRI path used with this quic Session, or nil if none.
func GetColibriPath(session quic.Session) (*colibri.ColibriPath, error) {
	// TODO(juagargi) currently, the same session can receive packets from multitude of
	// COLIBRI paths (or non colibri), which should not be allowed. To enforce that the limits
	// of the reservation are respected, only one colibri path must be allowed thru the
	// life of the session. For now we assume no malicious parties.
	var colPath *colibri.ColibriPath
	netAddr := session.RemoteAddr()
	addr, _ := netAddr.(*snet.UDPAddr)
	if addr != nil && addr.Path.Type == colibri.PathType {
		colPath = new(colibri.ColibriPath)
		if err := colPath.DecodeFromBytes(addr.Path.Raw); err != nil {
			return nil, err
		}
	}
	return colPath, nil
}
