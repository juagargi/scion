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

// Package bwencoding holds the encoding of the bandwidth of a Hummingbird
// reservation. The dataplane carries that bandwidth as a 10 bit codepoint, so
// everybody who reads or writes one has to agree on the bandwidth it stands
// for: the router that polices the traffic, and the marketplace that sells the
// reservations.
package bwencoding

// DecodeEncodedBandwidth returns the bandwidth of a codepoint, in kbps.
//
// It is a variable so that a different encoding can be put in place, which every
// user of a codepoint then follows. Replace it during initialization, before any
// codepoint is interpreted.
var DecodeEncodedBandwidth func(codepoint uint16) uint32 = decodeEncodedBandwidthWithLogStart

// EncodeBandwidth returns the rounded bandwidth and corresponding codepoint for an
// arbitrary bandwidth
var EncodeBandwidth func(bw uint32) (uint32, uint16) = encodeBandwidthWithLogStart
