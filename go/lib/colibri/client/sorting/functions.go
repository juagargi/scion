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

package sorting

import (
	"github.com/scionproto/scion/go/lib/colibri"
)

// ByExpiration is the less function to sort reservations by expiration time.
func ByExpiration(a, b colibri.FullTrip) bool {
	return a.ExpirationTime().Before(b.ExpirationTime())
}
