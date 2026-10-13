package pvf

import "testing"

func TestFileIdentitySurvivesClonesCompactionAndDuplicatePaths(t *testing.T) {
	a := New()
	a.AddFile("first.str", nil, TypeUnicode)
	a.AddFile("second.str", nil, TypeUnicode)
	// Real archives may contain duplicate paths; identity must not depend on
	// the path lookup map or the compact table index.
	a.items[1].nameOff = a.items[0].nameOff
	first, second := a.FileIdentity(0), a.FileIdentity(1)
	if first == 0 || second == 0 || first == second {
		t.Fatal("entries do not have distinct identities")
	}
	clone := a.CloneForBatch()
	if _, err := clone.RemoveFiles([]int32{0}); err != nil {
		t.Fatal(err)
	}
	if clone.FileIdentity(0) != second || a.FileIdentity(0) != first || a.FileIdentity(1) != second {
		t.Fatal("clone/compaction changed surviving identities")
	}
	clone.AddFile("third.str", nil, TypeUnicode)
	if clone.FileIdentity(1) == first || clone.FileIdentity(1) == second {
		t.Fatal("new entry reused an identity")
	}
}
