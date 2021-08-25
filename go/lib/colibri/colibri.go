// Copyright 2020 ETH Zurich
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
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/slayers"
	"github.com/scionproto/scion/go/lib/slayers/path/colibri"
	"github.com/scionproto/scion/go/lib/util"
)

const (
	// packetLifetime denotes the maximal lifetime of a packet
	packetLifetime = 2 * time.Second
	// clockSkew denotes the maximal clock skew
	clockSkew = time.Second
	// LengthInputData denotes the length of InputData in bytes
	LengthInputData = 30
	// LengthInputDataRound16 denotes the LengthInputData rounded to the next multiple of 16
	LengthInputDataRound16 = ((LengthInputData-1)/16 + 1) * 16
)

// CreateColibriTimestamp creates the COLIBRI Timestamp from tsRel, coreID, and coreCounter.
func CreateColibriTimestamp(tsRel uint32, coreID uint8, coreCounter uint32) colibri.Timestamp {
	// 0                   1                   2                   3
	// 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
	// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	// |                             TsRel                             |
	// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	// |    CoreID     |                  CoreCounter                  |
	// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	pktId := (uint32(coreID) << 24) | uint32(coreCounter)
	return CreateColibriTimestampCustom(tsRel, pktId)
}

// CreateColibriTimestampCustom creates the COLIBRI Timestamp from tsRel and pckId.
func CreateColibriTimestampCustom(tsRel uint32, pktId uint32) colibri.Timestamp {
	// 0                   1                   2                   3
	// 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
	// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	// |                             TsRel                             |
	// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	// |                             PckId                             |
	// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	ts := colibri.Timestamp{}
	binary.BigEndian.PutUint64(ts[:], (uint64(tsRel)<<32)|uint64(pktId))
	return ts
}

// ParseColibriTimestamp reads tsRel, coreID, and coreCounter from the Timestamp.
func ParseColibriTimestamp(ts colibri.Timestamp) (tsRel uint32, coreID uint8, coreCounter uint32) {
	var pktId uint32
	tsRel, pktId = ParseColibriTimestampCustom(ts)
	coreID = uint8(pktId >> 24)
	coreCounter = pktId & 0x00ffffff
	return
}

// ParseColibriTimestampCustom reads tsRel and pckId from the Timestamp.
func ParseColibriTimestampCustom(ts colibri.Timestamp) (tsRel uint32, pktId uint32) {
	bothParts := binary.BigEndian.Uint64(ts[:])
	tsRel = uint32(bothParts >> 32)
	pktId = uint32(bothParts & 0x00000000ffffffff)
	return
}

// CreateTsRel returns tsRel, which encodes the current time (the time when this function is called)
// relative to the expiration time minus 16 seconds. The input expiration tick must be specified in
// ticks of four seconds since Unix time.
// If the current time is not between the expiration time minus 16 seconds and the expiration time,
// an error is returned.
func CreateTsRel(expirationTick uint32) (uint32, error) {
	expiration := util.SecsToTime(expirationTick * 4)
	timestamp := expiration.Add(-16 * time.Second)
	now := time.Now()
	if now.After(expiration) {
		return 0, serrors.New("provided packet expiration time is in the past",
			"expiration", expiration, "now", now)
	}
	if now.Before(timestamp) {
		return 0, serrors.New("provided packet expiration time is too far in the future",
			"timestamp", timestamp, "now", now)
	}
	diff := now.Sub(timestamp)
	tsRel := max(0, uint32(diff)/4-1)
	return tsRel, nil
}

// VerifyExpirationTick returns whether the expiration time has not been reached yet.
func VerifyExpirationTick(expirationTick uint32) bool {
	expTime := 4 * int64(expirationTick)
	now := time.Now().Unix()
	return now <= expTime
}

// VerifyTimestamp checks whether a COLIBRI packet is fresh. This means that the time the packet
// was sent from the source host, which is encoded by the expiration tick and the Timestamp,
// does not date back more than the maximal packet lifetime of two seconds. The function also takes
// a possible clock drift between the packet source and the verifier of up to one second into
// account.
func VerifyTimestamp(expirationTick uint32, ts colibri.Timestamp) bool {
	// TODO(juagargi) re-enable the proper check once we have a timestamping mechanism
	// nowNano := uint64(time.Now().UnixNano())
	// timestampNano := (4*uint64(expirationTick) - 16) * 1000000000
	// tsRel, _, _ := ParseColibriTimestamp(ts)
	// timestampSenderNano := timestampNano + (1+uint64(tsRel))*4
	// nowMs := nowNano / 1000000
	// tsSenderMs := timestampSenderNano / 1000000
	// if (nowMs < tsSenderMs-uint64(clockSkewMs)) ||
	// 	(nowMs > tsSenderMs+uint64(packetLifetimeMs)+uint64(clockSkewMs)) {
	// 	return false
	timestamp := util.SecsToTime(4*expirationTick - 16).Add(-clockSkew)
	if time.Now().Before(timestamp) {
		return false
	} else {
		return true
	}
}

