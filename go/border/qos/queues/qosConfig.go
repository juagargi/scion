package queues

import (
	"github.com/scionproto/scion/go/lib/addr"
)

// InternalRouterConfig is what I am loading from the config file
type InternalRouterConfig struct {
	Scheduler SchedulerConfig
	Queues    []PacketQueueInterface
	Rules     MapRules
}

type SchedulerConfig struct {
	Latency   int
	Bandwidth int
}

type MapRules struct {
	RulesList                 []InternalClassRule
	CrCache                   ClassRuleCache
	SourceRules               map[addr.IA][]*InternalClassRule
	DestinationRules          map[addr.IA][]*InternalClassRule
	SourceAnyDestinationRules map[addr.IA][]*InternalClassRule
	DestinationAnySourceRules map[addr.IA][]*InternalClassRule
	ASOnlySourceRules         map[addr.AS][]*InternalClassRule
	ASOnlyDestRules           map[addr.AS][]*InternalClassRule
	ISDOnlySourceRules        map[addr.ISD][]*InternalClassRule
	ISDOnlyDestRules          map[addr.ISD][]*InternalClassRule
	L4OnlyRules               []*InternalClassRule
	InterfaceIncomingRules    map[uint64][]*InternalClassRule
}
