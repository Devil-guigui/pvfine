package pvf

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestTextEncodingReinterpretsNarrowPool(t *testing.T) {
	a := New()
	raw, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("测试"))
	a.strA = append(raw, 0)
	a.strW = utf16le("固定 Unicode\x00")
	a.poolsDirty = false
	if got := a.ResolveString(0); got == "测试" {
		t.Fatal("fixture already decoded")
	}
	b, err := a.WithTextEncoding("GBK")
	if err != nil {
		t.Fatal(err)
	}
	if b.ResolveString(0) != "测试" || b.ResolveString(1) != "固定 Unicode" {
		t.Fatal("wrong pool decoding")
	}
	if b.StringOffset("测试") != 0 {
		t.Fatal("string lookup did not use selected codec")
	}
	if a.ResolveString(0) == "测试" {
		t.Fatal("original archive mutated")
	}
	if b.Modified() || !bytes.Equal(a.strA, b.strA) {
		t.Fatal("switch modified source")
	}
	if b.CloneForBatch().TextEncoding() != "GBK" {
		t.Fatal("clone lost encoding")
	}
	c, err := b.WithTextEncoding("auto")
	if err != nil || c.ResolveString(0) != a.ResolveString(0) {
		t.Fatal("auto did not restore decoding", err)
	}
	if _, err := b.WithTextEncoding("invalid"); err == nil {
		t.Fatal("accepted invalid codec")
	}
}

func TestNativeTextEncodingSwitchAndSave(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		data                  func(*testing.T) []byte
		label, path, expected string
	}{
		{"ACT60", func(t *testing.T) []byte { return act60Fixture(t, false) }, "GBK", "equipment/test.equ", "测试装备"},
		{"TW", twFixture, "Big5", "equipment/test.equ", "測試裝備"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := tc.data(t)
			a, err := Parse(source)
			if err != nil {
				t.Fatal(err)
			}
			if a.Info().TextEncodingLabel != tc.label {
				t.Fatal("wrong default", a.Info())
			}
			b, err := a.WithTextEncoding("UTF-8")
			if err != nil {
				t.Fatal(err)
			}
			idx, _ := b.Find(tc.path)
			text, _ := b.Text(idx)
			if strings.Contains(text, tc.expected) {
				t.Fatal("codec did not change text")
			}
			var untouched bytes.Buffer
			if err := b.SaveTo(&untouched); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(source, untouched.Bytes()) {
				t.Fatal("switch rewrote untouched bytes")
			}
			b, err = b.WithTextEncoding("auto")
			if err != nil {
				t.Fatal(err)
			}
			text, _ = b.Text(idx)
			if !strings.Contains(text, tc.expected) {
				t.Fatal("auto did not restore text", text)
			}
			b, err = b.WithTextEncoding("UTF-8")
			if err != nil {
				t.Fatal(err)
			}
			if err := b.SetText(idx, "[name]\n`编码切换保存😀`\n"); err != nil {
				t.Fatal(err)
			}
			if _, err := b.WithTextEncoding("GBK"); err == nil {
				t.Fatal("pending edits silently discarded")
			}
			var edited bytes.Buffer
			if err := b.SaveTo(&edited); err != nil {
				t.Fatal(err)
			}
			if b.TextEncoding() != "UTF-8" {
				t.Fatal("save lost encoding")
			}
			path := filepath.Join(t.TempDir(), "edited.pvf")
			if err := os.WriteFile(path, edited.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := OpenWithTextEncoding(path, "", "UTF-8")
			if err != nil {
				t.Fatal(err)
			}
			idx, _ = c.Find(tc.path)
			text, _ = c.Text(idx)
			if !strings.Contains(text, "编码切换保存😀") {
				t.Fatal("save used wrong codec", text)
			}
		})
	}
}

func TestUTF16LESelectionPreservesPoolLayout(t *testing.T) {
	a := New()
	a.strA = []byte("equipment\x00")
	a.strW = utf16le("测试😀\x00")
	a.poolsDirty = false
	b, err := a.WithTextEncoding("UTF-16LE")
	if err != nil {
		t.Fatal(err)
	}
	if b.TextEncodingLabel() != "UTF-16LE" || b.ResolveString(0) != "equipment" || b.ResolveString(1) != "测试😀" {
		t.Fatal("UTF-16LE selection corrupted the fixed string pool layout")
	}
	if b.StringOffset("equipment") != 0 || b.StringOffset("测试😀") != 1 {
		t.Fatal("string indexes diverged from decoding")
	}
}

func TestNativeUTF16LETextSaveAndReopen(t *testing.T) {
	for _, tc := range []struct {
		name string
		data func(*testing.T) []byte
	}{
		{"ACT60", func(t *testing.T) []byte { return act60Fixture(t, false) }},
		{"TW", twFixture},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := Parse(tc.data(t))
			if err != nil {
				t.Fatal(err)
			}
			a, err = a.WithTextEncoding("UTF-16LE")
			if err != nil {
				t.Fatal(err)
			}
			expected := "name>UTF-16LE 文本😀\r\n"
			for _, path := range []string{"text/utf16.str", "sqr/utf16.nut"} {
				if _, err := a.AddFileText(path, expected, TypeUnicode); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			if err := a.SaveTo(&output); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "utf16.pvf")
			if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			b, err := OpenWithTextEncoding(path, "", "UTF-16LE")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"text/utf16.str", "sqr/utf16.nut"} {
				index, ok := b.Find(name)
				if !ok {
					t.Fatal("missing UTF-16 text file")
				}
				text, err := b.Text(index)
				if err != nil || text != expected {
					t.Fatalf("UTF-16LE text round trip: %q %v", text, err)
				}
			}
			codec := b.nativeTextCodec()
			raw, err := b.encodeNativeText(expected)
			if err != nil || !bytes.Equal(raw, utf16le(expected)) || codec == nil {
				t.Fatal("wrong encoder", err)
			}
			if b.decodeNativeText(append([]byte{0xff, 0xfe}, raw...)) != expected {
				t.Fatal("BOM was not stripped")
			}
			if b.nativePayloadIsText([]byte{0, 0, 255, 255}) {
				t.Fatal("classified binary control data as text")
			}
		})
	}
}