// VerifyMAC verifies the authenticity of the MAC in the colibri hop field. If the MAC is correct,
// nil is returned, otherwise VerifyMAC returns an error.
func VerifyMAC(privateKey []byte, ts colibri.Timestamp, inf *colibri.InfoField,
	currHop *colibri.HopField, s *slayers.SCION) error {

	var mac []byte
	var err error

	switch inf.C {
	case true:
		mac, err = CalculateColibriMacStatic(privateKey, inf, currHop, s.SrcIA.A)
	case false:
		// TODO(juagargi) we will use the defined MAC computation once we start timestamping
		// the E2E colibri packets. For now do as if C=true. Toggle comments below.
		mac, err = CalculateColibriMacStatic(privateKey, inf, currHop, s.SrcIA.A)
		// mac, err = CalculateColibriMacPacket(privateKey, inf, ts, currHop, s)
	}
	if err != nil {
		return err
	}

	if subtle.ConstantTimeCompare(mac[:4], currHop.Mac[:4]) != 1 {
		return serrors.New("colibri mac verification failed",
			"calculated", hex.EncodeToString(mac[:4]),
			"packet", hex.EncodeToString(currHop.Mac[:4]))
	}

	return nil
}

func StaticMAC(key []byte, input []byte) ([]byte, error) {
	// Initialize cryptographic MAC function
	f, err := initColibriMac(key)
	if err != nil {
		return nil, err
	}
	// Calculate CBC-MAC = first 4 bytes of the last CBC block
	mac := make([]byte, len(input))
	f.CryptBlocks(mac, input)
	return mac[len(mac)-aes.BlockSize : len(mac)-aes.BlockSize+4], nil
}

// CalculateColibriMacStatic calculates the static colibri MAC.
// The private key comes from calling scrypto.DeriveColibriKey.
func CalculateColibriMacStatic(privateKey []byte, inf *colibri.InfoField,
	currHop *colibri.HopField, srcAS addr.AS) ([]byte, error) {

	// Initialize cryptographic MAC function
	f, err := initColibriMac(privateKey)
	if err != nil {
		return nil, err
	}
	// Prepare the input for the MAC function
	input, err := prepareMacInputStatic(srcAS, inf, currHop)
	if err != nil {
		return nil, err
	}
	// Calculate CBC-MAC = first 4 bytes of the last CBC block
	mac := make([]byte, len(input))
	f.CryptBlocks(mac, input)
	return mac[len(mac)-aes.BlockSize : len(mac)-aes.BlockSize+4], nil
}

// calculateColibriMacSigma calculates the "sigma" authenticator.
func calculateColibriMacSigma(privateKey []byte, inf *colibri.InfoField,
	currHop *colibri.HopField, s *slayers.SCION) ([]byte, error) {

	// Initialize cryptographic MAC function
	f, err := initColibriMac(privateKey)
	if err != nil {
		return nil, err
	}
	// Prepare the input for the MAC function
	input, err := prepareMacInputSigma(s, inf, currHop)
	if err != nil {
		return nil, err
	}

	// Calculate CBC-MAC = last CBC block
	mac := make([]byte, len(input))
	f.CryptBlocks(mac, input)
	return mac[len(mac)-aes.BlockSize:], nil
}

// CalculateColibriMacPacket calculates the per-packet colibri MAC.
func CalculateColibriMacPacket(privateKey []byte, inf *colibri.InfoField, ts colibri.Timestamp,
	currHop *colibri.HopField, s *slayers.SCION) ([]byte, error) {

	auth, err := calculateColibriMacSigma(privateKey, inf, currHop, s)
	if err != nil {
		return nil, err
	}
	// Initialize cryptographic MAC function
	f, err := initColibriMac(auth)
	if err != nil {
		return nil, err
	}
	// Prepare the input for the MAC function
	input, err := prepareMacInputPacket(ts, inf, s)
	if err != nil {
		return nil, err
	}

	// Calculate CBC-MAC = first 4 bytes of the last CBC block
	mac := make([]byte, len(input))
	f.CryptBlocks(mac, input)
	return mac[len(mac)-aes.BlockSize : len(mac)-12], nil
}

var zeroesBuff [12]byte

// MACInput prepares the buffer using the passed parameters to be used as input for the
// MAC computation.
// buffer is expected to be at least `LengthInputData` bytes long.
// suffix is expected to be at most 12 byte long.
func MACInput(buffer []byte, suffix []byte, expTick uint32,
	bwCls reservation.BWCls, rlc reservation.RLC, controlFlag, reverseFlag bool,
	idx reservation.IndexNumber, srcAS, dstAS addr.AS, ingress, egress uint16) error {

	if len(buffer) < LengthInputData {
		return serrors.New("buffer too small", "actual", len(buffer), "expected", LengthInputData)
	}
	copy(buffer[:12], zeroesBuff[:])
	copy(buffer[:12], suffix)
	binary.BigEndian.PutUint32(buffer[12:16], expTick)
	buffer[16] = uint8(bwCls)
	buffer[17] = uint8(rlc)
	buffer[18] = 0 // TODO(juagargi) shouldn't it be HFCount?

	// Version | C | 0
	var flags uint8
	if controlFlag {
		flags = uint8(1) << 3
	}
	flags += uint8(idx) << 4
	buffer[19] = flags
	if reverseFlag {
		binary.BigEndian.PutUint64(buffer[22:30], uint64(dstAS))
	} else {
		binary.BigEndian.PutUint64(buffer[22:30], uint64(srcAS))
	}
	binary.BigEndian.PutUint16(buffer[20:22], ingress)
	binary.BigEndian.PutUint16(buffer[22:24], egress)
	return nil
}

