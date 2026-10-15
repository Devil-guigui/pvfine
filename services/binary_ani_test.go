package services

import (
	"bytes"
	"strings"
	"testing"

	"pvfine/internal/pvf"
)

func TestBinaryANISourceAndPreview(t *testing.T) {
	a := pvf.New()
	// A standard archive carrying a native ANI resource proves this path does not
	// depend on ACT60, TW or any other archive profile.
	native := []byte{1, 0, 0, 0, 0, 0, 0, 0, 255, 255, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 12, 0, 50, 0, 0, 0}
	index := a.AddFile("animation/test.ani", native, 0)
	var packed bytes.Buffer
	if err := a.SaveTo(&packed); err != nil {
		t.Fatal(err)
	}
	c := NewCore()
	if err := c.setArchive(a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.closeArchive)
	editor := NewEditorService(c)
	for _, read := range []func(int32) (*FileMeta, error){editor.GetFileBasic, editor.GetFile} {
		meta, err := read(index)
		if err != nil {
			t.Fatal(err)
		}
		if meta.Editable || meta.DataType != 0 || !strings.HasPrefix(meta.Text, "#PVF_File") {
			t.Fatal("binary ANI view is not read-only decoded text")
		}
		preview, err := NewPreviewService().ParseANI(meta.Text)
		if err != nil || !preview.Valid || len(preview.Frames) != 1 || preview.Frames[0].Delay != 50 {
			t.Fatal("binary ANI did not reach the existing preview", err)
		}
	}
	raw, err := a.RawBytes(index)
	if err != nil || !bytes.Equal(raw, native) || a.Modified() {
		t.Fatal("viewing binary ANI modified archive", err)
	}
	var rewritten bytes.Buffer
	if err := a.SaveTo(&rewritten); err != nil || !bytes.Equal(packed.Bytes(), rewritten.Bytes()) {
		t.Fatal("viewing binary ANI changed saved bytes", err)
	}
	if err := a.SetRawBytes(index, []byte{255, 255, 0}); err != nil {
		t.Fatal(err)
	}
	bad, err := editor.GetFile(index)
	if err != nil || bad.Editable || !bad.Modified || !strings.Contains(bad.Text, "解码失败") {
		t.Fatal("invalid binary ANI did not report decode failure", err)
	}
}
