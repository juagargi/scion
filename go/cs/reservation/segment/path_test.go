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

package segment_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/go/cs/reservation/segment"
	"github.com/scionproto/scion/go/cs/reservation/segmenttest"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/xtest"
)

func TestValidatePath(t *testing.T) {
	tc := map[string]struct {
		Path    segment.TransparentPath
		IsValid bool
	}{
		"src-dst": {
			Path:    segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0),
			IsValid: true,
		},
		"invalid dst": {
			Path:    segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 2),
			IsValid: false,
		},
		"invalid src": {
			Path:    segmenttest.NewPathFromComponents(2, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0),
			IsValid: false,
		},
	}
	for name, tc := range tc {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := tc.Path.Validate()
			if tc.IsValid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestEqualPath(t *testing.T) {
	tc := map[string]struct {
		Path1   segment.TransparentPath
		Path2   segment.TransparentPath
		IsEqual bool
	}{
		"eq1": {
			Path1:   segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0),
			Path2:   segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0),
			IsEqual: true,
		},
		"eq2": {
			Path1: segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 2, "1-ff00:1:10", 3,
				1, "1-ff00:0:2", 0),
			Path2: segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 2, "1-ff00:1:10", 3,
				1, "1-ff00:0:2", 0),
			IsEqual: true,
		},
		"eq3": {
			Path1:   nil,
			Path2:   nil,
			IsEqual: true,
		},
		"eq4": {
			Path1:   nil,
			Path2:   make(segment.TransparentPath, 0),
			IsEqual: true,
		},
		"neq1": {
			Path1:   segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0),
			Path2:   segmenttest.NewPathFromComponents(1, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0),
			IsEqual: false,
		},
		"neq2": {
			Path1:   segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0),
			Path2:   segmenttest.NewPathFromComponents(0, "1-ff00:0:3", 1, 1, "1-ff00:0:2", 0),
			IsEqual: false,
		},
		"neq3": {
			Path1:   segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0),
			Path2:   segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 2, 1, "1-ff00:0:2", 0),
			IsEqual: false,
		},
		"neq4": {
			Path1: segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 2, "1-ff00:1:10", 3,
				1, "1-ff00:0:2", 0),
			Path2:   segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 2, "1-ff00:1:10", 3),
			IsEqual: false,
		},
	}
	for name, tc := range tc {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			eq := tc.Path1.Equal(tc.Path2)
			require.Equal(t, tc.IsEqual, eq)
		})
	}
}

func TestGetIAs(t *testing.T) {
	p := segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0)
	require.Equal(t, xtest.MustParseIA("1-ff00:0:1"), p.GetSrcIA())
	require.Equal(t, xtest.MustParseIA("1-ff00:0:2"), p.GetDstIA())
	p = nil
	require.Equal(t, xtest.MustParseIA("0-0"), p.GetSrcIA())
	require.Equal(t, xtest.MustParseIA("0-0"), p.GetDstIA())
	p = make(segment.TransparentPath, 0)
	require.Equal(t, xtest.MustParseIA("0-0"), p.GetSrcIA())
	require.Equal(t, xtest.MustParseIA("0-0"), p.GetDstIA())
}

func TestPathLen(t *testing.T) {
	p := segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0)
	require.Equal(t, 2*12, p.Len())
	p = segment.TransparentPath{}
	require.Equal(t, 0, p.Len())
	p = nil
	require.Equal(t, 0, p.Len())
	p = make(segment.TransparentPath, 0)
	require.Equal(t, 0, p.Len())
}

func TestToFromBinary(t *testing.T) {
	p := segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0)
	var buff []byte
	_, err := p.Read(buff)
	require.Error(t, err)
	_, err = p.Read(buff)
	require.Error(t, err)
	buff = make([]byte, 2*12)
	c, err := p.Read(buff)
	require.NoError(t, err)
	require.Equal(t, 2*12, c)

	anotherP, err := segment.NewPathFromRaw(buff)
	require.NoError(t, err)
	require.Equal(t, p, anotherP)

	anotherBuff := p.ToRaw()
	require.Equal(t, buff, anotherBuff)
	// wrong buffer
	buff = buff[:len(buff)-1]
	_, err = segment.NewPathFromRaw(buff)
	require.Error(t, err)
	// empty and nil buffer
	p, err = segment.NewPathFromRaw(nil)
	require.NoError(t, err)
	require.Empty(t, p)
	p, err = segment.NewPathFromRaw([]byte{})
	require.NoError(t, err)
	require.Empty(t, p)
	// empty and nil path
	p = nil
	require.Empty(t, p.ToRaw())
	p = make(segment.TransparentPath, 0)
	require.Empty(t, p.ToRaw())
}

