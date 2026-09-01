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
	"fmt"
	"math"
	"strconv"
	"strings"
)

var bandwidthUnits = []struct {
	suffix string
	kbps   uint64
}{
	{suffix: "kbps", kbps: 1},
	{suffix: "mbps", kbps: 1000},
	{suffix: "gbps", kbps: 1000 * 1000},
}

// ParseBandwidth parses a Hummingbird reservation bandwidth into kbps.
//
// Marketplace reservations use a decimal value with a unit (kbps, mbps, or gbps).
// Reservations derived from AS key material use a bare 32-bit bandwidth class instead.
// Units are case-insensitive and surrounding whitespace is ignored.
// The withUnit argument selects which of these two forms is accepted.
func ParseBandwidth(raw string, withUnit bool) (uint32, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	for _, unit := range bandwidthUnits {
		number, hasUnit := strings.CutSuffix(value, unit.suffix)
		if !hasUnit {
			continue
		}
		if !withUnit {
			return 0, fmt.Errorf("bandwidth class must not carry a unit: %q", raw)
		}
		parsed, err := strconv.ParseUint(strings.TrimSpace(number), 10, 32)
		if err != nil {
			return 0, fmt.Errorf("parsing bandwidth %q: %w", raw, err)
		}
		kbps := parsed * unit.kbps
		if kbps > math.MaxUint32 {
			return 0, fmt.Errorf("bandwidth too large %q: maximum %d kbps", raw, uint64(math.MaxUint32))
		}
		return uint32(kbps), nil
	}
	if withUnit {
		return 0, fmt.Errorf("bandwidth must carry a unit: %q (units: kbps|mbps|gbps)", raw)
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parsing bandwidth class %q: %w", raw, err)
	}
	return uint32(parsed), nil
}
