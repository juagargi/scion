// Copyright 2020 ETH Zurich
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

package queues

import (
	"strconv"
	"strings"

	"github.com/scionproto/scion/go/border/qos/conf"
	"github.com/scionproto/scion/go/border/rpkt"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/common"
)

// ClassRuleInterface allows to get the rule matchting rp from implementing structs
type ClassRuleInterface interface {
	GetRuleForPacket(config *InternalRouterConfig, rp *rpkt.RtrPkt) *InternalClassRule
	Init(noRules int)
}

// ProtocolMatchType is combination of l4protocol and extension type
// that can be matched against incoming router packets.
// Note that the extension here is an int and the extension in the rp
// (taken from common.ExtnType) is an uint8. -1 has meaning any for
// ProtocolMatchType and in common.ExtnType -1 equals
// type 255 an actual protocol type.
type ProtocolMatchType struct {
	baseProtocol common.L4ProtocolType
	extension    int
}

// InternalClassRule is the QoS subsystems internal representation of a rule from
// the config file.
// It only differs in data type from the external rule.
// Everything means the same thing.
type InternalClassRule struct {
	Name          string
	Priority      int
	SourceAs      matchRule
	DestinationAs matchRule
	L4Type        []ProtocolMatchType
	QueueNumber   int
}

type matchRule struct {
	IA        addr.IA
	lowLim    addr.IA // Only set if matchMode is Range
	upLim     addr.IA // Only set if matchMode is Range
	intf      uint64
	matchMode matchMode
}

type matchMode int

const (
	// EXACT match the exact ISD and AS
	EXACT matchMode = 0
	// ISDONLY match the ISD only
	ISDONLY matchMode = 1
	// ASONLY match the AS only
	ASONLY matchMode = 2
	// RANGE match AS and ISD in this range
	RANGE matchMode = 3
	// ANY match anything
	ANY matchMode = 4
	// INTF match interface
	INTF matchMode = 5
)

// RegularClassRule implements ClassRuleInterface
type RegularClassRule struct {
	maskMatched, maskSad, maskDas, maskLf, maskIntf []bool
	extensions                                      []common.ExtnType
}

var _ ClassRuleInterface = (*RegularClassRule)(nil)

// ConvClassRuleToInternal converts the rules loaded from the config file to rules
// used by the QoS subsystem
func ConvClassRuleToInternal(cr conf.ExternalClassRule) (InternalClassRule, error) {

	sourceMatch, err := getMatchRuleTypeFromRule(cr, cr.SourceMatchMode, cr.SourceAs)
	if err != nil {
		return InternalClassRule{}, err
	}
	destinationMatch, err := getMatchRuleTypeFromRule(
		cr,
		cr.DestinationMatchMode,
		cr.DestinationAs)

	if err != nil {
		return InternalClassRule{}, err
	}

	l4t := make([]ProtocolMatchType, 0)

	for _, l4pt := range cr.L4Type {
		l4t = append(l4t, ProtocolMatchType{
			baseProtocol: common.L4ProtocolType(l4pt.BaseProtocol),
			extension:    l4pt.Extension})
	}

	rule := InternalClassRule{
		Name:          cr.Name,
		Priority:      cr.Priority,
		SourceAs:      sourceMatch,
		DestinationAs: destinationMatch,
		L4Type:        l4t,
		QueueNumber:   cr.QueueNumber}

	return rule, nil
}

