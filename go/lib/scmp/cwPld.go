package scmp

import (
	"bytes"
	"fmt"

	"github.com/scionproto/scion/go/lib/common"
)

var _ common.Payload = (*CWPayload)(nil)

type CWPayload struct {
	Meta *CWMeta
	Info Info
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
	return p, nil
}

/*TODO: MS: implement this similarly to the PldFromQuotes function but instead
of deciding which headers to quote, restrict the information content*/
func NotifyPld(info Info) *CWPayload {
	p := &CWPayload{Info: info}

	p.Meta = &CWMeta{}
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
	return c, nil
}

func (p *CWPayload) WritePld(b common.RawBytes) (int, error) {
	if p.Len() > len(b) {
		return 0, common.NewBasicError("Not engough space in buffer", nil,
			"actual", len(b), "expected", p.Len())
	}
	if err := p.Meta.Write(b[0:]); err != nil {
		return 0, err
	}
	if p.Info != nil {
		if _, err := p.Info.Write(b[CWMetaLen:]); err != nil {
			return 0, err
		}
	}
	return p.Len(), nil
}

func (p *CWPayload) Len() int {
	l := CWMetaLen
	if p.Info != nil {
		l += p.Info.Len()
	}
	return l
}

func (p *CWPayload) String() string {
	buf := &bytes.Buffer{}
	fmt.Fprintf(buf, "Meta: %v\n", p.Meta)
	if p.Info != nil {
		fmt.Fprintf(buf, "Info: %v\n", p.Info)
	}
	return buf.String()
}
