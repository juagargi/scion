// Copyright 2026 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package marketplace

import (
	"fmt"
	"math"
	"time"

	hbird "github.com/scionproto/scion/pkg/hummingbird"
)

// ReservationPrice calculates the price of a reservation from its unit price,
// bandwidth, minimum terms, and requested duration.
func ReservationPrice(
	unitPrice uint32,
	bandwidth uint32,
	minimumBandwidth uint32,
	minimumDuration uint32,
	timeGranularity uint32,
	duration time.Duration,
) (uint64, error) {
	billableDuration, err := ReservationDuration(duration, minimumDuration, timeGranularity)
	if err != nil {
		return 0, err
	}
	billableBandwidth := max(bandwidth, minimumBandwidth)
	return multiply(uint64(unitPrice), uint64(billableBandwidth), uint64(billableDuration/time.Second))
}

// ReservationDuration applies an asset's minimum duration and time granularity
// to a requested duration.
func ReservationDuration(
	duration time.Duration,
	minimumDuration uint32,
	timeGranularity uint32,
) (time.Duration, error) {
	if duration <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	if timeGranularity == 0 {
		return 0, fmt.Errorf("time granularity must be positive")
	}
	duration = max(duration, time.Duration(minimumDuration)*time.Second)
	return hbird.RoundUpDuration(duration, time.Duration(timeGranularity)*time.Second), nil
}

func multiply(factors ...uint64) (uint64, error) {
	product := uint64(1)
	for _, factor := range factors {
		if factor != 0 && product > math.MaxUint64/factor {
			return 0, fmt.Errorf("reservation price overflows uint64")
		}
		product *= factor
	}
	return product, nil
}
