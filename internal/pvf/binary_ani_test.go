package pvf

import (
	"bytes"
	"strings"
	"testing"
)

// This native resource is stored in an ordinary archive, with no ACT60 state.
func binaryANIFixture() []byte {
	return []byte{1, 0, 0, 0, 0, 0, 0, 0, 255, 255, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 12, 0, 50, 0, 0, 0}
}
func TestBinaryANIDecodeIndependentOfContainer(t *testing.T) {
	a := New()
	defer a.Release()
	native := binaryANIFixture()
	i := a.AddFile("animation/test.ani", native, 0)
	a.items[i].nameOff = a.StringOffset("test.ANI") // preserve original-case names from native containers
	text, err := a.Text(i)
	if err != nil || !strings.Contains(text, "[FRAME000]") || !strings.Contains(text, "[DELAY]\n\t\t50") {
		t.Fatal("binary ANI decode failed", err)
	}
	if err := a.SetText(i, "[FRAME MAX] 0"); err == nil {
		t.Fatal("binary ANI accepted text edits without an encoder")
	}
	var out bytes.Buffer
	if err := a.SaveTo(&out); err != nil {
		t.Fatal(err)
	}
	b, err := Parse(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release()
	raw, err := b.RawBytes(i)
	if err != nil || !bytes.Equal(native, raw) || b.File(i).DataType != 0 {
		t.Fatal("binary ANI changed during save", err)
	}
	got, err := b.Text(i)
	if err != nil || got != text {
		t.Fatal("saved binary ANI failed to decode", err)
	}
}

func TestRealBinaryANIDecode(t *testing.T) {
	a := openReal(t)
	defer a.Release()
	decoded := 0
	for i := int32(0); i < a.FileCount(); i++ {
		file := a.File(i)
		if file.DataType == TypeScript || file.DataType == TypeUnicode || !strings.HasSuffix(a.Path(i), ".ani") {
			continue
		}
		if _, err := a.Text(i); err != nil {
			t.Fatalf("binary ANI entry %d: %v", i, err)
		}
		decoded++
	}
	if decoded == 0 {
		t.Skip("no native binary ANI resources")
	}
	t.Logf("decoded %d binary ANI resources", decoded)
}
