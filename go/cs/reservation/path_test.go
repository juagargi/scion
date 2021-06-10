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

package reservation

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/go/lib/slayers/path/colibri"
	"github.com/scionproto/scion/go/lib/spath"
	"github.com/scionproto/scion/go/lib/xtest"
)

func TestOpaqueToRawFromRaw(t *testing.T) {
	cases := map[string]struct {
		opaque *TransparentPath
	}{
		"nil": {
			opaque: nil,
		},
		"empty": {
			opaque: &TransparentPath{
				CurrentStep: 0,
				Steps:       []PathStep{},
			},
		},
		"no spath": {
			opaque: &TransparentPath{
				CurrentStep: 1,
				Steps: []PathStep{
					{
						Ingress: 0,
						Egress:  1,
						IA:      xtest.MustParseIA("1-ff00:0:111"),
					},
					{
						Ingress: 4,
						Egress:  0,
						IA:      xtest.MustParseIA("1-ff00:0:110"),
					},
				},
			},
		},
		"some spath": {
			opaque: &TransparentPath{
				CurrentStep: 1,
				Steps: []PathStep{
					{
						Ingress: 0,
						Egress:  1,
						IA:      xtest.MustParseIA("1-ff00:0:111"),
					},
					{
						Ingress: 4,
						Egress:  0,
						IA:      xtest.MustParseIA("1-ff00:0:110"),
					},
				},
				Spath: spath.Path{
					Type: colibri.PathType,
					Raw:  xtest.MustParseHexString("beefcafe"),
				},
			},
		},
		"only spath": {
			opaque: &TransparentPath{
				CurrentStep: 111,
				Steps:       []PathStep{},
				Spath: spath.Path{
					Type: colibri.PathType,
					Raw:  xtest.MustParseHexString("beefcafe"),
				},
			},
		},
	}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw := tc.opaque.ToRaw()
			opaque, err := TransparentPathFromRaw(raw)
			require.NoError(t, err, "buffer=%s", hex.EncodeToString(raw))
			require.Equal(t, tc.opaque, opaque, "buffer=%s", hex.EncodeToString(raw))
		})
	}
}
