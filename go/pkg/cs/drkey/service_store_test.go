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

package drkey

import (
	"testing"
	"time"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/xtest"
	"github.com/stretchr/testify/require"
)

func TestDeriveLvl1Key(t *testing.T) {
	srcIA, _ := addr.IAFromString("1-ff00:0:112")
	dstIA, _ := addr.IAFromString("1-ff00:0:111")
	expectedKey := xtest.MustParseHexString("87ee10bcc9ef1501783949a267f8ec6b")

	store := ServiceStore{
		LocalIA:      srcIA,
		SecretValues: GetSecretValueTestFactory(),
	}
	lvl1Key, err := store.DeriveLvl1(dstIA, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, expectedKey, lvl1Key.Key)
}
