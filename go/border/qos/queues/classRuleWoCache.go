package queues

import (
	"github.com/scionproto/scion/go/border/rpkt"
)

// CachelessClassRule implements ClassRuleInterface
type CachelessClassRule struct{}

var _ ClassRuleInterface = (*CachelessClassRule)(nil)

// GetRuleForPacket returns the rule for rp
func (*CachelessClassRule) GetRuleForPacket(
	config *InternalRouterConfig, rp *rpkt.RtrPkt) *InternalClassRule {

	var sources [3][]*InternalClassRule
	var destinations [3][]*InternalClassRule

	srcAddr, _ = rp.SrcIA()
	dstAddr, _ = rp.DstIA()
	intf = uint64(rp.Ingress.IfID)

	l4t = rp.L4Type
	hbhext := rp.HBHExt
	e2eext := rp.E2EExt
	for k := 0; k < len(hbhext); k++ {
		ext, _ = hbhext[k].GetExtn()
		extensions = append(extensions, ext.Type())
	}
	for k := 0; k < len(e2eext); k++ {
		ext, _ = e2eext[k].GetExtn()
		extensions = append(extensions, ext.Type())
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

	maskMatched = make([]bool, len(matched))
	maskSad = make([]bool, len(sourceAnyDestinationMatches))
	maskDas = make([]bool, len(destinationAnySourceRules))
	maskLf = make([]bool, len(l4OnlyRules))
	maskIntf = make([]bool, len(l4OnlyRules))

	matchL4Type(maskMatched, &matched, l4t, extensions)
	matchL4Type(maskSad, &sourceAnyDestinationMatches, l4t, extensions)
	matchL4Type(maskDas, &destinationAnySourceRules, l4t, extensions)
	matchL4Type(maskLf, &l4OnlyRules, l4t, extensions)
	matchL4Type(maskIntf, &interfaceIncomingRules, l4t, extensions)

	max := -1
	max, returnRule = getRuleWithPrevMax(returnRule, maskMatched, matched, max)
	max, returnRule = getRuleWithPrevMax(returnRule, maskSad, sourceAnyDestinationMatches, max)
	max, returnRule = getRuleWithPrevMax(returnRule, maskDas, destinationAnySourceRules, max)
	max, returnRule = getRuleWithPrevMax(returnRule, maskIntf, interfaceIncomingRules, max)
	_, returnRule = getRuleWithPrevMax(returnRule, maskLf, l4OnlyRules, max)

	return returnRule
}
