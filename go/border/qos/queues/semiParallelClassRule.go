package queues

import (
	"github.com/scionproto/scion/go/border/rpkt"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/common"
)

// SemiParallelClassRule contains helper arrays to store
// temporary results.
type SemiParallelClassRule struct {
	result []*InternalClassRule

	sources      [4][]*InternalClassRule
	destinations [4][]*InternalClassRule
}

var _ ClassRuleInterface = (*SemiParallelClassRule)(nil)

// GetRuleForPacket returns the rule for rp
func (pcr *SemiParallelClassRule) GetRuleForPacket(
	config *InternalRouterConfig,
	rp *rpkt.RtrPkt) *InternalClassRule {

	done := make(chan bool, 3)

	var srcAddr addr.IA
	var dstAddr addr.IA
	var extensions []common.ExtnType
	var l4t common.L4ProtocolType
	intf = uint64(rp.Ingress.IfID)

	go func(dun chan bool) {
		srcAddr, _ = rp.SrcIA()
		dun <- true
	}(done)
	go func(dun chan bool) {
		dstAddr, _ = rp.DstIA()
		dun <- true
	}(done)
	go func(dun chan bool) {

		l4t = rp.L4Type
		hbhext := rp.HBHExt
		e2eext := rp.E2EExt
		for k := 0; k < len(hbhext); k++ {
			ext, _ := hbhext[k].GetExtn()
			extensions = append(extensions, ext.Type())
		}
		for k := 0; k < len(e2eext); k++ {
			ext, _ := e2eext[k].GetExtn()
			extensions = append(extensions, ext.Type())
		}

		dun <- true
	}(done)

	for i := 0; i < cap(done); i++ {
		<-done
	}

	entry := cacheEntry{srcAddress: srcAddr, dstAddress: dstAddr, intf: intf, l4type: l4t}

	returnRule = config.Rules.CrCache.Get(entry)

	if returnRule != nil {
		if matchRuleL4Type(returnRule, extensions) {
			return returnRule
		}
	}

	returnRule = emptyRule

	done = make(chan bool, 8)
	go func(dun chan bool) {
		pcr.getMatchFromMap(
			config,
			&config.Rules.SourceRules,
			srcAddr,
			&pcr.sources,
			0,
			done)

		pcr.getMatchFromMap(
			config,
			&config.Rules.DestinationRules,
			dstAddr,
			&pcr.destinations,
			0,
			done)

		pcr.getMatchFromMap(
			config,
			&config.Rules.SourceAnyDestinationRules,
			srcAddr,
			&pcr.sources,
			3,
			done)
		pcr.getMatchFromMap(
			config,
			&config.Rules.DestinationAnySourceRules,
			dstAddr,
			&pcr.destinations,
			3,
			done)
	}(done)

	go func(dun chan bool) {
		pcr.getMatchASFromMap(
			config,
			&config.Rules.ASOnlySourceRules,
			srcAddr.A,
			&pcr.sources,
			1,
			done)
		pcr.getMatchASFromMap(
			config,
			&config.Rules.ASOnlyDestRules,
			dstAddr.A,
			&pcr.destinations,
			1,
			done)
		pcr.getMatchISDFromMap(
			config,
			&config.Rules.ISDOnlySourceRules,
			srcAddr.I,
			&pcr.sources,
			2,
			done)
		pcr.getMatchISDFromMap(
			config,
			&config.Rules.ISDOnlyDestRules,
			dstAddr.I,
			&pcr.destinations,
			2,
			done)
	}(done)

	for i := 0; i < cap(done); i++ {
		<-done
	}

	interfaceIncomingRules = config.Rules.InterfaceIncomingRules[intf]
	l4OnlyRules = config.Rules.L4OnlyRules

	matched = intersectLongListsRules(pcr.sources, pcr.destinations)

	maskMatched = make([]bool, len(matched))
	maskSad = make([]bool, len(pcr.sources[3]))
	maskDas = make([]bool, len(pcr.destinations[3]))
	maskLf = make([]bool, len(l4OnlyRules))
	maskIntf = make([]bool, len(l4OnlyRules))

	matchL4Type(maskMatched, &matched, l4t, extensions)
	matchL4Type(maskSad, &pcr.sources[3], l4t, extensions)
	matchL4Type(maskDas, &pcr.destinations[3], l4t, extensions)
	matchL4Type(maskLf, &l4OnlyRules, l4t, extensions)
	matchL4Type(maskIntf, &interfaceIncomingRules, l4t, extensions)

	var result [5]*InternalClassRule

	for i := 0; i < len(result); i++ {
		result[i] = emptyRule
	}

	done = make(chan bool, 2)

	go func(dun chan bool) {
		_, result[0] = getRuleWithPrevMax(returnRule, maskMatched, matched, -1)
		_, result[1] = getRuleWithPrevMax(returnRule, maskSad, pcr.sources[3], -1)
		dun <- true
	}(done)
	go func(dun chan bool) {
		_, result[2] = getRuleWithPrevMax(returnRule, maskDas, pcr.destinations[3], -1)
		_, result[3] = getRuleWithPrevMax(returnRule, maskLf, l4OnlyRules, -1)
		_, result[4] = getRuleWithPrevMax(returnRule, maskIntf, interfaceIncomingRules, -1)
		dun <- true
	}(done)

	for i := 0; i < cap(done); i++ {
		<-done
	}
	for i := 0; i < len(result); i++ {
		if result[i].Priority > returnRule.Priority {
			returnRule = result[i]
		}
	}

	config.Rules.CrCache.Put(entry, returnRule)

	return returnRule
}

func (pcr *SemiParallelClassRule) getMatchISDFromMap(
	config *InternalRouterConfig,
	m *map[addr.ISD][]*InternalClassRule,
	address addr.ISD,
	result *[4][]*InternalClassRule,
	resultSpot int,
	done chan bool) {

	returnRule = emptyRule
	exactAndRangeSourceMatches = (*m)[address]
	result[resultSpot] = exactAndRangeSourceMatches
	done <- true
}

func (pcr *SemiParallelClassRule) getMatchASFromMap(
	config *InternalRouterConfig,
	m *map[addr.AS][]*InternalClassRule,
	address addr.AS,
	result *[4][]*InternalClassRule,
	resultSpot int,
	done chan bool) {

	returnRule = emptyRule
	exactAndRangeSourceMatches = (*m)[address]
	result[resultSpot] = exactAndRangeSourceMatches
	done <- true
}

func (pcr *SemiParallelClassRule) getMatchFromMap(
	config *InternalRouterConfig,
	m *map[addr.IA][]*InternalClassRule,
	address addr.IA,
	result *[4][]*InternalClassRule,
	resultSpot int,
	done chan bool) {

	returnRule = emptyRule
	exactAndRangeSourceMatches = (*m)[address]
	result[resultSpot] = exactAndRangeSourceMatches
	done <- true
}
