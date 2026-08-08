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

package hummingbird

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReservationDurationAndPrice(t *testing.T) {
	duration, err := ReservationDuration(61*time.Second, 90, 60)
	require.NoError(t, err)
	require.Equal(t, 120*time.Second, duration)

	price, err := ReservationPrice(2, 10, 15, 90, 60, 61*time.Second)
	require.NoError(t, err)
	require.Equal(t, uint64(3600), price)
}

func TestReservationPriceRejectsInvalidTermsAndOverflow(t *testing.T) {
	_, err := ReservationPrice(1, 1, 1, 1, 0, time.Second)
	require.Error(t, err)

	_, err = ReservationPrice(math.MaxUint32, math.MaxUint32, 0, 0, 1, time.Duration(math.MaxInt64))
	require.Error(t, err)
}

func TestRoundUpTime(t *testing.T) {
	timestamp := time.Date(2026, time.January, 1, 12, 0, 1, 0, time.UTC)
	require.Equal(t,
		time.Date(2026, time.January, 1, 12, 1, 0, 0, time.UTC),
		RoundUpTime(timestamp, time.Minute))
}
