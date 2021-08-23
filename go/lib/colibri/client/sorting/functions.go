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

// ByBW is the order function to find the trip with the widest data reservation for e2e.
// This does not take into account the existing e2e reservations, but only the potential
// bandwidth allocatable by an e2e reservation, if all of it was free.
func ByBW(a, b colibri.FullTrip) bool {
	return a.BW() > b.BW()
}

// ByNumberOfASes is the sort function that prefers less number of ASes.
func ByNumberOfASes(a, b colibri.FullTrip) bool {
	return a.NumberOfASes() < b.NumberOfASes()
}

// ByExpiration is the less function to sort reservations by expiration time.
func ByExpiration(a, b colibri.FullTrip) bool {
	return a.ExpirationTime().Before(b.ExpirationTime())
}

func ByMinBW(a, b colibri.FullTrip) bool {
	return a.MinBW() > b.MinBW()
}

func ByMaxBW(a, b colibri.FullTrip) bool {
	return a.MaxBW() > b.MaxBW()
}

func ByAllocBW(a, b colibri.FullTrip) bool {
	return a.AllocBW() > b.AllocBW()
}

func BySplit(a, b colibri.FullTrip) bool {
	return a.Split() > b.Split()
}

func Inverse(fcn func(a, b colibri.FullTrip) bool) func(a, b colibri.FullTrip) bool {
	return func(a, b colibri.FullTrip) bool {
		return !fcn(a, b)
	}
}
