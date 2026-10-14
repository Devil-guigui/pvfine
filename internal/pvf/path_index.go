package pvf

import (
	"hash/fnv"
	"slices"
	"sort"
)

const compactPathIndexThreshold = 100000

func pathHash(path string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(path))
	return h.Sum32()
}

// Each immutable entry packs a path fingerprint and file index into eight
// bytes. Hash collisions are checked against the original string pools.
// Newly added paths live in pathIndex until the next rebuild.
func (a *Archive) rebuildPathIndex() {
	a.pathIndex = make(map[string]int32)
	a.compactPaths = nil
	if len(a.items) >= compactPathIndexThreshold {
		a.compactPaths = make([]uint64, len(a.items))
		for i := range a.items {
			path := normalizePath(a.Path(int32(i)))
			a.compactPaths[i] = uint64(pathHash(path))<<32 | uint64(uint32(i))
		}
		slices.Sort(a.compactPaths)
		return
	}
	for i := range a.items {
		path := normalizePath(a.Path(int32(i)))
		if _, exists := a.pathIndex[path]; !exists {
			a.pathIndex[path] = int32(i)
		}
	}
}

func (a *Archive) findNormalizedPath(path string) (int32, bool) {
	if i, ok := a.pathIndex[path]; ok {
		return i, true
	}
	hash := uint64(pathHash(path)) << 32
	start := sort.Search(len(a.compactPaths), func(i int) bool { return a.compactPaths[i] >= hash })
	for _, entry := range a.compactPaths[start:] {
		if entry>>32 != hash>>32 {
			break
		}
		index := int32(uint32(entry))
		if normalizePath(a.Path(index)) == path {
			return index, true
		}
	}
	return 0, false
}