// RulesToMap converts a list of internal class rules
// (converted by ConvClassRuleToInternal) to a struct of maps of rules
// which can be used to match packets
func RulesToMap(crs []InternalClassRule) *MapRules {
	sourceRules := make(map[addr.IA][]*InternalClassRule)
	destinationRules := make(map[addr.IA][]*InternalClassRule)

	asOnlySourceRules := make(map[addr.AS][]*InternalClassRule)
	asOnlyDestRules := make(map[addr.AS][]*InternalClassRule)
	isdOnlySourceRules := make(map[addr.ISD][]*InternalClassRule)
	isdOnlyDestRules := make(map[addr.ISD][]*InternalClassRule)
	sourceAnyDestinationRules := make(map[addr.IA][]*InternalClassRule)
	destinationAnySourceRules := make(map[addr.IA][]*InternalClassRule)
	interfaceIncomingRules := make(map[uint64][]*InternalClassRule)

	l4OnlyRules := make([]*InternalClassRule, 0)

	for k, cr := range crs {

		switch cr.SourceAs.matchMode {
		case EXACT:
			sourceRules[cr.SourceAs.IA] = append(sourceRules[cr.SourceAs.IA], &crs[k])
		case RANGE:
			lowLimI := uint16(cr.SourceAs.lowLim.I)
			upLimI := uint16(cr.SourceAs.upLim.I)
			lowLimA := uint64(cr.SourceAs.lowLim.A)
			upLimA := uint64(cr.SourceAs.upLim.A)

			for i := lowLimI; i <= upLimI; i++ {
				for j := lowLimA; j <= upLimA; j++ {
					address := addr.IA{I: addr.ISD(i), A: addr.AS(j)}
					sourceRules[address] = append(
						sourceRules[addr.IA{
							I: addr.ISD(i),
							A: addr.AS(j)}],
						&crs[k])
				}
			}
		case ASONLY:
			address := cr.SourceAs.IA.A
			asOnlySourceRules[address] = append(
				asOnlySourceRules[address],
				&crs[k])
		case ISDONLY:
			address := cr.SourceAs.IA.I
			isdOnlySourceRules[address] = append(
				isdOnlySourceRules[address],
				&crs[k])
		case ANY:
			if cr.DestinationAs.matchMode != ANY {
				address := cr.DestinationAs.IA
				destinationAnySourceRules[address] = append(
					destinationAnySourceRules[address],
					&crs[k])
			} else {
				l4OnlyRules = append(l4OnlyRules, &crs[k])
			}
		case INTF:
			interfaceIncomingRules[cr.SourceAs.intf] = append(
				interfaceIncomingRules[cr.SourceAs.intf],
				&crs[k])
		}

		switch cr.DestinationAs.matchMode {
		case EXACT:
			address := cr.DestinationAs.IA
			destinationRules[address] = append(
				destinationRules[address],
				&crs[k])
		case RANGE:
			lowLimI := uint16(cr.DestinationAs.lowLim.I)
			upLimI := uint16(cr.DestinationAs.upLim.I)
			lowLimA := uint64(cr.DestinationAs.lowLim.A)
			upLimA := uint64(cr.DestinationAs.upLim.A)

			for i := lowLimI; i <= upLimI; i++ {
				for j := lowLimA; j <= upLimA; j++ {
					address := addr.IA{I: addr.ISD(i), A: addr.AS(j)}
					destinationRules[address] = append(
						destinationRules[addr.IA{
							I: addr.ISD(i),
							A: addr.AS(j)}],
						&crs[k])
				}
			}
		case ASONLY:
			address := cr.DestinationAs.IA.A
			asOnlyDestRules[address] = append(
				asOnlyDestRules[address],
				&crs[k])
		case ISDONLY:
			address := cr.DestinationAs.IA.I
			isdOnlyDestRules[address] = append(
				isdOnlyDestRules[address],
				&crs[k])
		case ANY:
			if cr.SourceAs.matchMode != ANY {
				address := cr.SourceAs.IA
				sourceAnyDestinationRules[address] = append(
					sourceAnyDestinationRules[address],
					&crs[k])
			}
			// else case is handled while checking the source match mode
		case INTF:
			// This case is not handeled
		}
	}

	mp := MapRules{
		RulesList:                 crs,
		SourceRules:               sourceRules,
		DestinationRules:          destinationRules,
		SourceAnyDestinationRules: sourceAnyDestinationRules,
		DestinationAnySourceRules: destinationAnySourceRules,
		ASOnlySourceRules:         asOnlySourceRules,
		ASOnlyDestRules:           asOnlyDestRules,
		ISDOnlySourceRules:        isdOnlySourceRules,
		ISDOnlyDestRules:          isdOnlyDestRules,
		L4OnlyRules:               l4OnlyRules,
		InterfaceIncomingRules:    interfaceIncomingRules,
	}

	return &mp
}

