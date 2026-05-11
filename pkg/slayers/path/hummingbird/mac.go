// Copyright 2025 ETH Zurich
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

//go:build amd64 || arm64 || ppc64 || ppc64le

package hummingbird

import (
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"

	"golang.org/x/crypto/pbkdf2"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/slayers/path"
)

// The original implementation of FullFlyoverMac used assembly helpers copied from the
// Go AES implementation to avoid per-call allocations and the hidden key schedule work
// inside aes.NewCipher. The pure-Go expanded-key path below keeps the same caller-facing
// shape and buffer reuse, while preserving the assembly implementation for side-by-side
// testing and benchmarking.

// defined in asm_* assembly files

//go:noescape
func encryptBlockAsm(nr int, xk *uint32, dst, src *byte)

//go:noescape
func expandKeyAsm(nr int, key *byte, enc *uint32)

const (
	PathType = 5
	// SecretValueDerivationSalt is the PBKDF2 salt used to derive the Hummingbird AS secret value.
	SecretValueDerivationSalt = "Derive hbird sv"

	aesRounds            = 10
	AkBufferSize         = 16
	FlyoverMacBufferSize = 16
	XkBufferSize         = (aesRounds + 1) * (128 / 32) // 44
	// Total MAC buffer size:
	MACBufferSize = path.MACBufferSize + FlyoverMacBufferSize + AkBufferSize
)

// DeriveSecretValue derives the Hummingbird AS secret value from the master secret.
func DeriveSecretValue(masterSecret []byte) []byte {
	if len(masterSecret) == 0 {
		panic("empty key")
	}
	// This uses 16B keys with 1000 hash iterations, which is the same as the
	// defaults used by pycrypto.
	return pbkdf2.Key(masterSecret, []byte(SecretValueDerivationSalt), 1000, 16, sha256.New)
}

// Derive authentication key A_k
// block is expected to be initialized beforehand with aes.NewCipher(sv),
// where sv is this AS' secret value
// Requires buffer to be of size at least AkBufferSize
func DeriveAuthKey(
	block cipher.Block,
	resId uint32,
	bw uint16,
	in uint16,
	eg uint16,
	startTime uint32,
	resDuration uint16,
	buffer []byte,
) []byte {

	// Bounds check.
	_ = buffer[AkBufferSize-1]

	// Prepare input buffer.
	binary.BigEndian.PutUint16(buffer[0:2], in)
	binary.BigEndian.PutUint16(buffer[2:4], eg)
	binary.BigEndian.PutUint32(buffer[4:8], resId<<10|uint32(bw))
	binary.BigEndian.PutUint32(buffer[8:12], startTime)
	binary.BigEndian.PutUint16(buffer[12:14], resDuration)
	binary.BigEndian.PutUint16(buffer[14:16], 0) //padding

	// Should XOR input with iv, but we use iv = 0 => identity
	block.Encrypt(buffer[0:16], buffer[0:16])
	return buffer[0:AkBufferSize]
}

// ExpandAES128Key expands the 16-byte AES-128 key into the caller-provided round-key
// workspace. xk must have room for 44 uint32 values.
func ExpandAES128Key(ak []byte, xk []uint32) {
	_ = ak[AkBufferSize-1]
	_ = xk[XkBufferSize-1]

	xk[0] = binary.BigEndian.Uint32(ak[0:4])
	xk[1] = binary.BigEndian.Uint32(ak[4:8])
	xk[2] = binary.BigEndian.Uint32(ak[8:12])
	xk[3] = binary.BigEndian.Uint32(ak[12:16])

	for i := 4; i < XkBufferSize; i++ {
		t := xk[i-1]
		if i%4 == 0 {
			t = subw(rotw(t)) ^ (uint32(powx[i/4-1]) << 24)
		}
		xk[i] = xk[i-4] ^ t
	}
}

func subw(w uint32) uint32 {
	return uint32(sbox0[w>>24])<<24 |
		uint32(sbox0[w>>16&0xff])<<16 |
		uint32(sbox0[w>>8&0xff])<<8 |
		uint32(sbox0[w&0xff])
}

func rotw(w uint32) uint32 {
	return w<<8 | w>>24
}

