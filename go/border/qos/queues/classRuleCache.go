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

// ClassRuleCacheInterface defines the interface for a cache of traffic class rules.
// Needs to be thread safe as classification may run in parallel.
// Must call Init before it can be used.
type ClassRuleCacheInterface interface {
	Init(maxEntries int)
	Get(entry cacheEntry) *InternalClassRule
	Put(entry cacheEntry, rule *InternalClassRule)
}

// ClassRuleCache implements ClassRuleCacheInterface
type ClassRuleCache struct {
	cacheMap *sync.Map
}

// Init needs to be called before the cache can be used
func (crCache *ClassRuleCache) Init(maxEntries int) {
	crCache.cacheMap = new(sync.Map)
}

// Get will return the class rule for this entry or nil if entry is not in the cache
func (crCache *ClassRuleCache) Get(entry cacheEntry) *InternalClassRule {
	r, found := crCache.cacheMap.Load(entry)
	if !found {
		return nil
	}
	return r.(*InternalClassRule)
}

// Put adds a new entry to the cache
func (crCache *ClassRuleCache) Put(entry cacheEntry, rule *InternalClassRule) {
	crCache.cacheMap.Store(entry, rule)
}