func getMatchRuleTypeFromRule(
	cr conf.ExternalClassRule, matchModeField int, matchRuleField string) (matchRule, error) {
	switch matchMode(matchModeField) {
	case EXACT, ASONLY, ISDONLY, ANY:
		IA, err := addr.IAFromString(matchRuleField)
		if err != nil {
			return matchRule{}, err
		}
		m := matchRule{
			IA:        IA,
			lowLim:    addr.IA{},
			upLim:     addr.IA{},
			matchMode: matchMode(matchModeField)}
		return m, nil
	case RANGE:
		if matchMode(matchModeField) == RANGE {
			parts := strings.Split(matchRuleField, "||")
			if len(parts) != 2 {
				return matchRule{}, common.NewBasicError(
					"Invalid Class",
					nil,
					"raw",
					matchModeField)
			}
			lowLim, err := addr.IAFromString(parts[0])
			if err != nil {
				return matchRule{}, err
			}
			upLim, err := addr.IAFromString(parts[1])
			if err != nil {
				return matchRule{}, err
			}
			m := matchRule{
				IA:        addr.IA{},
				lowLim:    lowLim,
				upLim:     upLim,
				matchMode: matchMode(matchModeField)}
			return m, nil
		}
	case INTF:
		intf, _ := strconv.ParseInt(matchRuleField, 0, 64)
		m := matchRule{
			IA:        addr.IA{},
			intf:      uint64(intf),
			lowLim:    addr.IA{},
			upLim:     addr.IA{},
			matchMode: matchMode(matchModeField)}
		return m, nil

	}

	return matchRule{}, common.NewBasicError(
		"Invalid matchMode declared",
		nil,
		"matchMode",
		matchModeField)
}

var emptyRule = &InternalClassRule{
	Name:        "default",
	Priority:    0,
	QueueNumber: 0,
}

func (rc *RegularClassRule) Init(noRules int) {
	rc.extensions = make([]common.ExtnType, 255)

	rc.maskMatched = make([]bool, noRules)
	rc.maskSad = make([]bool, noRules)
	rc.maskDas = make([]bool, noRules)
	rc.maskLf = make([]bool, noRules)
	rc.maskIntf = make([]bool, noRules)
}

// GetRuleForPacket returns the rule for rp
func (rc *RegularClassRule) GetRuleForPacket(
	config *InternalRouterConfig, rp *rpkt.RtrPkt) *InternalClassRule {

	var sources [3][]*InternalClassRule
	var destinations [3][]*InternalClassRule
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

	entry := cacheEntry{srcAddress: srcAddr, dstAddress: dstAddr, intf: intf, l4type: l4t}

	returnRule = config.Rules.CrCache.Get(entry)

	if returnRule != nil {
		if matchRuleL4ExtensionType(returnRule, rc.extensions) {
			return returnRule
		}
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

	config.Rules.CrCache.Put(entry, returnRule)

	return returnRule
}

// matchRuleL4ExtensionType checks whether the rule includes one of the given extension types
// -1 means that any extension type will return true.
func matchRuleL4ExtensionType(rule *InternalClassRule, extensions []common.ExtnType) bool {

	for i := 0; i < len(rule.L4Type); i++ {
		if rule.L4Type[i].extension == -1 {
			return true
		}
		for k := 0; k < len(extensions); k++ {
			if uint8(rule.L4Type[i].extension) == extensions[k].Type &&
				rule.L4Type[i].baseProtocol == extensions[k].Class {
				return true
			}
		}
	}
	return false
}

// matchL4Type returns all rules that match the l4 type and the extension type given.
// It sets mask to true if the match succeeds.
func matchL4Type(
	mask []bool,
	list *[]*InternalClassRule,
	l4t common.L4ProtocolType,
	extensions []common.ExtnType) {

	for i := 0; i < len(mask); i++ {
		mask[i] = false

	}

	for i := 0; i < len(*list); i++ {
		if (*list)[i] == nil {
			break
		}

		for j := 0; j < len((*list)[i].L4Type); j++ {
			if (*list)[i].L4Type[j].baseProtocol == l4t {
				if matchRuleL4ExtensionType((*list)[i], extensions) {
					mask[i] = true
					break
				}
			}
		}
	}
}

// getRuleWithPrevMax returns the rule from list which has a priority higher than the previous
// maximum priority.
// Only rules where mask is true are checked.
func getRuleWithPrevMax(
	returnRule *InternalClassRule,
	mask []bool,
	list []*InternalClassRule,
	prevMax int) (int, *InternalClassRule) {

	if list == nil {
		return prevMax, returnRule
	}

	for i := 0; i < len(list); i++ {
		if mask[i] {
			if list[i].Priority > prevMax {
				returnRule = list[i]
				prevMax = list[i].Priority
			}
		} else {
			break
		}
	}
	return prevMax, returnRule
}

func intersectListsRules(
	a [3][]*InternalClassRule,
	b [3][]*InternalClassRule) []*InternalClassRule {
	var matches = make([]*InternalClassRule, 10)
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
					if a[l] == nil {
						break
					}
					if b[m] == nil {
						break
					}

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
