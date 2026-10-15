package pvf

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"math/bits"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func act60Fixture(t *testing.T, legacy bool) []byte {
	t.Helper()
	encrypt := func(raw []byte, seed uint32) ([]byte, uint32) {
		out := make([]byte, (len(raw)+3)&^3)
		copy(out, raw)
		crc := crc32.Update(seed, crc32.IEEETable, out)
		for i := 0; i < len(out); i += 4 {
			binary.LittleEndian.PutUint32(out[i:], bits.RotateLeft32(binary.LittleEndian.Uint32(out[i:]), 6)^crc)
		}
		return out, crc
	}
	text, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("[name]\r\n`测试装备`\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	var tree, body bytes.Buffer
	paths := []string{"equipment/test.equ", "animation/test.ani", "etc/empty.txt", "equipment/equipment.lst"}
	for i, raw := range [][]byte{text, {7, 0, 1, 0, 35, 0, 0, 0, 255}, {}, []byte("1\t`test.equ`\r\n")} {
		number, offset := uint32(i+17), uint32(body.Len())
		seed := number
		if legacy {
			seed = offset
		}
		enc, crc := encrypt(raw, seed)
		if legacy {
			for _, v := range []uint32{number, offset, uint32(len(raw)), crc, uint32(len(paths[i]))} {
				binary.Write(&tree, binary.LittleEndian, v)
			}
			tree.WriteString(paths[i])
		} else {
			binary.Write(&tree, binary.LittleEndian, number)
			binary.Write(&tree, binary.LittleEndian, uint32(len(paths[i])))
			tree.WriteString(paths[i])
			for _, v := range []uint32{uint32(len(raw)), crc, offset} {
				binary.Write(&tree, binary.LittleEndian, v)
			}
		}
		body.Write(enc)
	}
	enc, crc := encrypt(tree.Bytes(), uint32(len(paths)))
	var out bytes.Buffer
	if legacy {
		out.WriteString(act60Signature)
	} else {
		binary.Write(&out, binary.LittleEndian, uint32(36))
		out.WriteString("13ff9a94-0204-0410-845a-ceeffe637190")
		binary.Write(&out, binary.LittleEndian, uint32(48323))
	}
	for _, v := range []uint32{uint32(len(enc)), crc, uint32(len(paths))} {
		binary.Write(&out, binary.LittleEndian, v)
	}
	out.Write(enc)
	out.Write(body.Bytes())
	return out.Bytes()
}

func TestACT60Parse(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		name := "modern"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			original := act60Fixture(t, legacy)
			before := bytes.Clone(original)
			a, err := Parse(original)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Release()
			if a.Format() != FormatACT60 || a.ClientVersion() != "60ACT" || a.FileCount() != 4 || a.Modified() {
				t.Fatal("ACT60 state mismatch")
			}
			if !a.WriteCapabilities().CanSave || !a.WriteCapabilities().CanChangeStructure {
				t.Fatal("ACT60 should advertise write support")
			}
			index, ok := a.Find("EQUIPMENT/TEST.EQU")
			if !ok {
				t.Fatal("case insensitive path missing")
			}
			text, err := a.Text(index)
			if err != nil || text != "[name]\r\n`测试装备`\r\n" {
				t.Fatalf("text decode: %q %v", text, err)
			}
			raw, err := a.RawBytes(1)
			if err != nil || !bytes.Equal(raw, []byte{7, 0, 1, 0, 35, 0, 0, 0, 255}) {
				t.Fatal("binary payload changed", err)
			}
			if text, err := a.Text(2); err != nil || text != "" {
				t.Fatal("empty file", err)
			}
			err = a.ForEachComparisonChunk(context.Background(), []int32{0, 1}, func(c ComparisonChunk) error {
				for _, i := range c.Indexes() {
					x, e := c.Bytes(i)
					if e != nil {
						return e
					}
					y, e := a.RawBytes(i)
					if e != nil {
						return e
					}
					if !bytes.Equal(x, y) {
						t.Fatal("comparison mismatch")
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := a.SaveTo(&out); err != nil || !bytes.Equal(original, out.Bytes()) {
				t.Fatal("untouched export changed", err)
			}
			clone := a.CloneForBatch()
			defer clone.Release()
			if err := clone.SetText(index, "修改"); err != nil {
				t.Fatal(err)
			}
			out.Reset()
			if err := clone.SaveTo(&out); err != nil {
				t.Fatal(err)
			}
			reopened, err := Parse(out.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Release()
			if text, err := reopened.Text(index); err != nil || text != "修改" {
				t.Fatal("edited text did not round trip", err)
			}
			if clone.Modified() || clone.FileIdentity(index) != a.FileIdentity(index) {
				t.Fatal("save state/identity mismatch")
			}
			if a.Modified() || !bytes.Equal(original, before) {
				t.Fatal("clone/parse mutated source")
			}
		})
	}
}

func TestACT60RejectCorruption(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		original := act60Fixture(t, legacy)
		for _, off := range []int{len(original) - 4, len(original) / 2} {
			bad := bytes.Clone(original)
			bad[off] ^= 1
			if _, err := Parse(bad); err == nil {
				t.Fatal("corrupt ACT60 accepted")
			}
		}
		for n := 0; n < len(original); n++ {
			if _, err := Parse(original[:n]); err == nil {
				t.Fatalf("truncated ACT60 accepted: %d", n)
			}
		}
	}
}

// Run separately for each local revision with PVF_TESTFILE. Never hardcode a
// personal fixture path or commit client executables/archives to the repository.
func TestRealACT60Parse(t *testing.T) {
	a := openReal(t)
	defer a.Release()
	if a.Format() != FormatACT60 {
		t.Skip("not an ACT60 archive")
	}
	if a.FileCount() == 0 {
		t.Fatal("empty directory")
	}
	var out bytes.Buffer
	if err := a.SaveTo(&out); err != nil || !bytes.Equal(out.Bytes(), a.data) {
		t.Fatal("untouched export changed", err)
	}
	scripts, texts, binaries := 0, 0, 0
	for i := int32(0); i < a.FileCount(); i++ {
		raw, err := a.RawBytes(i)
		if err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		if a.items[i].typ == TypeScript {
			scripts++
			if len(raw)%5 != 0 {
				t.Fatal("invalid token payload")
			}
		} else if a.items[i].typ == TypeUnicode {
			texts++
			if len(raw)%2 != 0 {
				t.Fatal("invalid UTF-16 payload")
			}
		} else {
			binaries++
		}
	}
	for i := int32(0); i < a.FileCount(); i++ {
		if strings.HasSuffix(a.Path(i), ".equ") {
			text, err := a.Text(i)
			if err != nil || !strings.Contains(text, "[name]") {
				t.Fatal("equipment source failed", err)
			}
			break
		}
	}
	t.Logf("entries=%d script=%d text=%d binary=%d", a.FileCount(), scripts, texts, binaries)
}
