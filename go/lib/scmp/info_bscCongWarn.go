package scmp

import (
	"fmt"

	"github.com/scionproto/scion/go/lib/common"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/util"
)

//IMPL: Defines the new SCMP type used for basic information dissemination
var _ Info = (*InfoBscCW)(nil) //Interface assertion

const (
	bscCWLen = 40
)

type InfoBscCW struct {
	CurrBW        uint64
	QueueLength   uint64
	QueueFullness uint64
	ConsIngress   common.IFIDType
	Violation     uint64
	//Path          *spath.Path
	//QueueNo       uint64 //MS: used for debugging
}

func InfoBscCWFromRaw(b common.RawBytes) (*InfoBscCW, error) {
	if len(b) < bscCWLen {
		return nil, serrors.New("Unable to parse InfoBscCW, small buffer size")
	}
	i := &InfoBscCW{}
	i.CurrBW = common.Order.Uint64(b)
	i.QueueLength = common.Order.Uint64(b[8:])
	i.QueueFullness = common.Order.Uint64(b[16:])
	i.ConsIngress = common.IFIDType(common.Order.Uint64(b[24:]))
	i.Violation = common.Order.Uint64(b[32:])
	//i.Path = spath.New(b[40:])
	//i.QueueNo = common.Order.Uint64((b[24:]))
	return i, nil
}
func (i *InfoBscCW) Copy() Info {
	if i == nil {
		return nil
	}
	return &InfoBscCW{CurrBW: i.CurrBW, QueueLength: i.QueueLength, QueueFullness: i.QueueFullness,
		ConsIngress: i.ConsIngress, Violation: i.Violation} //, Path: i.Path		, QueueNo: i.QueueNo

}

func (i *InfoBscCW) Len() int {
	return bscCWLen + util.CalcPadding(bscCWLen, common.LineLen) //i.Path.Raw.Len() + +i.Path.Raw.Len()
}

func (i *InfoBscCW) Write(b common.RawBytes) (int, error) {
	common.Order.PutUint64(b, i.CurrBW)
	common.Order.PutUint64(b[8:], i.QueueLength)
	common.Order.PutUint64(b[16:], i.QueueFullness)
	common.Order.PutUint64(b[24:], uint64(i.ConsIngress))
	common.Order.PutUint64(b[32:], i.Violation)
	// if _, err := (i.Path.Raw).WritePld(b[40:]); err != nil {
	// 	return 0, err
	// }
	// if i.Path.InfOff < 0 {
	// 	return 0, common.NewBasicError("Negative InfOff", nil, "cannot convert to uint64")
	// }
	// common.Order.PutUint64(b[i.Path.Raw.Len()+40:], uint64(i.Path.InfOff))
	// if i.Path.HopOff < 0 {
	// 	return 0, common.NewBasicError("Negative HopOff", nil, "cannot convert to uint64")
	// }
	// common.Order.PutUint64(b[i.Path.Raw.Len()+48:], uint64(i.Path.HopOff))
	//_, _ = i.Path.Write(b[40:])
	//common.Order.PutUint64(b[24:], i.QueueNo)
	//_, _ = i.Path.Write(b[40:])

	return util.FillPadding(b, bscCWLen, common.LineLen), nil //+i.Path.Len() +i.Path.Raw.Len()
}

func (i *InfoBscCW) String() string {
	return fmt.Sprintf("CurrBW=%d QueueLength=%d QueueFullness=%d  ConsIngress=%d Violation=%d ", //Path=%s	QueueNo=%d Path: Raw=%s InfOff=%d HopOff=%d
		i.CurrBW, i.QueueLength, i.QueueFullness, i.ConsIngress, i.Violation) //, i.Path.String()	, i.QueueNo , i.Path.Raw.String(), i.Path.InfOff, i.Path.HopOff
}
