package services

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"pvfine/internal/pvf"
	pvfversion "pvfine/internal/version"
)

func addDiffText(t *testing.T, a *pvf.Archive, path, text string) int32 {
	t.Helper()
	i, err := a.AddFileText(path, text, pvf.TypeScript)
	if err != nil {
		t.Fatal(err)
	}
	return i
}
func waitDiff(t *testing.T, s *ArchiveDiffService, id string) *ArchiveDiffStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := s.Status(id)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == "ready" || status.State == "error" || status.State == "cancelled" {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("comparison timed out")
	return nil
}
func diffFixture(t *testing.T) (*core, *ArchiveDiffService, string) {
	t.Helper()
	other := pvf.New()
	addDiffText(t, other, "equipment/same.equ", "[name]\n`same`")
	addDiffText(t, other, "equipment/changed.equ", "[name]\n`old`")
	addDiffText(t, other, "equipment/deleted.equ", "[name]\n`deleted`")
	path := filepath.Join(t.TempDir(), "other.pvf")
	if err := other.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	c := NewCore()
	if err := c.setArchive(mustReopen(t, path)); err != nil {
		t.Fatal(err)
	}
	s := NewArchiveDiffService(c)
	t.Cleanup(c.closeArchive)
	return c, s, path
}
func TestArchiveDiffWorkspaceSnapshot(t *testing.T) {
	c, s, path := diffFixture(t)
	a := c.archive
	changed, _ := a.Find("equipment/changed.equ")
	deleted, _ := a.Find("equipment/deleted.equ")
	if err := a.SetText(changed, "[name]\n`backend edit`"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RemoveFiles([]int32{deleted}); err != nil {
		t.Fatal(err)
	}
	addDiffText(t, a, "equipment/added.equ", "[name]\n`added`")
	changed, _ = a.Find("equipment/changed.equ")
	original, err := a.CanonicalText(changed)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := a.MutationCheckpoint()
	revision := c.batchRevision
	indexGen := c.indexGen
	generation := c.archiveGeneration
	c.editorText[changed] = "retained editor overlay"
	p, err := s.Start(ArchiveDiffRequest{Path: path, Drafts: []ArchiveDiffDraft{{FileIndex: changed, Path: "equipment/changed.equ", Text: "[name]\n`frontend draft`"}}})
	if err != nil {
		t.Fatal(err)
	}
	status := waitDiff(t, s, p.ID)
	if status.State != "ready" || status.Added != 1 || status.Modified != 1 || status.Deleted != 1 {
		t.Fatalf("status=%+v", status)
	}
	file, err := s.FileDiff(p.ID, "EQUIPMENT\\CHANGED.EQU")
	if err != nil {
		t.Fatal(err)
	}
	if !file.TextAvailable || !strings.Contains(file.BeforeText, "old") || !strings.Contains(file.AfterText, "frontend draft") {
		t.Fatalf("diff=%+v", file)
	}
	if now, _ := a.CanonicalText(changed); now != original || c.archive != a || a.MutationCheckpoint() != checkpoint || c.batchRevision != revision || c.indexGen != indexGen || c.archiveGeneration != generation || c.editorText[changed] != "retained editor overlay" {
		t.Fatal("comparison mutated workspace")
	}
	s.mu.Lock()
	other := s.session.other
	s.mu.Unlock()
	if other.StringTableIndexStats().MappingBuilds != 0 || other.StringTableIndexStats().TableBuilds != 0 {
		t.Fatal("comparison built string tables")
	}
	roots, err := s.Children(p.ID, "")
	if err != nil || len(roots) != 1 || roots[0].Count != 3 {
		t.Fatalf("roots=%+v error=%v", roots, err)
	}
	children, err := s.Children(p.ID, "EQUIPMENT")
	if err != nil || len(children) != 3 {
		t.Fatalf("children=%+v error=%v", children, err)
	}
	// Existing results are immutable; refresh reads the new backend payload.
	if err := a.SetText(changed, "[name]\n`refreshed`"); err != nil {
		t.Fatal(err)
	}
	file, _ = s.FileDiff(p.ID, "equipment/changed.equ")
	if !strings.Contains(file.AfterText, "frontend draft") {
		t.Fatal("snapshot changed")
	}
	fresh, err := s.Start(ArchiveDiffRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if waitDiff(t, s, fresh.ID).State != "ready" {
		t.Fatal("refresh failed")
	}
	if _, err := s.Status(p.ID); err == nil {
		t.Fatal("old session retained")
	}
	file, _ = s.FileDiff(fresh.ID, "equipment/changed.equ")
	if !strings.Contains(file.AfterText, "refreshed") {
		t.Fatal("refresh missed latest content")
	}
}
func TestArchiveDiffLogicalComparison(t *testing.T) {
	old, new := pvf.New(), pvf.New()
	addDiffText(t, old, "Item.equ", "[name]\n`equal`")
	addDiffText(t, new, "unrelated.equ", "[name]\n`different pool layout`")
	i := addDiffText(t, new, "item.equ", "[name]\n`equal`")
	if _, err := new.RemoveFiles([]int32{0}); err != nil {
		t.Fatal(err)
	}
	_ = i
	before, err := archiveDiffSnapshot(context.Background(), old, 0, func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	after, err := archiveDiffSnapshot(context.Background(), new, 0, func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	if changes := pvfversion.Diff(before, after); len(changes) != 0 {
		t.Fatalf("false changes=%+v", changes)
	}
	old.AddFile("empty.bin", nil, 99)
	new.AddFile("empty.bin", nil, 98)
	before, _ = archiveDiffSnapshot(context.Background(), old, 0, func(int) {})
	after, _ = archiveDiffSnapshot(context.Background(), new, 0, func(int) {})
	changes := pvfversion.Diff(before, after)
	if len(changes) != 1 || changes[0].Operation != pvfversion.OperationModify {
		t.Fatalf("type change=%+v", changes)
	}
}
func TestArchiveDiffErrorsAndRelease(t *testing.T) {
	c, s, path := diffFixture(t)
	bad := filepath.Join(t.TempDir(), "bad.pvf")
	if err := os.WriteFile(bad, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, request := range []ArchiveDiffRequest{{Path: bad}, {Path: path, Drafts: []ArchiveDiffDraft{{FileIndex: 999, Path: "missing", Text: "invalid"}}}} {
		p, err := s.Start(request)
		if err != nil {
			t.Fatal(err)
		}
		if status := waitDiff(t, s, p.ID); status.State != "error" || status.Error == "" {
			t.Fatalf("status=%+v", status)
		}
	}
	p, err := s.Start(ArchiveDiffRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if status := waitDiff(t, s, p.ID); status.State != "ready" || status.Added+status.Modified+status.Deleted != 0 {
		t.Fatalf("equal status=%+v", status)
	}
	s.Cancel(p.ID)
	status, _ := s.Status(p.ID)
	if status.State != "cancelled" {
		t.Fatal("not cancelled")
	}
	s.mu.Lock()
	retained := s.session.current != nil || s.session.other != nil || s.session.children != nil
	s.mu.Unlock()
	if retained {
		t.Fatal("cancel retains archives")
	}
	fresh, _ := s.Start(ArchiveDiffRequest{Path: path})
	waitDiff(t, s, fresh.ID)
	c.closeArchive()
	if _, err := s.Status(fresh.ID); err == nil {
		t.Fatal("workspace close retains session")
	}
}
func TestArchiveDiffRejectsDuplicatePathsAndCancellation(t *testing.T) {
	a := pvf.New()
	a.AddFile("A.bin", []byte{1}, 99)
	a.AddFile("b.bin", []byte{2}, 99)
	var packed bytes.Buffer
	if err := a.SaveTo(&packed); err != nil {
		t.Fatal(err)
	}
	data := packed.Bytes()
	// File-table records are plaintext: make the second entry reference the first path.
	copy(data[0x30+0x18:0x30+0x18+8], data[0x30:0x30+8])
	var parseErr error
	a, parseErr = pvf.Parse(data)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if _, err := archiveDiffSnapshot(context.Background(), a, 0, func(int) {}); err == nil || !strings.Contains(err.Error(), "重复路径") {
		t.Fatalf("duplicate error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := archiveDiffSnapshot(ctx, a, 0, func(int) {}); err != context.Canceled {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestArchiveDiffBinaryEmptyAndLargeDetails(t *testing.T) {
	c, s, path := diffFixture(t)
	c.archive.AddFile("custom.bin", []byte{1, 2, 3}, 99)
	addDiffText(t, c.archive, "empty.equ", "")
	// Unicode data stays cheap to synthesize and exceeds the display threshold.
	c.archive.AddFile("large.str", make([]byte, int(c.editableByteLimit())+2), pvf.TypeUnicode)
	p, err := s.Start(ArchiveDiffRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if status := waitDiff(t, s, p.ID); status.State != "ready" || status.Added != 3 {
		t.Fatalf("status=%+v", status)
	}
	binary, err := s.FileDiff(p.ID, "custom.bin")
	if err != nil {
		t.Fatal(err)
	}
	if binary.TextAvailable || binary.Message == "" || binary.BeforeExists || !binary.AfterExists || binary.AfterSize != 3 {
		t.Fatalf("binary=%+v", binary)
	}
	empty, err := s.FileDiff(p.ID, "empty.equ")
	if err != nil {
		t.Fatal(err)
	}
	if !empty.TextAvailable || empty.AfterText != "" || empty.Operation != "add" {
		t.Fatalf("empty=%+v", empty)
	}
	large, err := s.FileDiff(p.ID, "large.str")
	if err != nil {
		t.Fatal(err)
	}
	if large.TextAvailable || !strings.Contains(large.Message, "阈值") {
		t.Fatalf("large=%+v", large)
	}
}

func TestArchiveDiffConcurrentRestartAndCancel(t *testing.T) {
	_, s, path := diffFixture(t)
	var workers sync.WaitGroup
	errors := make(chan error, 12)
	for worker := 0; worker < 3; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for attempt := 0; attempt < 4; attempt++ {
				p, err := s.Start(ArchiveDiffRequest{Path: path})
				if err != nil {
					errors <- err
					continue
				}
				_, _ = s.Status(p.ID)
				_, _ = s.Children(p.ID, "")
				_, _ = s.FileDiff(p.ID, "equipment/changed.equ")
				s.Cancel(p.ID)
			}
		}()
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	final, err := s.Start(ArchiveDiffRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if status := waitDiff(t, s, final.ID); status.State != "ready" {
		t.Fatalf("final=%+v", status)
	}
	s.Release()
	if _, err := s.Status(final.ID); err == nil {
		t.Fatal("release retains final session")
	}
}

func TestArchiveDiffHashMatchesVersionSnapshot(t *testing.T) {
	a := pvf.New()
	addDiffText(t, a, "item.equ", "[name]\n`logical`")
	if _, err := a.AddFileText("text.str", "文本\r\n", pvf.TypeUnicode); err != nil {
		t.Fatal(err)
	}
	a.AddFile("binary.bin", []byte{0, 1, 2, 255}, 99)
	expected, err := pvfversion.SnapshotFromArchive(a)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := archiveDiffSnapshot(context.Background(), a, 0, func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	for path, entry := range expected {
		if actual[path] != entry {
			t.Fatalf("incompatible logical hash for %s", path)
		}
	}
}

func TestArchiveDiffParallelSnapshotMatchesSerialAndProgress(t *testing.T) {
	a := pvf.New()
	for i := 0; i < 256; i++ {
		addDiffText(t, a, fmt.Sprintf("scripts/%04d.equ", i), fmt.Sprintf("[name]\n`item %d`\n[price]\n%d", i, i*10))
		a.AddFile(fmt.Sprintf("binary/%04d.bin", i), bytes.Repeat([]byte{byte(i)}, 1024), 99)
	}
	expected, err := pvfversion.SnapshotFromArchive(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, workers := range []int{1, 4, 13, 32} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			progress := 37
			actual, err := archiveDiffSnapshotWorkers(context.Background(), a, 37, func(n int) {
				if n < progress || n > 37+int(a.FileCount()) {
					t.Errorf("invalid progress %d after %d", n, progress)
				}
				progress = n
			}, workers)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatal("parallel snapshot differs from serial logical snapshot")
			}
			if progress != 37+int(a.FileCount()) {
				t.Fatalf("incomplete progress %d", progress)
			}
		})
	}
	if got := archiveDiffWorkerCount(a.FileCount()); got != min(int(a.FileCount()), max(1, runtime.GOMAXPROCS(0)-1)) {
		t.Fatalf("worker count %d does not follow available CPUs", got)
	}
	if archiveDiffWorkerCount(1) != 1 || archiveDiffWorkerCount(0) != 0 {
		t.Fatal("worker count exceeds file count")
	}
}

func TestArchiveDiffParallelCancellationDrainsWorkers(t *testing.T) {
	a := pvf.New()
	for i := 0; i < 512; i++ {
		a.AddFile(fmt.Sprintf("%04d.bin", i), bytes.Repeat([]byte{byte(i)}, 4096), 99)
	}
	ctx, cancel := context.WithCancel(context.Background())
	last := 0
	_, err := archiveDiffSnapshotWorkers(ctx, a, 0, func(n int) { last = n; cancel() }, 13)
	if err != context.Canceled {
		t.Fatalf("cancellation error=%v", err)
	}
	if last != 1 {
		t.Fatalf("progress continued after cancellation: %d", last)
	}
	// A fresh scan must remain usable after all cancelled workers exit.
	result, err := archiveDiffSnapshotWorkers(context.Background(), a, 0, func(int) {}, 13)
	if err != nil || len(result) != int(a.FileCount()) {
		t.Fatalf("fresh scan=%d error=%v", len(result), err)
	}
}

func BenchmarkArchiveDiffParallelSnapshot(b *testing.B) {
	for _, kind := range []string{"script", "binary"} {
		a := pvf.New()
		for i := 0; i < 1024; i++ {
			path := fmt.Sprintf("files/%04d", i)
			if kind == "script" {
				if _, err := a.AddFileText(path+".equ", "[values]\n"+strings.Repeat("1 2 3 4 5 6 7 8\n", 128), pvf.TypeScript); err != nil {
					b.Fatal(err)
				}
			} else {
				a.AddFile(path+".bin", bytes.Repeat([]byte{byte(i)}, 64<<10), 99)
			}
		}
		for _, workers := range []int{1, 4, 13} {
			b.Run(fmt.Sprintf("%s/workers-%d", kind, workers), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := archiveDiffSnapshotWorkers(context.Background(), a, 0, func(int) {}, workers); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
