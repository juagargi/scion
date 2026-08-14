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

package storage

import (
	"fmt"
	"time"
)

type RequestedSplit struct {
	ExactFrom      time.Time
	ExactTo        time.Time
	ExactBandwidth uint32
}

type AssetSegment struct {
	StartsAt  time.Time
	StopsAt   time.Time
	Bandwidth uint32
}

type SplitResult struct {
	Split      AssetSegment
	Remainders []AssetSegment
}

func validateSplit(asset AssetSegment, p RequestedSplit) error {
	if p.ExactFrom.Before(asset.StartsAt) || p.ExactTo.After(asset.StopsAt) {
		return fmt.Errorf("purchase outside asset bounds")
	}
	if !p.ExactFrom.Before(p.ExactTo) {
		return fmt.Errorf("invalid validity range")
	}
	if p.ExactFrom.Nanosecond() != 0 || p.ExactTo.Nanosecond() != 0 {
		return fmt.Errorf("timestamps must be second precision")
	}
	if p.ExactBandwidth > asset.Bandwidth {
		return fmt.Errorf("purchase amount exceeds asset amount")
	}

	return nil
}

func SplitAsset(asset AssetSegment, split RequestedSplit) (*SplitResult, error) {
	if !asset.StartsAt.Before(asset.StopsAt) {
		return nil, fmt.Errorf("invalid asset range")
	}
	if err := validateSplit(asset, split); err != nil {
		return nil, err
	}
	remainingAsset := AssetSegment{
		Bandwidth: asset.Bandwidth,
		StartsAt:  asset.StartsAt,
		StopsAt:   asset.StopsAt,
	}
	splitResult := &SplitResult{}
	if split.ExactBandwidth != asset.Bandwidth {
		splitResult.Remainders = append(splitResult.Remainders, AssetSegment{
			StartsAt:  remainingAsset.StartsAt,
			StopsAt:   remainingAsset.StopsAt,
			Bandwidth: remainingAsset.Bandwidth - split.ExactBandwidth,
		})
		remainingAsset.Bandwidth = split.ExactBandwidth
	}
	if split.ExactFrom.Equal(remainingAsset.StartsAt) {
		// no left remainder asset exists
		if split.ExactTo.Equal(remainingAsset.StopsAt) {
			// no right remainder asset exists -> split = asset
			splitResult.Split = AssetSegment{
				Bandwidth: remainingAsset.Bandwidth,
				StartsAt:  remainingAsset.StartsAt,
				StopsAt:   remainingAsset.StopsAt,
			}
		} else {
			// only a right remainder asset exists
			splitResult.Split = AssetSegment{
				Bandwidth: remainingAsset.Bandwidth,
				StartsAt:  remainingAsset.StartsAt,
				StopsAt:   split.ExactTo,
			}
			splitResult.Remainders = append(splitResult.Remainders, AssetSegment{
				Bandwidth: remainingAsset.Bandwidth,
				StartsAt:  split.ExactTo,
				StopsAt:   remainingAsset.StopsAt,
			})
		}
	} else {
		// a left remainder asset exists
		if split.ExactTo.Equal(remainingAsset.StopsAt) {
			// no right remainder asset exists -> only a left remainder exists
			splitResult.Split = AssetSegment{
				Bandwidth: remainingAsset.Bandwidth,
				StartsAt:  split.ExactFrom,
				StopsAt:   remainingAsset.StopsAt,
			}
			splitResult.Remainders = append(splitResult.Remainders, AssetSegment{
				Bandwidth: remainingAsset.Bandwidth,
				StartsAt:  remainingAsset.StartsAt,
				StopsAt:   split.ExactFrom,
			})
		} else {
			// a left remainder and a right remainder exists
			splitResult.Split = AssetSegment{
				Bandwidth: remainingAsset.Bandwidth,
				StartsAt:  split.ExactFrom,
				StopsAt:   split.ExactTo,
			}
			splitResult.Remainders = append(splitResult.Remainders,
				AssetSegment{
					Bandwidth: remainingAsset.Bandwidth,
					StartsAt:  remainingAsset.StartsAt,
					StopsAt:   split.ExactFrom,
				}, AssetSegment{
					Bandwidth: remainingAsset.Bandwidth,
					StartsAt:  split.ExactTo,
					StopsAt:   remainingAsset.StopsAt,
				})
		}
	}
	return splitResult, nil

}
