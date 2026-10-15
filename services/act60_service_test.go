package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pvfine/internal/pvf"
)

func TestACT60ArchiveService(t *testing.T) {
	src := os.Getenv("PVF_ACT60_TESTFILE")
	if src == "" {
		t.Skip("PVF_ACT60_TESTFILE not set")
	}
	// Version/index sidecars must never be created next to the local fixture.
	path := filepath.Join(t.TempDir(), "ACT60.pvf")
	if err := os.Link(src, path); err != nil {
		t.Fatal(err)
	}
	c := NewCore()
	t.Cleanup(c.closeArchive)
	svc := NewArchiveService(c)
	info, err := svc.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != pvf.FormatACT60 || info.FileCount == 0 || !info.WriteCapabilities.CanSave {
		t.Fatal("incorrect ACT60 info")
	}
	deadline := time.Now().Add(time.Minute)
	for {
		c.mu.RLock()
		ready := info.FileCount < largeArchiveIndexThreshold || c.diskIndex != nil
		status := c.indexStatus
		c.mu.RUnlock()
		if ready {
			break
		}
		if status.State == IndexStateError {
			t.Fatal(status.Error)
		}
		if time.Now().After(deadline) {
			t.Fatal("ACT60 directory index timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
	root, err := svc.ListChildren("")
	if err != nil || len(root) == 0 {
		t.Fatal("ACT60 tree", err)
	}
	c.mu.RLock()
	index := int32(-1)
	for i := int32(0); i < c.archive.FileCount(); i++ {
		if strings.HasSuffix(c.archive.Path(i), ".equ") {
			index = i
			break
		}
	}
	c.mu.RUnlock()
	if index < 0 {
		t.Fatal("ACT60 equipment missing")
	}
	editor := NewEditorService(c)
	for _, read := range []func(int32) (*FileMeta, error){editor.GetFile, editor.GetFileBasic} {
		file, err := read(index)
		if err != nil {
			t.Fatal(err)
		}
		if !file.Editable || file.DataType != pvf.TypeScript || !strings.Contains(file.Text, "[name]") {
			t.Fatal("ACT60 source must expose the script editor")
		}
	}
	c.mu.Lock()
	metadata, metadataErr := c.archive.ScriptMetadata(index)
	c.mu.Unlock()
	_, _, unresolved := pvf.ParsePlaceholder(metadata.Name)
	if metadataErr != nil || !metadata.HasName || unresolved {
		t.Fatal("ACT60 semantic metadata missing", metadataErr)
	}
	before, err := editor.GetFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Annotations) == 0 {
		t.Fatal("ACT60 script annotations missing")
	}
	changed := before.Text + "\r\n// 服务层保存验证\r\n"
	if err := editor.SetText(index, changed); err != nil {
		t.Fatal(err)
	}
	saved, err := editor.Save()
	if err != nil {
		t.Fatal(err)
	}
	if saved.ModifiedCount != 0 || saved.Format != pvf.FormatACT60 {
		t.Fatal("incorrect saved ACT60 state")
	}
	reopened, err := pvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Release()
	text, err := reopened.Text(index)
	if err != nil || text != changed {
		t.Fatal("service save did not round trip", err)
	}

}
