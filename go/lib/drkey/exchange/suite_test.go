// Copyright 2018 ETH Zurich
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

package exchange

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/drkey"
	"github.com/scionproto/scion/go/lib/drkey/protocol"
	"github.com/scionproto/scion/go/lib/scrypto"
	"github.com/scionproto/scion/go/lib/xtest"
)

func TestSuiteDRKeyLvl1(t *testing.T) {
	lvl1 := genLvl1Key(t)
	sndPubKey, sndPrivKey, err := scrypto.GenKeyPair(scrypto.Curve25519xSalsa20Poly1305)
	if err != nil {
		t.Errorf("GenKeyPair failed = %v", err)
	}
	rcvPubKey, rcvPrivKey, err := scrypto.GenKeyPair(scrypto.Curve25519xSalsa20Poly1305)
	if err != nil {
		t.Errorf("GenKeyPair failed = %v", err)
	}
	nonce, err := scrypto.Nonce(24)
	if err != nil {
		t.Errorf("Nonce failed = %v", err)
	}
	cipherMsg, err := EncryptDRKeyLvl1(lvl1, nonce, rcvPubKey, sndPrivKey)
	if err != nil {
		t.Errorf("EncryptDRKeyLvl1 failed = %v", err)
	}
	gotLvl1, err := DecryptDRKeyLvl1(cipherMsg, nonce, sndPubKey, rcvPrivKey)
	if err != nil {
		t.Errorf("DecryptDRKeyLvl1 failed = %v", err)
	}

	if !(lvl1.Lvl1Meta.SrcIA.Equal(gotLvl1.Lvl1Meta.SrcIA)) {
		t.Fatalf("Lvl1 Src IA mismatch %s != %s",
			lvl1.Lvl1Meta.SrcIA.String(), gotLvl1.Lvl1Meta.SrcIA.String())
	}
	if !(lvl1.Lvl1Meta.DstIA.Equal(gotLvl1.Lvl1Meta.DstIA)) {
		t.Fatalf("Lvl1 Dst IA mismatch %s != %s",
			lvl1.Lvl1Meta.DstIA.String(), gotLvl1.Lvl1Meta.DstIA.String())
	}
	if !lvl1.Key.Equal(gotLvl1.Key) {
		t.Fatalf("Key mismatch sent: %s, received: %s",
			hex.EncodeToString(lvl1.Key), hex.EncodeToString(gotLvl1.Key))
	}

}

func genLvl1Key(t *testing.T) drkey.Lvl1Key {
	meta := drkey.SVMeta{
		Epoch: drkey.NewEpoch(0, 1),
	}
	asSecret := []byte{0, 1, 2, 3, 4, 5, 6, 7, 0, 1, 2, 3, 4, 5, 6, 7}
	svTgtKey := xtest.MustParseHexString("47bfbb7d94706dc9e79825e5a837b006")
	epoch := drkey.NewEpoch(0, 1)
	srcIA, _ := addr.IAFromString("1-ff00:0:111")
	dstIA, _ := addr.IAFromString("1-ff00:0:112")
	sv, err := drkey.DeriveSV(meta, asSecret)
	if err != nil {
		t.Errorf("Derive SV failed = %v", err)
	}
	if bytes.Compare(sv.Key, svTgtKey) != 0 {
		t.Fatalf("Unexpected sv key: %s, expected: %s",
			hex.EncodeToString(sv.Key), hex.EncodeToString(svTgtKey))
	}
	lvlTgtKey := xtest.MustParseHexString("51663adbc06e55f40a9ad899cf0775e5")
	lvl1, err := protocol.DeriveLvl1(drkey.Lvl1Meta{
		Epoch: epoch,
		SrcIA: srcIA,
		DstIA: dstIA,
	}, sv)
	if err != nil {
		t.Errorf("DeriveLvl1 failed = %v", err)
	}
	if !lvl1.Key.Equal(lvlTgtKey) {
		t.Fatalf("Unexpected lvl1 key: %s", hex.EncodeToString(lvl1.Key))
	}

	return lvl1
}
