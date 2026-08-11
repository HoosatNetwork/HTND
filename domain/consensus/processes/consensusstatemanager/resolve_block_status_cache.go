package consensusstatemanager

// ResolveBlockStatusCacheLen returns the number of entries in the ResolveBlockStatus cache
func (csm *consensusStateManager) ResolveBlockStatusCacheLen() int {
	return csm.resolveBlockStatusCache.Len()
}

// ClearResolveBlockStatusCache clears all entries from the ResolveBlockStatus cache
func (csm *consensusStateManager) ClearResolveBlockStatusCache() {
	csm.resolveBlockStatusCache.Clear()
}