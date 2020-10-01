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
	"time"

	"github.com/scionproto/scion/go/lib/drkey"
	"github.com/scionproto/scion/go/lib/drkeystorage"
	"github.com/scionproto/scion/go/lib/util"
)

func getTestMasterSecret() []byte {
	return []byte{0, 1, 2, 3}
}

// SecretValueTestFactory works as a SecretValueFactory but uses a user-controlled-variable instead
// of time.Now when calling GetSecretValue.
type SecretValueTestFactory struct {
	SecretValueFactory
	Now time.Time
}

func (f *SecretValueTestFactory) GetSecretValue(t time.Time) (drkey.SV, error) {
	return f.SecretValueFactory.GetSecretValue(f.Now)
}

func GetSecretValueTestFactory() drkeystorage.SecretValueFactory {
	return &SecretValueTestFactory{
		SecretValueFactory: *NewSecretValueFactory(getTestMasterSecret(), 10*time.Second),
		Now:                util.SecsToTime(0),
	}
}
