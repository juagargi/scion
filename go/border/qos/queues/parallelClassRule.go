package queues

import (
	"fmt"

	"github.com/scionproto/scion/go/border/rpkt"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/common"
)

// ParallelClassRule contains helper arrays to store
// temporary results.
type ParallelClassRule struct {
	result []*InternalClassRule

	sources      [4][]*InternalClassRule
	destinations [4][]*InternalClassRule
}

var _ ClassRuleInterface = (*ParallelClassRule)(nil)

// GetRuleForPacket returns the rule for rp
func (pcr *ParallelClassRule) GetRuleForPacket(
	config *InternalRouterConfig,
	rp *rpkt.RtrPkt) *InternalClassRule {

	done := make(chan bool, 3)

	var srcAddr addr.IA
	var dstAddr addr.IA
	var l4t common.L4ProtocolType
	var extensions []common.ExtnType

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

	// exactAndRangeSourceMatches = config.Rules.SourceRules[srcAddr]
	go pcr.getMatchFromMap(
		config,
		&config.Rules.SourceRules,
		srcAddr,
		&pcr.sources,
		0,
		done)
	// exactAndRangeDestinationMatches = config.Rules.DestinationRules[dstAddr]
	go pcr.getMatchFromMap(
		config,
		&config.Rules.DestinationRules,
		dstAddr,
		&pcr.destinations,
		0,
		done)

	// sourceAnyDestinationMatches = config.Rules.SourceAnyDestinationRules[srcAddr]
	go pcr.getMatchFromMap(
		config,
		&config.Rules.SourceAnyDestinationRules,
		srcAddr,
		&pcr.sources,
		3,
		done)
	// destinationAnySourceRules = config.Rules.DestinationAnySourceRules[dstAddr]
	go pcr.getMatchFromMap(
		config,
		&config.Rules.DestinationAnySourceRules,
		dstAddr,
		&pcr.destinations,
		3,
		done)

	// asOnlySourceRules = config.Rules.ASOnlySourceRules[srcAddr.A]
	go pcr.getMatchASFromMap(
		config,
		&config.Rules.ASOnlySourceRules,
		srcAddr.A,
		&pcr.sources,
		1,
		done)
	// asOnlyDestinationRules = config.Rules.ASOnlyDestRules[dstAddr.A]
	go pcr.getMatchASFromMap(
		config,
		&config.Rules.ASOnlyDestRules,
		dstAddr.A,
		&pcr.destinations,
		1,
		done)

	// isdOnlySourceRules = config.Rules.ISDOnlySourceRules[srcAddr.I]
	go pcr.getMatchISDFromMap(
		config,
		&config.Rules.ISDOnlySourceRules,
		srcAddr.I,
		&pcr.sources,
		2,
		done)
	// isdOnlyDestinationRules = config.Rules.ISDOnlyDestRules[dstAddr.I]
	go pcr.getMatchISDFromMap(
		config,
		&config.Rules.ISDOnlyDestRules,
		dstAddr.I,
		&pcr.destinations,
		2,
		done)

	for i := 0; i < cap(done); i++ {
		<-done
	}

	l4OnlyRules = config.Rules.L4OnlyRules

	matched = intersectLongListsRules(pcr.sources, pcr.destinations)
	interfaceIncomingRules = config.Rules.InterfaceIncomingRules[intf]

	maskMatched = make([]bool, len(matched))
	maskSad = make([]bool, len(pcr.sources[3]))
	maskDas = make([]bool, len(pcr.destinations[3]))
	maskLf = make([]bool, len(l4OnlyRules))
	maskIntf = make([]bool, len(l4OnlyRules))

	matchL4Type(maskMatched, &matched, l4t, extensions)
	matchL4Type(maskSad, &pcr.sources[3], l4t, extensions)
	matchL4Type(maskDas, &pcr.destinations[3], l4t, extensions)
	matchL4Type(maskLf, &l4OnlyRules, l4t, extensions)
	maskIntf = make([]bool, len(l4OnlyRules))

	var result [5]*InternalClassRule

	for i := 0; i < len(result); i++ {
		result[i] = emptyRule
	}

	done = make(chan bool, 5)

	go func(dun chan bool) {
		_, result[0] = getRuleWithPrevMax(returnRule, maskMatched, matched, -1)
		dun <- true
	}(done)
	go func(dun chan bool) {
		_, result[1] = getRuleWithPrevMax(returnRule, maskSad, pcr.sources[3], -1)
		dun <- true
	}(done)
	go func(dun chan bool) {
		_, result[2] = getRuleWithPrevMax(returnRule, maskDas, pcr.destinations[3], -1)
		dun <- true
	}(done)
	go func(dun chan bool) {
		_, result[3] = getRuleWithPrevMax(returnRule, maskLf, l4OnlyRules, -1)
		dun <- true
	}(done)
	go func(dun chan bool) {
		_, result[4] = getRuleWithPrevMax(returnRule, maskIntf, interfaceIncomingRules, -1)
		dun <- true
	}(done)

	for i := 0; i < cap(done); i++ {
		<-done
	}
	for i := 0; i < cap(done); i++ {
		if result[i].Priority >= returnRule.Priority {
			returnRule = result[i]
		}
	}

	config.Rules.CrCache.Put(entry, returnRule)

	return returnRule
}

func (pcr *ParallelClassRule) getMatchISDFromMap(
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

func (pcr *ParallelClassRule) getMatchASFromMap(
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

func (pcr *ParallelClassRule) getMatchFromMap(
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

func intersectLongListsRules(
	a [4][]*InternalClassRule,
	b [4][]*InternalClassRule) []*InternalClassRule {

	for i := 0; i < len(matches); i++ {
		matches[i] = nil
	}
	k := 0

	for l := 0; l < 3; l++ {
		for m := 0; m < 3; m++ {
			lb := len(b[m])
			la := len(a[l])
			for i := 0; i < la; i++ {
				for j := 0; j < lb; j++ {
					if a[l][i] == b[m][j] {
						matches[k] = a[l][i]
						k++
					}
				}
			}
		}
	}
	return matches
}

func getRuleWithMaxFrom(
	result *InternalClassRule,
	mask []bool,
	list []*InternalClassRule,
	done *chan bool) {

	prevMax := -1
	for i := 0; i < len(list); i++ {
		if mask[i] {
			if list[i].Priority > prevMax {
				fmt.Println("list is", list[i])
				result = list[i]
				fmt.Println("Will return", result)
				prevMax = list[i].Priority
			}
		} else {
			break
		}
	}
	fmt.Println("Returning", result)
	*done <- true
}
