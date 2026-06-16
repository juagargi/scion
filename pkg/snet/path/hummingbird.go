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

package path

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/slayers"
	dppath "github.com/scionproto/scion/pkg/slayers/path"
	"github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	dphum "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	"github.com/scionproto/scion/pkg/slayers/path/scion"
	"github.com/scionproto/scion/pkg/snet"
)

// Reservation is the snet path for a Reservation path type.
// When creating a packet with a Reservation path, the flyover fields must contain the MAC that
// was computed using the correct payload size.
// This path represents a possibly partially reserved path, with zero or more flyovers.
type Reservation struct {
	Now   func() time.Time // The current time.
	DstIA addr.IA          // Destination IA of the path.
	Dec   *dphum.Decoded   // The Hummingbird path.
	Hops  []*Hop           // Same length as `Dec`. Hops[i]==nil iff no hop at i (eg. xover hop).

	blocksPerAk []cipher.Block        // Same length as Hops.
	scionMacs   [][dppath.MacLen]byte // Original MAC fields from the SCION path.
	counter     uint32                // duplicate detection counter.
}

var _ snet.DataplanePath = (*Reservation)(nil)

// NewReservation builds a new Hummingbird Reservation based on the destination IA and the
// options passed.
func NewReservation(opts ...ReservationModFcn) (*Reservation, error) {
	r := &Reservation{
		Now: time.Now,
		Dec: &dphum.Decoded{},
	}
	// Run all options on this object.
	for _, fcn := range opts {
		if err := fcn(r); err != nil {
			return nil, err
		}
	}

	if len(r.Hops) != len(r.Dec.HopFields) {
		return nil, fmt.Errorf("wrong number of flyover hops %d, expected %d from path",
			len(r.Hops), len(r.Dec.HopFields))
	}

	if r.DstIA == 0 {
		return nil, serrors.New("unset destination IA")
	}

	return r, nil
}

// SetPath sets the path into the passed-by-pointer scion headers.
// When called, the scion layer has its fields (e.g. payload length, src IA, etc.) already set up.
func (r *Reservation) SetPath(s *slayers.SCION) error {
	// We need to have a path set in the slayers.SCION to compute its full packet length,
	// since r.Dec and the derived dataplane path have the same length in bytes,
	// use the decoded Hummingbird path initially before deriving the correct dataplane path.
	s.Path, s.PathType = r.Dec, r.Dec.Type()
	pktLen := s.PacketLen()
	fmt.Printf("deleteme packet length = %d\n", pktLen)
	r.deriveDataPlanePath(pktLen, r.Now())

	// The correct dataplane path in the SCION layer is still r.Dec (pointer to path),
	// nothing else to do.
	return nil
}

// deriveDataPlanePath sets pathmeta timestamps and increments the duplicate detection counter and
// updates MACs of all flyoverfields using the full SCION packet length.
func (r *Reservation) deriveDataPlanePath(
	pktLen uint16,
	timeStamp time.Time,
) {

	// Update timestamps
	secs := uint32(timeStamp.Unix())
	millis := uint32(timeStamp.Nanosecond()/1_000_000) << 22 // Milliseconds use the 10 MSBs.
	millis |= r.counter
	r.Dec.Base.PathMeta.BaseTS = secs
	r.Dec.Base.PathMeta.HighResTS = millis
	// Increment counter for the next packet. Make sure it always fits in 22 bits.
	r.counter++
	r.counter %= 1 << 22 // Counter is the "tail" of the timestamp, using the 22 LSBs.

	// compute Macs for Flyovers
	var byteBuffer [hummingbird.FlyoverMacBufferSize]byte
	for i, h := range r.Hops {
		// Check if hop is xover (no hop) or non flyover (just best effort)
		if h == nil || h.Flyover == nil {
			continue
		}
		f := h.Flyover
		hf := &r.Dec.HopFields[i]
		hf.ResStartTime = uint16(secs - f.StartTime)

		flyovermac := hummingbird.FlyoverMacWithAkAesBlock(
			r.blocksPerAk[i],
			byteBuffer[:],
			r.DstIA,
			pktLen,
			hf.ResStartTime,
			millis,
		)

		binary.BigEndian.PutUint32(hf.HopField.Mac[:4],
			binary.BigEndian.Uint32(flyovermac[:4])^binary.BigEndian.Uint32(r.scionMacs[i][:4]))
		binary.BigEndian.PutUint16(hf.HopField.Mac[4:],
			binary.BigEndian.Uint16(flyovermac[4:])^binary.BigEndian.Uint16(r.scionMacs[i][4:]))

	}
}

