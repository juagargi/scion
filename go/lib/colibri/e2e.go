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

// Package colibri contains methods for the creation and verification of the colibri packet
// timestamp and validation fields.
package colibri

import (
	"github.com/scionproto/scion/go/lib/colibri/reservation"
)

// E2EReservationSetup has the necessary data for an endhost to setup/renew an e2e reservation.
type E2EReservationSetup struct {
	Id          reservation.ID
	Index       reservation.IndexNumber
	Segments    []reservation.ID
	RequestedBW reservation.BWCls
}

type E2ESetupError struct {
	Message         string
	FailedAS        int
	AllocationTrail []reservation.BWCls
}

func (e *E2ESetupError) Error() string {
	return e.Message
}
