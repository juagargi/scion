package scmp

import (
	"encoding/binary"
	"fmt"

	"github.com/scionproto/scion/go/lib/common"
	"gopkg.in/restruct.v1"
)

//Q: Which headers should we include if any at all? Should include the L4hdr
//such that the dispatcher can read out the original source host port
type CWMeta struct {
	InfoLen  uint8
	L4HdrLen uint8
	L4Proto  common.L4ProtocolType
}

const (
	CWMetaLen = 3
)

func CWMetaFromRaw(b []byte) (*CWMeta, error) {
	if len(b) < CWMetaLen {
		return nil, common.NewBasicError("Can't parse SCMP CWmeta subheader, buffer is too short",
			nil, "expected", CWMetaLen, "actual", len(b))
	}
	m := &CWMeta{}
	if err := restruct.Unpack(b, binary.BigEndian, m); err != nil {
		return nil, common.NewBasicError("Failed to unpack SCMP CWMetadata", err)
	}
	return m, nil

}

func (m *CWMeta) Copy() *CWMeta {
	if m == nil {
		return nil
	}
	return &CWMeta{InfoLen: m.InfoLen, L4HdrLen: m.L4HdrLen, L4Proto: m.L4Proto}
}

func (m *CWMeta) Write(b common.RawBytes) error {
	out, err := restruct.Pack(common.Order, m)
	if err != nil {
		return common.NewBasicError("Error packing SCMP CWMetadata", err)
	}
	copy(b, out)
	return nil
}

func (m *CWMeta) String() string {
	return fmt.Sprintf(
		"CW=%d L4Hdr=%d L4Proto=%v", m.InfoLen, m.L4HdrLen, m.L4Proto)

}