// ReservationModFcn is a options setting function for a reservation.
type ReservationModFcn func(*Reservation) error

// WithNow modifies the current point in time for this reservation. It is useful to filter
// the different flyovers that can be passed to WithScionPath.
func WithNow(now func() time.Time) ReservationModFcn {
	return func(r *Reservation) error {
		r.Now = now
		return nil
	}
}

// WithDstIA changes the destination IA of the reservation.
func WithDstIA(dstIA addr.IA) ReservationModFcn {
	return func(r *Reservation) error {
		r.DstIA = dstIA
		return nil
	}
}

// WithScionPath allows to build a Reservation based on the SCION path and flyovers passed as
// arguments. If no flyover is found for a hop, that hop will not have priority.
// The flyover map is modified by removing those flyovers that were used during the reservation.
func WithScionPath(p snet.Path, flyoverMap FlyoverMap) ReservationModFcn {
	return func(r *Reservation) error {
		if p == nil {
			return serrors.New("nil path")
		}
		switch p := p.Dataplane().(type) {
		case SCION:
			scionDec := &scion.Decoded{}
			if err := scionDec.DecodeFromBytes(p.Raw); err != nil {
				return serrors.Wrap("decoding scion path", err)
			}
			if err := r.setScionPath(scionDec); err != nil {
				return err
			}
		default:
			return serrors.New("Unsupported path type")
		}
		// Extend the number of hops to that of the path.
		r.Hops = make([]*Hop, len(r.Dec.HopFields))
		r.blocksPerAk = make([]cipher.Block, len(r.Hops))

		// We use the path metadata to get the IAs and interface ID sequence from it.
		interfaces := p.Metadata().Interfaces
		baseHops := InterfacesToBaseHops(interfaces)

		// Set the destination IA from the path metadata:
		r.DstIA = baseHops[len(baseHops)-1].IA

		hfIndices := reservationHopFieldIndicesForFlyovers(r.Dec)
		if len(hfIndices) != len(baseHops) {
			return serrors.New("inconsistent path metadata to hop-field mapping",
				"base_hops", len(baseHops), "hop_fields", len(hfIndices))
		}
		for i, baseHop := range baseHops {
			err := r.SetHopAndFlyover(hfIndices[i], consumeFlyover(flyoverMap, baseHop))
			if err != nil {
				return serrors.Wrap("cannot set the flyover for hop", err,
					"index", i, "base hop", baseHop)
			}
		}

		return nil
	}
}

func WithScionDataplane(p *scion.Raw, dstIA addr.IA, flyoverSeq FlyoverSequence) ReservationModFcn {
	return func(r *Reservation) error {
		if p == nil {
			return serrors.New("nil path")
		}

		scionDec, err := p.ToDecoded()
		if err != nil {
			return serrors.Wrap("cannot convert to scion decoded", err)
		}
		if err := r.setScionPath(scionDec); err != nil {
			return err
		}
		r.DstIA = dstIA

		// Extend the number of hops to that of the path.
		r.Hops = make([]*Hop, len(r.Dec.HopFields))
		r.blocksPerAk = make([]cipher.Block, len(r.Hops))

		hfIndices := reservationHopFieldIndicesForFlyovers(r.Dec)
		if len(hfIndices) != len(flyoverSeq) {
			return serrors.New("inconsistent path metadata to hop-field mapping",
				"base_hops", len(flyoverSeq), "hop_fields", len(hfIndices))
		}
		for i, baseHop := range flyoverSeq {
			// err := r.SetHopAndFlyover(hfIndices[i], consumeFlyover(flyoverSeq, baseHop))
			// Verify that the ingress and egress interfaces match:

			// Assign.
			err := r.SetHopAndFlyover(hfIndices[i], baseHop)
			if err != nil {
				return serrors.Wrap("cannot set the flyover for hop", err,
					"index", i, "base hop", baseHop)
			}
		}

		return nil
	}
}