func initColibriMac(key []byte) (cipher.BlockMode, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, serrors.New("Unable to initialize AES cipher")
	}

	// Zero initialization vector
	zeroInitVector := make([]byte, aes.BlockSize)
	// CBC-MAC = CBC-Encryption with zero initialization vector
	mode := cipher.NewCBCEncrypter(block, zeroInitVector)
	return mode, nil
}

func prepareMacInputStatic(srcAS addr.AS, inf *colibri.InfoField,
	hop *colibri.HopField) ([]byte, error) {

	// Create buffer large enough to store InputData, with length aligned to 16 bytes
	buffer := make([]byte, LengthInputDataRound16)
	err := prepareInputData(srcAS, inf, hop, buffer)
	if err != nil {
		return nil, err
	}
	return buffer, nil
}

func prepareMacInputSigma(s *slayers.SCION, inf *colibri.InfoField,
	hop *colibri.HopField) ([]byte, error) {

	// Check consistency of SL and DL with the actual address lengths
	srcLen := len(s.RawSrcAddr)
	dstLen := len(s.RawDstAddr)
	consistent := (4*(int(s.DstAddrLen)+1) == dstLen) &&
		(4*(int(s.SrcAddrLen)+1) == srcLen)
	if !consistent {
		panic(fmt.Sprintf("SL/DL not consistent with actual address lengths. DL: %d, SL: %d",
			s.DstAddrLen, s.SrcAddrLen))
	}

	// Write SL/ST/DL/DT into one single byte
	flags := uint8(s.DstAddrType&0x3)<<6 | uint8(s.DstAddrLen&0x3)<<4 |
		uint8(s.SrcAddrType&0x3)<<2 | uint8(s.SrcAddrLen&0x3)

	// The MAC input consists of the InputData plus the host addresses and the flags, rounded
	// up to the next multiple of aes.BlockSize bytes
	bufLen := LengthInputData + 1 + srcLen + dstLen
	nrBlocks := (bufLen-1)/aes.BlockSize + 1
	buffer := make([]byte, aes.BlockSize*nrBlocks)

	err := prepareInputData(s.SrcIA.A, inf, hop, buffer)
	if err != nil {
		return nil, err
	}
	buffer[LengthInputData] = flags
	copy(buffer[LengthInputData+1:], s.RawSrcAddr)
	copy(buffer[LengthInputData+1+srcLen:], s.RawDstAddr)

	return buffer, nil
}

func prepareMacInputPacket(ts colibri.Timestamp, inf *colibri.InfoField,
	s *slayers.SCION) ([]byte, error) {

	if inf == nil {
		return nil, serrors.New("invalid input")
	}

	input := make([]byte, aes.BlockSize)
	copy(input[:8], ts[:])

	baseHdrLen := uint64(slayers.CmnHdrLen + s.AddrHdrLen())
	hfcount := uint64(inf.HFCount)
	colHdrLen := 32 + (hfcount * 8)
	payloadLen := uint64(inf.OrigPayLen)
	total64 := baseHdrLen + colHdrLen + payloadLen
	if total64 > (1 << 16) {
		return nil, serrors.New("total packet length bigger than 2^16")
	}
	total16 := uint16(total64)

	binary.BigEndian.PutUint16(input[8:10], total16)

	return input, nil
}

// prepareInputData writes InputData to the given buffer.
func prepareInputData(srcAS addr.AS, inf *colibri.InfoField,
	hop *colibri.HopField, buffer []byte) error {

	if inf == nil || hop == nil {
		return serrors.New("invalid input")
	}
	if len(buffer) < LengthInputData {
		return serrors.New("provided buffer is too small")
	}
	// TODO(juagargi) Note from matzf:
	// For the segment reservations, this is only 4 bytes, right? Removing these 8 bytes of
	// padding would seem to allow to bring this down to a single block for the static MAC
	// (although these are probably not the ones that we need to optimize).
	copy(buffer[0:12], inf.ResIdSuffix)
	binary.BigEndian.PutUint32(buffer[12:16], inf.ExpTick)
	buffer[16] = inf.BwCls
	buffer[17] = inf.Rlc
	buffer[18] = 0

	// Version | C | 0
	var flags uint8
	if inf.C {
		flags = uint8(1) << 3
	}
	flags += inf.Ver << 4
	buffer[19] = flags

	binary.BigEndian.PutUint64(buffer[22:30], uint64(srcAS))
	binary.BigEndian.PutUint16(buffer[20:22], hop.IngressId)
	binary.BigEndian.PutUint16(buffer[22:24], hop.EgressId)

	return nil
}

func max(x, y uint32) uint32 {
	if x < y {
		return y
	}
	return x
}