// EncryptAES128BlockExpanded encrypts one AES-128 block in place using the
// caller-provided expanded key schedule.
func EncryptAES128BlockExpanded(xk []uint32, dstsrc []byte) {
	_ = xk[XkBufferSize-1]
	_ = dstsrc[FlyoverMacBufferSize-1]

	s0 := binary.BigEndian.Uint32(dstsrc[0:4]) ^ xk[0]
	s1 := binary.BigEndian.Uint32(dstsrc[4:8]) ^ xk[1]
	s2 := binary.BigEndian.Uint32(dstsrc[8:12]) ^ xk[2]
	s3 := binary.BigEndian.Uint32(dstsrc[12:16]) ^ xk[3]

	k := 4
	var t0, t1, t2, t3 uint32
	for r := 0; r < aesRounds-1; r++ {
		t0 = xk[k+0] ^ te0[uint8(s0>>24)] ^ te1[uint8(s1>>16)] ^ te2[uint8(s2>>8)] ^ te3[uint8(s3)]
		t1 = xk[k+1] ^ te0[uint8(s1>>24)] ^ te1[uint8(s2>>16)] ^ te2[uint8(s3>>8)] ^ te3[uint8(s0)]
		t2 = xk[k+2] ^ te0[uint8(s2>>24)] ^ te1[uint8(s3>>16)] ^ te2[uint8(s0>>8)] ^ te3[uint8(s1)]
		t3 = xk[k+3] ^ te0[uint8(s3>>24)] ^ te1[uint8(s0>>16)] ^ te2[uint8(s1>>8)] ^ te3[uint8(s2)]
		k += 4
		s0, s1, s2, s3 = t0, t1, t2, t3
	}

	s0 = uint32(sbox0[t0>>24])<<24 | uint32(sbox0[t1>>16&0xff])<<16 |
		uint32(sbox0[t2>>8&0xff])<<8 | uint32(sbox0[t3&0xff])
	s1 = uint32(sbox0[t1>>24])<<24 | uint32(sbox0[t2>>16&0xff])<<16 |
		uint32(sbox0[t3>>8&0xff])<<8 | uint32(sbox0[t0&0xff])
	s2 = uint32(sbox0[t2>>24])<<24 | uint32(sbox0[t3>>16&0xff])<<16 |
		uint32(sbox0[t0>>8&0xff])<<8 | uint32(sbox0[t1&0xff])
	s3 = uint32(sbox0[t3>>24])<<24 | uint32(sbox0[t0>>16&0xff])<<16 |
		uint32(sbox0[t1>>8&0xff])<<8 | uint32(sbox0[t2&0xff])

	s0 ^= xk[k+0]
	s1 ^= xk[k+1]
	s2 ^= xk[k+2]
	s3 ^= xk[k+3]

	binary.BigEndian.PutUint32(dstsrc[0:4], s0)
	binary.BigEndian.PutUint32(dstsrc[4:8], s1)
	binary.BigEndian.PutUint32(dstsrc[8:12], s2)
	binary.BigEndian.PutUint32(dstsrc[12:16], s3)
}

// Computes full flyover MAC Vk based on authentication key Ak, using the pure-Go
// expanded-key path.
func FullFlyoverMac(
	ak []byte,
	dstIA addr.IA,
	pktlen uint16,
	resStartTime uint16,
	highResTime uint32,
	buffer []byte,
	xkbuffer []uint32,
) []byte {
	return FullFlyoverMacGo(ak, dstIA, pktlen, resStartTime, highResTime, buffer, xkbuffer)
}

// FullFlyoverMacGo computes the flyover MAC using the pure-Go expanded-key AES path.
func FullFlyoverMacGo(
	ak []byte,
	dstIA addr.IA,
	pktlen uint16,
	resStartTime uint16,
	highResTime uint32,
	buffer []byte,
	xkbuffer []uint32,
) []byte {
	// Bounds check.
	_ = buffer[FlyoverMacBufferSize-1]
	_ = xkbuffer[XkBufferSize-1]

	binary.BigEndian.PutUint64(buffer[0:8], uint64(dstIA))
	binary.BigEndian.PutUint16(buffer[8:10], pktlen)
	binary.BigEndian.PutUint16(buffer[10:12], resStartTime)
	binary.BigEndian.PutUint32(buffer[12:16], highResTime)

	ExpandAES128Key(ak, xkbuffer)
	EncryptAES128BlockExpanded(xkbuffer, buffer[:FlyoverMacBufferSize])

	return buffer[0:FlyoverMacBufferSize]
}

// FullFlyoverMacAsm preserves the original assembly-backed implementation for
// side-by-side testing and benchmarking.
func FullFlyoverMacAsm(
	ak []byte,
	dstIA addr.IA,
	pktlen uint16,
	resStartTime uint16,
	highResTime uint32,
	buffer []byte,
	xkbuffer []uint32,
) []byte {
	// Bounds check.
	_ = buffer[FlyoverMacBufferSize-1]
	_ = xkbuffer[XkBufferSize-1]

	binary.BigEndian.PutUint64(buffer[0:8], uint64(dstIA))
	binary.BigEndian.PutUint16(buffer[8:10], pktlen)
	binary.BigEndian.PutUint16(buffer[10:12], resStartTime)
	binary.BigEndian.PutUint32(buffer[12:16], highResTime)

	expandKeyAsm(aesRounds, &ak[0], &xkbuffer[0])
	encryptBlockAsm(aesRounds, &xkbuffer[0], &buffer[0], &buffer[0])

	return buffer[0:FlyoverMacBufferSize]
}

// FlyoverMacWithAkAesBlock computes the MAC for a Hummingbird packet, given an existing
// block obtained with e.g. block:=aes.NewCipher(ak), and a preallocated buffer of at least
// AkBufferSize bytes.
func FlyoverMacWithAkAesBlock(
	block cipher.Block,
	buffer []byte,
	dstIA addr.IA,
	pktlen uint16,
	resStartTime uint16,
	highResTime uint32,
) []byte {
	_ = buffer[AkBufferSize-1]

	binary.BigEndian.PutUint64(buffer[0:8], uint64(dstIA))
	binary.BigEndian.PutUint16(buffer[8:10], pktlen)
	binary.BigEndian.PutUint16(buffer[10:12], resStartTime)
	binary.BigEndian.PutUint32(buffer[12:16], highResTime)

	block.Encrypt(buffer[:AkBufferSize], buffer[:AkBufferSize])
	return buffer[:AkBufferSize]
}