// WithHummDataplane allows building a Reservation directly from a decoded Hummingbird dataplane
// path and a positional sequence of flyovers. The flyover sequence is aligned to the logical hop
// sequence obtained from the dataplane hop fields after collapsing segment crossovers.
func WithHummDataplane(dec *dphum.Decoded, seq FlyoverSequence) ReservationModFcn {
	return func(r *Reservation) error {
		if dec == nil {
			return serrors.New("nil hummingbird dataplane path")
		}
		r.Dec = dec
		r.Hops = make([]*Hop, len(r.Dec.HopFields))
		r.blocksPerAk = make([]cipher.Block, len(r.Hops))
		r.cloneScionMACsFromHummDecoded()

		// hopsFromDP will skip crossovers.
		hopsFromDP, err := hummDataplaneToBaseHops(r.Dec)
		if err != nil {
			return err
		}
		hfIndices := reservationHopFieldIndicesForFlyovers(r.Dec)
		if len(hopsFromDP) != len(seq) || len(hopsFromDP) != len(hfIndices) {
			return serrors.New("inconsistent hummingbird dataplane to flyover mapping",
				"base_hops", len(hopsFromDP),
				"flyover_sequence", len(seq),
				"hop_fields", len(hfIndices))
		}
		for i, hopFromDP := range hopsFromDP {
			hop := seq[i]
			if hop == nil {
				continue
			}

			if hop.BaseHop.Ingress != hopFromDP.Ingress ||
				hop.BaseHop.Egress != hopFromDP.Egress {
				return serrors.New("mismatch hop parameter and data-plane",
					"index", i,
					"hop", hop,
					"dataplane_hop", hopFromDP)
			}
			if err := r.SetHopAndFlyover(hfIndices[i], hop); err != nil {
				return serrors.Wrap("cannot set the flyover for dataplane hop", err,
					"index", i,
					"hop", hop,
					"dataplane hop", hopFromDP)
			}
		}
		return nil
	}
}

func (r *Reservation) setScionPath(dec *scion.Decoded) error {
	r.Dec = &hummingbird.Decoded{}
	r.Dec.ConvertFromScionDecoded(dec)

	// Clone the MAC fields.
	r.scionMacs = make([][6]byte, len(dec.HopFields))
	for i, hf := range dec.HopFields {
		r.scionMacs[i] = hf.Mac
	}

	return nil
}

func (r *Reservation) cloneScionMACsFromHummDecoded() {
	// deleteme: this function needs that the aggregated MACs are actually just the SCION MACs.
	r.scionMacs = make([][dppath.MacLen]byte, len(r.Dec.HopFields))
	for i, hf := range r.Dec.HopFields {
		r.scionMacs[i] = hf.HopField.Mac
	}
}

func (r *Reservation) SetHopAndFlyover(
	hfIdx uint8,
	hop *Hop,
) error {
	r.Hops[hfIdx] = hop
	if hop.Flyover == nil {
		return nil
	}

	// Find the hop field from its index.
	hf := &r.Dec.HopFields[hfIdx]

	// Validate ingress and egress.
	segIdx := r.Dec.InfIndexForHFIndex(hfIdx)
	in := hf.HopField.ConsIngress
	eg := hf.HopField.ConsEgress
	if !r.Dec.InfoFields[segIdx].ConsDir {
		in, eg = eg, in
	}

	xover := r.Dec.IsCrossOver(hfIdx)
	switch xover {
	case -1:
		if !r.Dec.InfoFields[segIdx+1].ConsDir {
			eg = r.Dec.HopFields[hfIdx+1].HopField.ConsIngress
		} else {
			eg = r.Dec.HopFields[hfIdx+1].HopField.ConsEgress
		}
	case +1:
		if !r.Dec.InfoFields[segIdx-1].ConsDir {
			in = r.Dec.HopFields[hfIdx+1].HopField.ConsEgress
		} else {
			in = r.Dec.HopFields[hfIdx+1].HopField.ConsIngress
		}
	default:
	}
	if in != hop.Ingress || eg != hop.Egress {
		return serrors.New("inconsistent flyover ingress/egress for dataplane",
			"flyover", fmt.Sprintf("in:%d, eg:%d", hop.Ingress, hop.Egress),
			"dataplane", fmt.Sprintf("in:%d, eg:%d", in, eg))
	}

	if !hf.Flyover {
		// Because we are setting a plain hop field as a flyover, it will use two more lines.
		r.Dec.NumLines += 2
		r.Dec.PathMeta.SegLen[segIdx] += 2
		hf.Flyover = true
	}

	hf.Bw = hop.Flyover.Bw
	hf.Duration = hop.Flyover.Duration
	hf.ResID = hop.Flyover.ResID

	// Prepare the AES block with the right Ak.
	block, err := aes.NewCipher(hop.Flyover.Ak[:])
	if err != nil {
		return serrors.Wrap("cannot create AES block", err)
	}
	// Set the AES block to be used by deriveDataPlanePath.
	r.blocksPerAk[hfIdx] = block

	return nil
}