func TestTransparentPathString(t *testing.T) {
	cases := map[string]struct {
		transparent segment.TransparentPath
		str         string
	}{
		"empty": {
			transparent: segmenttest.NewPathFromComponents(),
			str:         "",
		},
		"one_step": {
			transparent: segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 0),
			str:         "1-ff00:0:1#0,0",
		},
		"two_steps": {
			transparent: segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0),
			str:         "1-ff00:0:1#0,1 > 1-ff00:0:2#1,0",
		},
	}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.str, tc.transparent.String())
		})
	}
}

func TestOpaquePathString(t *testing.T) {
	cases := map[string]struct {
		opaque segment.OpaquePath
		str    string
	}{
		"empty": {
			opaque: segmenttest.NewOpaquePathFromComponents(),
			str:    "",
		},
		"one_step": {
			opaque: segmenttest.NewOpaquePathFromComponents(0, 0),
			str:    "0,0",
		},
		"two_steps": {
			opaque: segmenttest.NewOpaquePathFromComponents(0, 1, 2, 0),
			str:    "0,1 > 2,0",
		},
	}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.str, tc.opaque.String())
		})
	}
}

func TestTransparentPathHasInterfaces(t *testing.T) {
	cases := map[string]struct {
		transparent segment.TransparentPath
		expected    []snet.PathInterface
	}{
		"empty": {
			transparent: segmenttest.NewPathFromComponents(),
			expected:    segmenttest.NewIfaces(),
		},
		"two_steps": {
			transparent: segmenttest.NewPathFromComponents(0, "1-ff00:0:1", 1, 2, "1-ff00:0:2", 0),
			expected:    segmenttest.NewIfaces("1-ff00:0:1", 1, 2, "1-ff00:0:2"),
		},
	}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.expected, tc.transparent.Interfaces())
		})
	}
}

func TestNewOpaquePathFromInterfaces(t *testing.T) {
	cases := map[string]struct {
		ifaces    []snet.PathInterface
		expectErr bool
		opaque    segment.OpaquePath
	}{
		"empty": {
			ifaces: segmenttest.NewIfaces(),
			opaque: segmenttest.NewOpaquePathFromComponents(),
		},
		"one": {
			ifaces:    segmenttest.NewIfaces("1-1", 1, 2, "1-1")[:1],
			expectErr: true,
			opaque:    nil,
		},
		"two": {
			ifaces: segmenttest.NewIfaces("1-1", 1, 2, "1-1"),
			opaque: segmenttest.NewOpaquePathFromComponents(0, 1, 2, 0),
		},
		"three": {
			ifaces: segmenttest.NewIfaces("1-1", 1, 2, "1-1", 3, 4, "1-1"),
			opaque: segmenttest.NewOpaquePathFromComponents(0, 1, 2, 3, 4, 0),
		},
	}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			opaque, err := segment.NewOpaquePathFromInterfaces(tc.ifaces)
			if tc.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.opaque, opaque)
		})
	}
}

func TestTransparentToOpaque(t *testing.T) {
	cases := map[string]struct {
		transparent segment.TransparentPath
		expected    segment.OpaquePath
	}{
		"nil": {
			transparent: nil,
			expected:    segment.OpaquePath{},
		},
		"empty": {
			transparent: segment.TransparentPath{},
			expected:    segment.OpaquePath{},
		},
		"one step": {
			transparent: segmenttest.NewPathFromComponents(0, "0-0", 1),
			expected:    segmenttest.NewOpaquePathFromComponents(0, 1),
		},
		"two steps": {
			transparent: segmenttest.NewPathFromComponents(0, "0-0", 1, 2, "0-0", 0),
			expected:    segmenttest.NewOpaquePathFromComponents(0, 1, 2, 0),
		},
	}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.transparent.Opaque())
		})
	}
}
