package pvf

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"testing"
)

func workspaceReferenceInput() StringReferenceInput {
	tokens := []struct {
		typ    byte
		offset int32
	}{{3, 20}, {5, 30}, {7, 30}, {5, 30}, {10, -7}, {6, 20}, {8, 20}, {0, 111}}
	raw := make([]byte, len(tokens)*5)
	for i, token := range tokens {
		raw[i*5] = token.typ
		binary.LittleEndian.PutUint32(raw[i*5+1:], uint32(token.offset))
	}
	// An incomplete trailing token is ignored by the existing parser.
	raw = append(raw, 7, 1)
	return StringReferenceInput{raw: raw, nameOff: 20, pathOff: 20}
}

func TestStringReferenceWorkspaceReusesWithoutStaleReferences(t *testing.T) {
	input := workspaceReferenceInput()
	want := []StringReference{
		{Offset: -7, Occurrences: 1, TokenTypes: []int32{10}},
		{Offset: 20, Occurrences: 5, TokenTypes: []int32{3, 6, 8}, FileFields: []string{"name", "path"}},
		{Offset: 30, Occurrences: 3, TokenTypes: []int32{5, 7}},
	}
	var workspace StringReferenceWorkspace
	refs, err := workspace.References(context.Background(), input)
	if err != nil || !reflect.DeepEqual(refs, want) {
		t.Fatalf("unexpected references: %v, err=%v", refs, err)
	}
	storage := &refs[0]
	workspace.Reset()
	if len(workspace.refs) != 0 || len(workspace.byOffset) != 0 || cap(workspace.refs) == 0 {
		t.Fatal("reset did not clear logical state and retain small storage")
	}
	refs, err = workspace.References(context.Background(), StringReferenceInput{nameOff: 30, pathOff: -7})
	want = []StringReference{
		{Offset: -7, Occurrences: 1, TokenTypes: []int32{}, FileFields: []string{"path"}},
		{Offset: 30, Occurrences: 1, TokenTypes: []int32{}, FileFields: []string{"name"}},
	}
	// Empty slices may reuse prior capacity; only their contents are semantic.
	if err != nil || len(refs) != len(want) {
		t.Fatalf("second parse failed: refs=%d err=%v", len(refs), err)
	}
	for i, ref := range refs {
		if ref.Offset != want[i].Offset || ref.Occurrences != want[i].Occurrences || len(ref.TokenTypes) != 0 || !reflect.DeepEqual(ref.FileFields, want[i].FileFields) {
			t.Fatal("second parse retained stale references, token types or fields")
		}
	}
	if storage != &refs[0] {
		t.Fatal("small result storage was not reused")
	}
	// The original owned-result API must remain independent of workspace reuse
	// and later calls, including nested slices.
	owned, err := input.References(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	other, err := input.References(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	other[0].TokenTypes[0] = 99
	other[1].FileFields[0] = "changed"
	if owned[0].TokenTypes[0] != 10 || owned[1].FileFields[0] != "name" {
		t.Fatal("owned results share nested storage")
	}
}

func largeWorkspaceReferenceInput() StringReferenceInput {
	raw := make([]byte, (maxRetainedStringReferences+10)*5)
	for i := 0; i < len(raw)/5; i++ {
		raw[i*5] = 7
		binary.LittleEndian.PutUint32(raw[i*5+1:], uint32(i))
	}
	return StringReferenceInput{raw: raw, nameOff: -1, pathOff: -2}
}

func TestStringReferenceWorkspaceDiscardsOversizedStorage(t *testing.T) {
	var workspace StringReferenceWorkspace
	refs, err := workspace.References(context.Background(), largeWorkspaceReferenceInput())
	if err != nil || len(refs) != maxRetainedStringReferences+12 {
		t.Fatalf("large file was truncated: refs=%d err=%v", len(refs), err)
	}
	workspace.Reset()
	if workspace.refs != nil || workspace.byOffset != nil {
		t.Fatal("oversized workspace survived reset")
	}
	refs, err = workspace.References(context.Background(), StringReferenceInput{nameOff: 1, pathOff: 2})
	if err != nil || len(refs) != 2 || cap(workspace.refs) > maxRetainedStringReferences {
		t.Fatal("small parse did not recover after oversized result")
	}
}

type workspaceCancelContext struct {
	context.Context
	checks int
}

func (ctx *workspaceCancelContext) Err() error {
	ctx.checks++
	if ctx.checks >= 6 {
		return context.Canceled
	}
	return nil
}

func TestStringReferenceWorkspaceCancellationReleasesPartialResult(t *testing.T) {
	var workspace StringReferenceWorkspace
	ctx := &workspaceCancelContext{Context: context.Background()}
	refs, err := workspace.References(ctx, largeWorkspaceReferenceInput())
	if !errors.Is(err, context.Canceled) || refs != nil {
		t.Fatalf("partial parse was published: refs=%d err=%v", len(refs), err)
	}
	if workspace.refs != nil || workspace.byOffset != nil {
		t.Fatal("canceled parse retained oversized storage")
	}
	refs, err = workspace.References(context.Background(), workspaceReferenceInput())
	if err != nil || len(refs) != 3 {
		t.Fatal("workspace could not recover after cancellation")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	refs, err = workspace.References(canceled, workspaceReferenceInput())
	if !errors.Is(err, context.Canceled) || refs != nil || len(workspace.refs) != 0 || len(workspace.byOffset) != 0 {
		t.Fatal("already canceled parse retained previous logical state")
	}
}

// Opt-in full-file comparison against the independently owned parser. It logs
// only counts and never archive paths, string-pool values or payload bytes.
func TestStringReferenceWorkspaceRealArchive(t *testing.T) {
	if os.Getenv("PVF_REFERENCE_TEST") != "1" {
		t.Skip("PVF_REFERENCE_TEST=1 required")
	}
	path := os.Getenv("PVF_TESTFILE")
	if path == "" {
		t.Fatal("PVF_TESTFILE required")
	}
	a, err := Open(path)
	if err != nil {
		t.Fatal("cannot open reference test archive")
	}
	ctx := context.Background()
	scanner := a.NewStringReferenceScanner()
	var workspace StringReferenceWorkspace
	files, count := 0, 0
	for {
		index, input, ok, err := scanner.NextInput(ctx)
		if err != nil {
			t.Fatal("cannot read reference test input")
		}
		if !ok {
			break
		}
		want, err := input.References(ctx)
		if err != nil {
			t.Fatalf("owned parser failed for file %d", index)
		}
		refs, err := workspace.References(ctx, input)
		if err != nil || !equalWorkspaceReferences(refs, want) {
			t.Fatalf("reference mismatch for file %d", index)
		}
		files++
		count += len(refs)
		workspace.Reset()
	}
	if files != int(a.FileCount()) {
		t.Fatalf("missing files: got=%d want=%d", files, a.FileCount())
	}
	t.Logf("files=%d references=%d", files, count)
}

func equalWorkspaceReferences(left, right []StringReference) bool {
	if len(left) != len(right) {
		return false
	}
	for i, ref := range left {
		other := right[i]
		if ref.Offset != other.Offset || ref.Occurrences != other.Occurrences || len(ref.TokenTypes) != len(other.TokenTypes) || len(ref.FileFields) != len(other.FileFields) {
			return false
		}
		for j, typ := range ref.TokenTypes {
			if typ != other.TokenTypes[j] {
				return false
			}
		}
		for j, field := range ref.FileFields {
			if field != other.FileFields[j] {
				return false
			}
		}
	}
	return true
}