func consumeFlyover(flyoverMap FlyoverMap, baseHop BaseHop) *Hop {
	flyover, ok := flyoverMap[baseHop]
	if ok {
		delete(flyoverMap, baseHop)
	}
	return &Hop{
		BaseHop: baseHop,
		Flyover: flyover,
	}
}

// reservationHopFieldIndicesForFlyovers returns the hop field indices in the Hummingbird path
// where flyovers would be written.
// I.e., Hummingbird requires its crossover hop between segments seg1->seg2
// to contain the flyover at the first hop, which is the last hop of seg1. Not all hop fields can
// contain flyovers.
func reservationHopFieldIndicesForFlyovers(dec *hummingbird.Decoded) []uint8 {
	indices := make([]uint8, 0, len(dec.HopFields))
	if dec.NumINF == 0 {
		return indices
	}

	segmentStart := 0

	// Segment 0: include all hops.
	hopCount := int(dec.Base.PathMeta.SegLen[0]) / hummingbird.HopLines
	for hopInSegment := 0; hopInSegment < hopCount; hopInSegment++ {
		indices = append(indices, uint8(segmentStart+hopInSegment))
	}
	segmentStart += hopCount

	// Remaining segments: skip the first hop in each segment.
	for segIdx := 1; segIdx < dec.NumINF; segIdx++ {
		hopCount = int(dec.Base.PathMeta.SegLen[segIdx]) / hummingbird.HopLines
		for hopInSegment := 1; hopInSegment < hopCount; hopInSegment++ {
			indices = append(indices, uint8(segmentStart+hopInSegment))
		}
		segmentStart += hopCount
	}
	return indices
}

// scionDataplaneToBaseHops maps a decoded SCION dataplane path to its logical ingress/egress
// hop sequence. Segment crossover pairs are collapsed into one logical hop.
func scionDataplaneToBaseHops(dec *scion.Decoded) ([]BaseHop, error) {
	if len(dec.HopFields) == 0 {
		return nil, nil
	}

	baseHops := make([]BaseHop, 0, len(dec.HopFields)-dec.NumINF+1)
	for segIdx, hopIdx := 0, 0; segIdx < dec.NumINF; segIdx++ {
		for hopInSegment := 0; hopInSegment < int(dec.PathMeta.SegLen[segIdx]); hopInSegment++ {
			h := dec.HopFields[hopIdx]
			in := h.ConsIngress
			eg := h.ConsEgress
			if !dec.InfoFields[segIdx].ConsDir {
				// In reverse construction direction, swap ingress with egress.
				in, eg = eg, in
			}
			hop := BaseHop{
				Ingress: in,
				Egress:  eg,
			}
			// Check for crossovers.
			if segIdx > 0 && hopInSegment == 0 {
				// Crossover. Replace the previous zero egress with the one in this hop field.
				baseHops[len(baseHops)-1].Egress = eg
			} else {
				// Not a crossover. Add the new hop field.
				baseHops = append(baseHops, hop)
			}
			hopIdx++
		}
	}
	return baseHops, nil
}

// hummDataplaneToBaseHops maps a decoded Hummingbird dataplane path to its logical ingress/egress
// hop sequence. Segment crossover pairs are collapsed into one logical hop.
func hummDataplaneToBaseHops(dec *hummingbird.Decoded) ([]BaseHop, error) {
	if len(dec.HopFields) == 0 {
		return nil, nil
	}

	baseHops := make([]BaseHop, 0, len(dec.HopFields)-dec.NumINF+1)
	for segIdx, hopIdx := 0, 0; segIdx < dec.NumINF; segIdx++ {
		for hopInSegment := 0; hopInSegment < dec.NumberOfHFsInSegment(segIdx); hopInSegment++ {
			h := dec.HopFields[hopIdx]
			in := h.HopField.ConsIngress
			eg := h.HopField.ConsEgress
			if !dec.InfoFields[segIdx].ConsDir {
				// In reverse construction direction, swap ingress with egress.
				in, eg = eg, in
			}
			hop := BaseHop{
				Ingress: in,
				Egress:  eg,
			}
			// Check for crossovers.
			if segIdx > 0 && hopInSegment == 0 {
				// Crossover. Replace the previous zero egress with the one in this hop field.
				baseHops[len(baseHops)-1].Egress = eg
			} else {
				// Not a crossover. Add the new hop field.
				baseHops = append(baseHops, hop)
			}
			hopIdx++
		}
	}
	return baseHops, nil
}

