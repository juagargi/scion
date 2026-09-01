// Copyright 2026 ETH Zurich
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

package bwencoding

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseBandwidth(t *testing.T) {
	tests := map[string]struct {
		raw      string
		withUnit bool
		want     uint32
		wantErr  bool
	}{
		"class": {
			raw: "3", want: 3,
		},
		"maximum class": {
			raw: "4294967295", want: ^uint32(0),
		},
		"kbps": {
			raw: "100kbps", withUnit: true, want: 100,
		},
		"mbps": {
			raw: "1mbps", withUnit: true, want: 1000,
		},
		"gbps": {
			raw: "2gbps", withUnit: true, want: 2_000_000,
		},
		"whitespace and case insensitive": {
			raw: " 1 MBpS ", withUnit: true, want: 1000,
		},
		"class with unit": {
			raw: "3kbps", wantErr: true,
		},
		"unit required": {
			raw: "100", withUnit: true, wantErr: true,
		},
		"invalid unit": {
			raw: "100tbps", withUnit: true, wantErr: true,
		},
		"invalid unit number": {
			raw: "manymbps", withUnit: true, wantErr: true,
		},
		"invalid class": {
			raw: "not-a-number", wantErr: true,
		},
		"overflow": {
			raw: "5000gbps", withUnit: true, wantErr: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := ParseBandwidth(test.raw, test.withUnit)
			if test.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}
