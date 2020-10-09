// Copyright 2020 ETH Zurich
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
	"testing"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/drkey"
	"github.com/scionproto/scion/go/lib/util"
	"github.com/scionproto/scion/go/lib/xtest"
	"github.com/scionproto/scion/go/pkg/cs/drkey/test"
	"github.com/stretchr/testify/require"
)

func TestDeriveLvl2Key(t *testing.T) {
	srcIA, _ := addr.IAFromString("1-ff00:0:1")
	dstIA, _ := addr.IAFromString("1-ff00:0:2")
	k := xtest.MustParseHexString("c584cad32613547c64823c756651b6f5") // just a level 1 key
	expectedKey := xtest.MustParseHexString("b90ceff1586e5b5cc3313445df18f271")

	sv, err := test.GetSecretValueTestFactory().GetSecretValue(util.SecsToTime(0))
	require.NoError(t, err)

	lvl1Key := drkey.Lvl1Key{
		Key: k,
		Lvl1Meta: drkey.Lvl1Meta{
			Epoch: sv.Epoch,
			SrcIA: srcIA,
			DstIA: dstIA,
		},
	}

	var srcHost addr.HostAddr = addr.HostNone{}
	var dstHost addr.HostAddr = addr.HostNone{}
	meta := drkey.Lvl2Meta{
		KeyType:  drkey.AS2AS,
		Protocol: "scmp",
		Epoch:    lvl1Key.Epoch,
		SrcIA:    srcIA,
		DstIA:    dstIA,
		SrcHost:  srcHost,
		DstHost:  dstHost,
	}
	lvl2Key, err := deriveLvl2(meta, lvl1Key)
	require.NoError(t, err)
	require.EqualValues(t, expectedKey, lvl2Key.Key)
}