// BaseHop describes a pair of Ingress and Egress interfaces in a specific AS
type BaseHop struct {
	IA      addr.IA
	Ingress uint16
	Egress  uint16
}

type Hop struct {
	BaseHop
	Flyover *FlyoverData // nil if this hop is not reserved (just best effort)
}

type FlyoverData struct {
	ResID     uint32   // Unique per AS.
	Ak        [16]byte // Authentication key.
	Bw        uint16
	StartTime uint32 // Unix timestamp for the start of the reservation.
	Duration  uint16 // Duration of the reservation in seconds.
}

const HopNoFlyoverLen = 12 // bytes
// ResID = 22 bits
// Bw = 10 bits
// Ak = 16 bytes
// StartTime = 4 bytes
// Duration = 4 bytes
const FlyoverLen = 4 + 16 + 4 + 4
const HopWithFlyoverLen = HopNoFlyoverLen + FlyoverLen

// Len returns the length of the hop in bytes.
func (h Hop) Len() int {

	l := HopNoFlyoverLen
	if h.Flyover != nil {
		l += FlyoverLen
	}
	return l
}

func (h Hop) Serialize(buff []byte) (int, error) {
	l := h.Len()
	if len(buff) < l {
		return 0, fmt.Errorf("buffer is too small (%d bytes); expected at least %d bytes",
			len(buff), l)
	}
	buff = buff[:0]
	buff = binary.BigEndian.AppendUint64(buff, uint64(h.IA))
	buff = binary.BigEndian.AppendUint16(buff, h.Ingress)
	buff = binary.BigEndian.AppendUint16(buff, h.Egress)

	if h.Flyover != nil {
		buff = binary.BigEndian.AppendUint32(buff, h.Flyover.ResID<<10|uint32(h.Flyover.Bw))
		buff = append(buff, h.Flyover.Ak[:]...)
		buff = binary.BigEndian.AppendUint32(buff, h.Flyover.StartTime)
		buff = binary.BigEndian.AppendUint16(buff, h.Flyover.Duration)
	}
	return l, nil
}

func (h *Hop) Deserialize(buff []byte, hasFlyover bool) error {
	expected := HopNoFlyoverLen
	if hasFlyover {
		expected += FlyoverLen
	}
	if len(buff) < expected {
		return fmt.Errorf("buffer is too small (%d bytes); expected at least %d bytes",
			len(buff), HopNoFlyoverLen+FlyoverLen)
	}
	h.IA = addr.IA(binary.BigEndian.Uint64(buff))
	buff = buff[8:]
	h.Ingress = binary.BigEndian.Uint16(buff)
	buff = buff[2:]
	h.Egress = binary.BigEndian.Uint16(buff)
	buff = buff[2:]

	if hasFlyover {
		h.Flyover = &FlyoverData{}
		resIdBw := binary.BigEndian.Uint32(buff)
		buff = buff[4:]
		h.Flyover.ResID = resIdBw >> 10
		h.Flyover.Bw = uint16(resIdBw) & 0x000003FF // lowest 10 bits
		copy(h.Flyover.Ak[:], buff)
		buff = buff[16:]
		h.Flyover.StartTime = binary.BigEndian.Uint32(buff)
		buff = buff[4:]
		h.Flyover.Duration = binary.BigEndian.Uint16(buff)
		buff = buff[2:]
	}
	return nil
}

func LenOfSerializedHops(hops []*Hop) int {
	l := 1                                   // 1 byte for the hop count.
	l += backingBytesForHopBitset(len(hops)) // bytes to hold the flyover flags.
	for _, h := range hops {
		l += h.Len() // For each hop
	}
	return l
}

// SerializeHops serializes up to 255 hops.
// The structure of the buffer ends up as:
// - Hop count, 1 byte.
// - Flyover flag bitset, for all hops; (hop count+7) / 8
// - Sequence of Hops.
func SerializeHops(buff []byte, hops []*Hop) (int, error) {
	if len(hops) > 255 {
		return 0, fmt.Errorf("cannot serialize more than 255 hops, requested %d", len(hops))
	}
	// Check size.
	expectedSize := LenOfSerializedHops(hops)
	if len(buff) < expectedSize {
		return 0, fmt.Errorf("buffer with length %d is too small; required %d bytes",
			len(buff), expectedSize)
	}

	// Serialize.
	buff[0] = byte(len(hops))
	buff = buff[1:]
	// This holds the flyover bitset.
	flags := newHopBitset(buff, len(hops))
	flags.Clear()
	n := backingBytesForHopBitset(len(hops))
	buff = buff[n:]

	var err error
	for i, h := range hops {
		if h.Flyover != nil {
			flags.Set(i, true)
		}
		n, err = h.Serialize(buff)
		if err != nil {
			return n, serrors.Wrap("serializing hop field", err, "i", i)
		}
		buff = buff[n:]
	}
	return expectedSize, nil
}

