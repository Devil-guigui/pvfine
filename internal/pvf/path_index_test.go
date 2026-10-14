package pvf

import (
	"slices"
	"testing"
)

func TestCompactPathIndexCollisionsAndMutations(t *testing.T) {
	a := New()
	a.AddFile("etc/other.txt", []byte("other"), TypeUnicode)
	a.AddFile("etc/target.txt", []byte("target"), TypeUnicode)
	// A collision candidate precedes the real target. Repeated entries must
	// resolve to the first file, just like the original string-keyed map.
	a.items = append(a.items, a.items[1])
	hash := uint64(pathHash("etc/target.txt")) << 32
	a.compactPaths = []uint64{hash, hash | 1, hash | 2}
	a.pathIndex = map[string]int32{}
	if i, ok := a.Find(" ./ETC\\TARGET.TXT/ "); !ok || i != 1 {
		t.Fatalf("collision/normalized lookup = %d, %t", i, ok)
	}
	if _, ok := a.Find("etc/missing.txt"); ok {
		t.Fatal("missing path matched")
	}
	clone := a.CloneForBatch()
	if i := clone.AddFile("etc/target.txt", []byte("replacement"), TypeUnicode); i != 1 || clone.FileCount() != 3 {
		t.Fatal("replacement appended a duplicate")
	}
	clone.AddFile("etc/new.txt", []byte("new"), TypeUnicode)
	if _, ok := clone.Find("ETC/NEW.TXT"); !ok {
		t.Fatal("added path missing")
	}
	if _, ok := a.Find("etc/new.txt"); ok {
		t.Fatal("clone changed original path index")
	}
	if _, err := clone.RemoveFiles([]int32{0, 1}); err != nil {
		t.Fatal(err)
	}
	if i, ok := clone.Find("etc/target.txt"); !ok || i != 0 {
		t.Fatal("removal lost remaining duplicate or failed to renumber")
	}
	if i, ok := a.Find("etc/target.txt"); !ok || i != 1 {
		t.Fatal("clone removal changed original")
	}
	a.Release()
	if a.compactPaths != nil {
		t.Fatal("release retained compact paths")
	}
}

func TestLargePathIndexUsesCompactStorage(t *testing.T) {
	a := New()
	a.AddFile("etc/first.txt", nil, TypeUnicode)
	a.AddFile("etc/last.txt", nil, TypeUnicode)
	first, last := a.items[0], a.items[1]
	a.items = make([]fileItem, compactPathIndexThreshold)
	for i := range a.items {
		a.items[i] = first
	}
	a.items[len(a.items)-1] = last
	a.rebuildPathIndex()
	if len(a.pathIndex) != 0 || len(a.compactPaths) != len(a.items) || !slices.IsSorted(a.compactPaths) {
		t.Fatal("large index retained path map or did not sort compact entries")
	}
	if i, ok := a.Find("etc/first.txt"); !ok || i != 0 {
		t.Fatal("duplicate lookup did not preserve first entry")
	}
	if i, ok := a.Find("etc/last.txt"); !ok || i != int32(len(a.items)-1) {
		t.Fatal("last path missing")
	}
	if _, err := a.RemoveFiles([]int32{0}); err != nil {
		t.Fatal(err)
	}
	if a.compactPaths != nil {
		t.Fatal("small rebuilt index did not return to map storage")
	}
	if i, ok := a.Find("etc/last.txt"); !ok || i != int32(len(a.items)-1) {
		t.Fatal("threshold transition failed to renumber")
	}
}
