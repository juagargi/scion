// Copyright 2026 ETH Zurich
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

package path

import (
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/slayers"
	dphumm "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	"github.com/scionproto/scion/pkg/snet"
)

type HummReplyPather struct {
	Fields []byte // deleteme here the authentication keys, res ids, etc.
}

func (p *HummReplyPather) SetState(state []byte) {
	p.Fields = state
}

func (HummReplyPather) ReplyPath(rpath snet.RawPath) (snet.DataplanePath, error) {
	if rpath.PathType != dphumm.PathType {
		return nil, serrors.New("non hummingbird path type for a hummingbird reply pather",
			"path_type", rpath.PathType)
	}
	var dec dphumm.Decoded
	if err := dec.DecodeFromBytes(rpath.Raw); err != nil {
		return nil, serrors.Wrap("cannot decode hummingbird raw path", err)
	}

	reversed, err := dec.Reverse()
	if err != nil {
		return nil, serrors.Wrap("cannot reverse hummingbird path", err)
	}

	// deleteme: use the proper fields of the reply pather to build a new reservation,
	// instead of using a raw reply pather which doesn't call deriveDataplanePath in its
	// SetPath function.

	return snet.RawReplyPath{
		Path: reversed,
	}, nil
}

type HummReplyPath struct {
	OriginalPath snet.RawPath
	Reversed     *Reservation
}

func (p HummReplyPath) SetPath(s *slayers.SCION) error {
	return p.Reversed.SetPath(s)
}
