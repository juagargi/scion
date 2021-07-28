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

package segment

import (
	"github.com/scionproto/scion/go/lib/colibri/reservation"
)

type SegmentSetupResponse interface {
	isSegmentSetupResponse_Success_Failure()
}

type SegmentSetupResponseSuccess struct {
	Token reservation.Token
}

func (*SegmentSetupResponseSuccess) isSegmentSetupResponse_Success_Failure() {}

type SegmentSetupResponseFailure struct {
	FailedRequest *SetupReq
	Message       string
}

func (*SegmentSetupResponseFailure) isSegmentSetupResponse_Success_Failure() {}
