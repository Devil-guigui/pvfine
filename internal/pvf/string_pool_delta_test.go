package pvf

import (
	"context"
	"io"
	"testing"
)

func TestStringPoolDeltaOffsetsAndValidation(t *testing.T) {
	a := New()
	a.StringOffset("original")
	a.UnicodeStringOffset("原有")
	state := a.StringPoolState()
	narrow, wide := a.StringOffset("appended"), a.UnicodeStringOffset("新增")
	if !a.ExtendsStringPools(state) {
		t.Fatal("append invalidated old offsets")
	}
	scan := a.NewAppendedStringPoolScanner(state)
	entries := make(map[int32]string)
	for {
		entry, ok, err := scan.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		entries[entry.Offset] = entry.Value
	}
	if len(entries) != 2 || entries[narrow] != "appended" || entries[wide] != "新增" {
		t.Fatalf("tail scanner=%v", entries)
	}
	a.strA[0] ^= 1
	if a.ExtendsStringPools(state) {
		t.Fatal("pool rewrite retained compatible offsets")
	}
}

func TestSavedContentDigestChangesOnlyWhenPackedBaselineChanges(t *testing.T) {
	a := New()
	index, err := a.AddFileText("file.equ", "[name]\n`original`", TypeScript)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SaveTo(io.Discard); err != nil {
		t.Fatal(err)
	}
	before, clean := a.SavedContentHash()
	if !clean || before == "" {
		t.Fatal("saved baseline has no digest")
	}
	clone := a.CloneForBatch()
	if digest, clean := clone.SavedContentHash(); !clean || digest != before {
		t.Fatal("clone lost the immutable baseline digest")
	}
	if err := clone.SetText(index, "[name]\n`changed`"); err != nil {
		t.Fatal(err)
	}
	if _, clean := clone.SavedContentHash(); clean {
		t.Fatal("dirty archive exposed a clean digest")
	}
	if err := clone.SaveTo(io.Discard); err != nil {
		t.Fatal(err)
	}
	if digest, clean := clone.SavedContentHash(); !clean || digest == before {
		t.Fatal("save reused the old digest")
	}
	if digest, clean := a.SavedContentHash(); !clean || digest != before {
		t.Fatal("saving clone invalidated the original baseline")
	}
}

func TestStringTableCacheTracksOrdinaryEditsAndLateFiles(t *testing.T) {
	a := New()
	mapIndex, err := a.AddFileText("list/n_string.lst", "0\t`string/names.str`", TypeScript)
	if err != nil {
		t.Fatal(err)
	}
	tableIndex, err := a.AddFileText("string/names.str", "key>old\n", TypeUnicode)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := a.LookupStringTable(0, "key"); !ok || value != "old" {
		t.Fatalf("initial=%q %v", value, ok)
	}
	if err := a.SetText(tableIndex, "key>updated\n"); err != nil {
		t.Fatal(err)
	}
	if value, ok := a.LookupStringTable(0, "key"); !ok || value != "updated" {
		t.Fatalf("ordinary edit=%q %v", value, ok)
	}
	if _, err := a.RemoveFiles([]int32{tableIndex}); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.LookupStringTable(0, "key"); ok {
		t.Fatal("removed table remained cached")
	}
	if _, err := a.AddFileText("string/names.str", "key>restored\n", TypeUnicode); err != nil {
		t.Fatal(err)
	}
	if value, ok := a.LookupStringTable(0, "key"); !ok || value != "restored" {
		t.Fatalf("late table=%q %v", value, ok)
	}
	if _, err := a.AddFileText("string/other.str", "key>remapped\n", TypeUnicode); err != nil {
		t.Fatal(err)
	}
	if err := a.SetText(mapIndex, "0\t`string/other.str`"); err != nil {
		t.Fatal(err)
	}
	if value, ok := a.LookupStringTable(0, "key"); !ok || value != "remapped" {
		t.Fatalf("mapping edit=%q %v", value, ok)
	}
}
