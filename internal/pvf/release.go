package pvf

// Release drops the large in-memory buffers and caches owned by an archive.
// Callers must ensure no archive operation is still running before invoking
// it. The archive object itself remains valid only as an empty shell after
// this call.
func (a *Archive) Release() int64 {
	if a == nil {
		return 0
	}

	a.cacheMu.Lock()
	released := int64(len(a.data) + len(a.pageKeys) + len(a.items)*24 + len(a.groups)*8)
	released += int64(len(a.strA) + len(a.strW))
	released += int64(len(a.compactPaths) * 8)
	if a.act60 != nil {
		released += int64(len(a.act60.entries)*80 + len(a.act60.directory))
	}
	if a.act60 != nil {
		for _, e := range a.act60.entries {
			released += int64(len(e.tokens))
		}
	}
	for _, source := range a.act60Sources {
		released += int64(len(source))
	}
	if a.tw != nil {
		released += int64(len(a.tw.entries)*48 + len(a.tw.strings)*4 + len(a.tw.stringTable) + len(a.tw.directory))
	}
	for _, value := range a.chunkCache {
		released += int64(len(value))
	}
	for _, value := range a.overlay {
		released += int64(len(value))
	}
	a.act60Sources = nil
	a.act60 = nil
	a.tw = nil
	a.data = nil
	a.contentDigest = ""
	a.pageKeys = nil
	a.items = nil
	a.groups = nil
	a.strA = nil
	a.strW = nil
	a.strAIdx = nil
	a.strWIdx = nil
	a.resolveCache = nil
	a.resolveCacheOrder = nil
	a.resolveCacheBytes = 0
	a.chunkCache = nil
	a.chunkCacheMeta = nil
	a.chunkCacheBytes = 0
	a.chunkCacheClock = 0
	a.overlay = nil
	a.pathIndex = nil
	a.compactPaths = nil
	a.removedSpans = nil
	a.sourcePath = ""
	a.cacheMu.Unlock()

	a.tables.mu.Lock()
	a.tables.state = nil
	a.tables.stats = StringTableIndexStats{}
	a.tables.observer = nil
	a.tables.mu.Unlock()

	a.scriptRenderer = nil
	a.canonicalScriptRenderer = nil
	a.hdr = Header{}
	a.keys = keySet{}
	a.format = formatProfile{}
	a.tableOff = 0
	a.hashOff = 0
	a.nameOff = 0
	a.grpiOff = 0
	a.bodyOff = 0
	a.hashSize = 0
	a.nameSize = 0
	a.grpiSize = 0
	return released
}
