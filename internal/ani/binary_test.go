package ani

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"pvfine/internal/preview"
)

func binaryFixture() []byte {
	var b bytes.Buffer
	put := func(v any) {
		if err := binary.Write(&b, binary.LittleEndian, v); err != nil {
			panic(err)
		}
	}
	put(uint16(2))
	put(uint16(1))
	image := "Monster/test.img"
	put(int32(len(image)))
	b.WriteString(image)
	put(uint16(5))
	put(uint16(0))
	put(byte(1))
	put(uint16(1))
	put(byte(1))
	put(uint16(3))
	put(uint16(2))
	put(uint16(28))
	put(uint16(8))
	put(uint16(18))
	put(byte(1))
	put(int32(30))
	put(int32(100))
	b.Write([]byte{1, 2, 3, 255})
	put(uint16(2))
	put(uint16(2)) // two collision boxes
	for _, id := range []uint16{14, 15} {
		put(id)
		for _, n := range []int32{-1, -2, -3, 4, 5, 6} {
			put(n)
		}
	}
	put(int16(0))
	put(uint16(8))
	put(int32(-12))
	put(int32(34))
	put(uint16(19))
	put(uint16(0))
	put(byte(0))
	put(uint16(1))
	put(byte(0))
	put(uint16(3))
	put(uint16(1))
	put(uint16(7))
	put(float32(1.5))
	put(float32(0.75))
	put(uint16(8))
	put(float32(-12.5))
	put(uint16(9))
	b.Write([]byte{255, 128, 64, 32})
	put(uint16(10))
	put(byte(1))
	put(uint16(11))
	put(uint16(5))
	b.Write([]byte{7, 8, 9})
	put(uint16(11))
	put(uint16(6))
	put(int16(-10))
	put(int16(20))
	put(uint16(12))
	put(int32(80))
	put(uint16(13))
	put(uint16(1))
	put(uint16(16))
	put(int32(9))
	b.WriteString("test`wave")
	put(uint16(17))
	put(uint16(23))
	put(int32(123))
	put(uint16(24))
	put(uint16(3))
	put(uint16(25))
	put(uint16(26))
	put(int32(4))
	put(uint16(27))
	for _, n := range []int16{-1, -2, 10, 20} {
		put(n)
	}
	put(uint16(13))
	put(uint16(99)) // unknown enum values still have a known width
	put(uint16(0))
	put(int16(-1))
	put(int32(0))
	put(int32(0))
	put(uint16(1))
	put(uint16(12))
	put(int32(40))
	return b.Bytes()
}

func TestDecodeBinaryAllFields(t *testing.T) {
	input := binaryFixture()
	before := bytes.Clone(input)
	text, err := DecodeBinary(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"[FRAME MAX]\n\t2", "[IMAGE]\n\t`Monster/test.img`\t8", "[IMAGE POS]\n\t-12\t34",
		"[IMAGE RATE]\n\t1.5\t0.75", "[IMAGE ROTATE]\n\t-12.5", "[RGBA]\n\t255\t128\t64\t32",
		"[SPECTRUM TERM]\n\t30", "[SPECTRUM COLOR]\n\t1\t2\t3\t255", "[SPECTRUM EFFECT]\n\t`LINEARDODGE`",
		"[GRAPHIC EFFECT]\n\t`MONOCHROME`\n\t7\t8\t9", "[GRAPHIC EFFECT]\n\t`SPACEDISTORT`\n\t-10\t20",
		"[DAMAGE TYPE]\n\t`SUPERARMOR`", "[DAMAGE TYPE]\n\t`99`", "[PLAY SOUND]\n\t`test``wave`",
		"[FLIP TYPE]\n\t`ALL`", "[LOOP START]\n", "[LOOP END]\n\t4", "[CLIP]\n\t-1\t-2\t10\t20",
		"[DAMAGE BOX]\n\t-1\t-2\t-3\t4\t5\t6", "[ATTACK BOX]\n\t-1\t-2\t-3\t4\t5\t6",
		"[FRAME001]\n[IMAGE]\n\t``\t0", "[DELAY]\n\t40", "[PRELOAD]\n\t1", "[SET FLAG]\n\t123",
	} {
		unindent := func(s string) string {
			lines := strings.Split(s, "\n")
			for i := range lines {
				lines[i] = strings.TrimLeft(lines[i], "\t")
			}
			return strings.Join(lines, "\n")
		}
		if !strings.Contains(unindent(text), unindent(fragment)) {
			t.Fatalf("missing decoded field %q", fragment)
		}
	}
	if !bytes.Equal(input, before) {
		t.Fatal("decoder changed input")
	}
	doc := preview.ParseANI(text)
	if !doc.Valid || !doc.Loop || !doc.Shadow || len(doc.Frames) != 2 || doc.Frames[0].Delay != 80 || len(doc.Frames[0].Layers) != 1 || doc.Frames[0].Layers[0].X != -12 || len(doc.Frames[1].Layers) != 1 || doc.Frames[1].Layers[0].Path != "" {
		t.Fatalf("preview mismatch: %+v", doc)
	}
}

func TestDecodeBinaryRejectsInvalidData(t *testing.T) {
	original := binaryFixture()
	for n := 0; n < len(original); n++ {
		if _, err := DecodeBinary(original[:n]); !errors.Is(err, ErrBinary) {
			t.Fatalf("truncated length %d: %v", n, err)
		}
	}
	for _, input := range [][]byte{
		{1, 0, 0, 0, 1, 0, 99, 0},                 // unknown global
		{1, 0, 1, 0, 255, 255, 255, 255},          // negative path length
		{1, 0, 255, 255, 0, 0},                    // path count out of bounds
		{255, 255, 0, 0, 0, 0},                    // frame count out of bounds
		append(bytes.Clone(original), 0, 0, 0, 0), // unexplained tail
		append(bytes.Clone(original), 1),
	} {
		if _, err := DecodeBinary(input); !errors.Is(err, ErrBinary) {
			t.Fatal("invalid data accepted", err)
		}
	}
	for n := 0; n <= 3; n++ {
		if _, err := DecodeBinary(append(bytes.Clone(original), make([]byte, n)...)); err != nil {
			t.Fatal("alignment padding rejected", err)
		}
	}
}

func TestDecodeBinaryBadFrameFields(t *testing.T) {
	// One empty-image frame; exercise the variable-length sections separately.
	prefix := []byte{1, 0, 0, 0, 0, 0}
	for _, frame := range [][]byte{
		{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},            // missing image path
		{1, 0, 99, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},           // unknown collision field
		{0, 0, 255, 255, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 99, 0}, // unknown frame field
	} {
		if _, err := DecodeBinary(append(bytes.Clone(prefix), frame...)); !errors.Is(err, ErrBinary) {
			t.Fatal("bad frame accepted", err)
		}
	}
}

func FuzzDecodeBinary(f *testing.F) {
	f.Add(binaryFixture())
	f.Add([]byte{0, 0, 0, 0, 0, 0})
	f.Add([]byte{255, 255, 255, 255})
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = DecodeBinary(data) })
}
