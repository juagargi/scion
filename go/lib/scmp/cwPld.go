package scmp

import (
	"bytes"
	"fmt"

	"github.com/scionproto/scion/go/lib/common"
)

var _ common.Payload = (*CWPayload)(nil)

type CWPayload struct {
	Meta  *CWMeta
	Info  Info
	L4Hdr common.RawBytes
}

func CWPldFromRaw(b common.RawBytes, t Type) (*CWPayload, error) {
	var err error
	p := &CWPayload{}
	buf := bytes.NewBuffer(b)
	if p.Meta, err = CWMetaFromRaw(buf.Next(CWMetaLen)); err != nil {
		return nil, err
	}
	if p.Info, err = ParseInfo(buf.Next(int(p.Meta.InfoLen)*common.LineLen), ClassType{Class: C_General, Type: t}); err != nil {
		return nil, err
	}
	p.L4Hdr = buf.Next(int(p.Meta.L4HdrLen) * common.LineLen)
	return p, nil
}

func NotifyPld(info Info, l4 common.L4ProtocolType, f QuoteFunc) *CWPayload {
	p := &CWPayload{Info: info}
	p.L4Hdr = f(RawL4Hdr) //only include the L4Hdr thus don't need classTypeQuotes()
	p.Meta = &CWMeta{L4HdrLen: uint8(len(p.L4Hdr) / common.LineLen),
		L4Proto: l4}
	if info != nil {
		p.Meta.InfoLen = uint8(p.Info.Len() / common.LineLen)
	}
	return p
}

func (p *CWPayload) Copy() (common.Payload, error) {
	if p == nil {
		return nil, nil
	}
	c := &CWPayload{}
	c.Meta = p.Meta.Copy()
	c.Info = p.Info
	c.L4Hdr = append(common.RawBytes(nil), p.L4Hdr...)
	return c, nil
}

func (p *CWPayload) WritePld(b common.RawBytes) (int, error) {
	if p.Len() > len(b) {
		return 0, common.NewBasicError("Not engough space in buffer", nil,
			"actual", len(b), "expected", p.Len())
	}
	offset := 0
	if err := p.Meta.Write(b[0:]); err != nil {
		return 0, err
	}
	offset += CWMetaLen
	if p.Info != nil {
		if count, err := p.Info.Write(b[offset:]); err != nil {
			return 0, err
		} else {
			offset += count
		}
	}
	copy(b[offset:], p.L4Hdr)
	return p.Len(), nil
}

func (p *CWPayload) Len() int {
	l := CWMetaLen
	if p.Info != nil {
		l += p.Info.Len()
	}
	l += int(p.Meta.L4HdrLen) * common.LineLen
	return l
}

func (p *CWPayload) String() string {
	buf := &bytes.Buffer{}
	fmt.Fprintf(buf, "Meta: %v\n", p.Meta)
	if p.Info != nil {
		fmt.Fprintf(buf, "Info: %v\n", p.Info)
	}
	if p.L4Hdr != nil {
		fmt.Fprintf(buf, "L4Hdr: %v\n", p.L4Hdr)
	}
	return buf.String()
}
