package queues

import (
	"github.com/scionproto/scion/go/border/rpkt"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/common"
)

// CachelessClassRule implements ClassRuleInterface
type CachelessClassRule struct {
	maskMatched, maskSad, maskDas, maskLf, maskIntf []bool
	extensions                                      []common.ExtnType
}

var _ ClassRuleInterface = (*CachelessClassRule)(nil)

func (rc *CachelessClassRule) Init(noRules int) {
	rc.extensions = make([]common.ExtnType, 255)

	rc.maskMatched = make([]bool, noRules)
	rc.maskSad = make([]bool, noRules)
	rc.maskDas = make([]bool, noRules)
	rc.maskLf = make([]bool, noRules)
	rc.maskIntf = make([]bool, noRules)
}

// GetRuleForPacket returns the rule for rp
func (rc *CachelessClassRule) GetRuleForPacket(
	config *InternalRouterConfig, rp *rpkt.RtrPkt) *InternalClassRule {

	var returnRule *InternalClassRule
	var exactAndRangeSourceMatches []*InternalClassRule
	var exactAndRangeDestinationMatches []*InternalClassRule
	var sourceAnyDestinationMatches []*InternalClassRule
	var destinationAnySourceRules []*InternalClassRule
	var asOnlySourceRules []*InternalClassRule
	var asOnlyDestinationRules []*InternalClassRule
	var isdOnlySourceRules []*InternalClassRule
	var isdOnlyDestinationRules []*InternalClassRule
	var interfaceIncomingRules []*InternalClassRule
	var matched []*InternalClassRule
	var l4OnlyRules []*InternalClassRule
	var srcAddr, dstAddr addr.IA
	var l4t common.L4ProtocolType
	var intf uint64

	var sources [3][]*InternalClassRule
	var destinations [3][]*InternalClassRule

	srcAddr, _ = rp.SrcIA()
	dstAddr, _ = rp.DstIA()
	intf = uint64(rp.Ingress.IfID)

	l4t = rp.L4Type
	hbhext := rp.HBHExt
	e2eext := rp.E2EExt
	for k := 0; k < len(hbhext); k++ {
		ext, _ := hbhext[k].GetExtn()
		rc.extensions = append(rc.extensions, ext.Type())
	}
	for k := 0; k < len(e2eext); k++ {
		ext, _ := e2eext[k].GetExtn()
		rc.extensions = append(rc.extensions, ext.Type())
	}

	returnRule = emptyRule

	exactAndRangeSourceMatches = config.Rules.SourceRules[srcAddr]
	exactAndRangeDestinationMatches = config.Rules.DestinationRules[dstAddr]

	sourceAnyDestinationMatches = config.Rules.SourceAnyDestinationRules[srcAddr]
	destinationAnySourceRules = config.Rules.DestinationAnySourceRules[dstAddr]

	asOnlySourceRules = config.Rules.ASOnlySourceRules[srcAddr.A]
	asOnlyDestinationRules = config.Rules.ASOnlyDestRules[dstAddr.A]

	isdOnlySourceRules = config.Rules.ISDOnlySourceRules[srcAddr.I]
	isdOnlyDestinationRules = config.Rules.ISDOnlyDestRules[dstAddr.I]

	interfaceIncomingRules = config.Rules.InterfaceIncomingRules[intf]

	l4OnlyRules = config.Rules.L4OnlyRules

	sources[0] = exactAndRangeSourceMatches
	sources[1] = asOnlySourceRules
	sources[2] = isdOnlySourceRules

	destinations[0] = exactAndRangeDestinationMatches
	destinations[1] = asOnlyDestinationRules
	destinations[2] = isdOnlyDestinationRules

	matched = intersectListsRules(sources, destinations)

	matchL4Type(rc.maskMatched, &matched, l4t, rc.extensions)
	matchL4Type(rc.maskSad, &sourceAnyDestinationMatches, l4t, rc.extensions)
	matchL4Type(rc.maskDas, &destinationAnySourceRules, l4t, rc.extensions)
	matchL4Type(rc.maskLf, &l4OnlyRules, l4t, rc.extensions)
	matchL4Type(rc.maskIntf, &interfaceIncomingRules, l4t, rc.extensions)

	max := -1
	max, returnRule = getRuleWithPrevMax(returnRule, rc.maskMatched, matched, max)
	max, returnRule = getRuleWithPrevMax(returnRule, rc.maskSad, sourceAnyDestinationMatches, max)
	max, returnRule = getRuleWithPrevMax(returnRule, rc.maskDas, destinationAnySourceRules, max)
	max, returnRule = getRuleWithPrevMax(returnRule, rc.maskIntf, interfaceIncomingRules, max)
	_, returnRule = getRuleWithPrevMax(returnRule, rc.maskLf, l4OnlyRules, max)

	return returnRule
}
