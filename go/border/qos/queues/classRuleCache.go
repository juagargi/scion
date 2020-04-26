package queues

import (
	"sync"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/common"
)

type cacheEntry struct {
	srcAddress addr.IA
	dstAddress addr.IA
	l4type     common.L4ProtocolType
	intf       uint64
}

type ClassRuleCacheInterface interface {
	Init(maxEntries int)
	Get(entry cacheEntry) *InternalClassRule
	Put(entry cacheEntry, rule *InternalClassRule)
}

type ClassRuleCache struct {
	cacheMap *sync.Map
}

func (crCache *ClassRuleCache) Init(maxEntries int) {
	crCache.cacheMap = new(sync.Map)
}

func (crCache *ClassRuleCache) Get(entry cacheEntry) *InternalClassRule {
	r, found := crCache.cacheMap.Load(entry)
	if !found {
		return nil
	}
	return r.(*InternalClassRule)
}

func (crCache *ClassRuleCache) Put(entry cacheEntry, rule *InternalClassRule) {
	crCache.cacheMap.Store(entry, rule)
}