func DeserializeHops(buff []byte) ([]*Hop, error) {
	if len(buff) == 0 {
		return nil, nil
	}
	N := int(buff[0])

	expectedLen := 1 + backingBytesForHopBitset(N)
	if len(buff) < expectedLen {
		return nil, fmt.Errorf("deserialize hops: buffer too small, expected >= %d, got %d bytes",
			expectedLen, len(buff))
	}
	flags := newHopBitset(buff[1:], N)
	hopData := buff[expectedLen:] // Starts right after the bitset.
	expectedLen += HopWithFlyoverLen*flags.CountOnes() + HopNoFlyoverLen*flags.CountZeroes()

	if len(buff) < expectedLen {
		return nil, fmt.Errorf("deserialize hops: buffer too small, expected >= %d, got %d bytes",
			expectedLen, len(buff))
	}

	hops := make([]*Hop, N)
	for i := range N {
		hops[i] = &Hop{}
		hasFlyover := flags.Get(i)
		err := hops[i].Deserialize(hopData, hasFlyover)
		if err != nil {
			return nil, serrors.Wrap("deserialize hops", err)
		}
		if hasFlyover {
			hopData = hopData[HopWithFlyoverLen:]
		} else {
			hopData = hopData[HopNoFlyoverLen:]
		}
	}
	return hops, nil
}

// FlyoverSequence represents a sequence of hops. These hops may contain flyovers.
type FlyoverSequence []*Hop

// FlyoverMap is a map between a flyover <IA,ingress,egress> and its corresponding data.
type FlyoverMap map[BaseHop]*FlyoverData

func FlyoversToMap(hops []*Hop) FlyoverMap {
	ret := make(FlyoverMap)
	for _, hop := range hops {
		k := hop.BaseHop
		ret[k] = hop.Flyover
	}
	return ret
}

// InterfacesToBaseHops maps path metadata interfaces to per-AS ingress/egress hop tuples.
func InterfacesToBaseHops(ifaces []snet.PathInterface) []BaseHop {
	if len(ifaces) == 0 {
		return nil
	}
	baseHops := make([]BaseHop, 0, len(ifaces)/2+1)
	baseHops = append(baseHops, BaseHop{
		IA:      ifaces[0].IA,
		Ingress: 0,
		Egress:  uint16(ifaces[0].ID),
	})

	for i := 1; i < len(ifaces); i += 2 {
		egress := uint16(0)
		if i+1 < len(ifaces) {
			egress = uint16(ifaces[i+1].ID)
		}
		baseHops = append(baseHops, BaseHop{
			IA:      ifaces[i].IA,
			Ingress: uint16(ifaces[i].ID),
			Egress:  egress,
		})
	}
	return baseHops
}

type hopBitset struct {
	nBits int
	buf   []byte
}

func backingBytesForHopBitset(nBits int) int {
	return (nBits + 7) / 8
}

func newHopBitset(backing []byte, nBits int) hopBitset {
	nBytes := backingBytesForHopBitset(nBits)
	b := backing[:nBytes]
	return hopBitset{
		nBits: nBits,
		buf:   b,
	}
}

func (b hopBitset) Clear() {
	clear(b.buf) // all bits to zero
}

func (b hopBitset) Set(i int, value bool) {
	byteIdx := i / 8
	bitIdx := uint(i % 8)

	mask := byte(1 << bitIdx) // LSB-first within each byte
	if value {
		b.buf[byteIdx] |= mask
	} else {
		b.buf[byteIdx] &^= mask
	}
}

func (b hopBitset) Get(i int) bool {
	byteIdx := i / 8
	bitIdx := uint(i % 8)
	return b.buf[byteIdx]&(1<<bitIdx) != 0
}

func (b hopBitset) TotalBits() int {
	return b.nBits
}

func (b hopBitset) CountOnes() int {
	count := 0
	for i := range b.nBits {
		if b.Get(i) {
			count++
		}
	}
	return count
}

func (b hopBitset) CountZeroes() int {
	return b.TotalBits() - b.CountOnes()
}
