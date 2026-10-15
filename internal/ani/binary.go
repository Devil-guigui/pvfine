// Package ani reads native binary animation resources independently of their
// archive container. Format reference: pvfUtility BinaryAniCompiler.cs at
// 53dc8e48bba0f7871ca96bcf10bfe6d2028d0582. This is a Go implementation of the
// observed binary layout, not a copy of the recovered C# source.
package ani

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

var ErrBinary = errors.New("ani: invalid binary animation")

const maxDecodedBytes = 64 << 20

type binaryReader struct {
	data []byte
	pos  int
	err  error
}

func (r *binaryReader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || n > len(r.data)-r.pos {
		r.err = fmt.Errorf("%w: truncated at byte %d", ErrBinary, r.pos)
		return nil
	}
	b := r.data[r.pos : r.pos+n]
	r.pos += n
	return b
}
func (r *binaryReader) u8() byte {
	b := r.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}
func (r *binaryReader) u16() uint16 {
	b := r.take(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}
func (r *binaryReader) i16() int16 { return int16(r.u16()) }
func (r *binaryReader) i32() int32 {
	b := r.take(4)
	if b == nil {
		return 0
	}
	return int32(binary.LittleEndian.Uint32(b))
}
func (r *binaryReader) float() string {
	return strconv.FormatFloat(float64(math.Float32frombits(uint32(r.i32()))), 'g', -1, 32)
}
func (r *binaryReader) ascii() string {
	n := r.i32()
	if n > 1<<20 {
		r.err = fmt.Errorf("%w: resource string exceeds limit", ErrBinary)
		return ""
	}
	b := r.take(int(n))
	if b == nil {
		return ""
	}
	out := make([]byte, len(b))
	for i, v := range b {
		if v > 127 {
			v = '?'
		}
		out[i] = v
	}
	return string(out)
}
func (r *binaryReader) fail(context string, id uint16) {
	if r.err == nil {
		r.err = fmt.Errorf("%w: unknown %s field %d at byte %d", ErrBinary, context, id, r.pos-2)
	}
}

var effects = []string{"NONE", "DODGE", "LINEARDODGE", "DARK", "XOR", "MONOCHROME", "SPACEDISTORT"}
var damageTypes = []string{"NORMAL", "SUPERARMOR", "UNBREAKABLE"}
var flips = []string{"0", "HORIZON", "VERTICAL", "ALL"}

func enumName(n uint16, names []string) string {
	if int(n) < len(names) {
		return names[n]
	}
	return strconv.Itoa(int(n))
}
func quote(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
func field(out *strings.Builder, name string, values ...any) {
	fmt.Fprintf(out, "[%s]\n", name)
	if len(values) > 0 {
		out.WriteByte('\t')
		for i, v := range values {
			if i > 0 {
				out.WriteByte('\t')
			}
			fmt.Fprint(out, v)
		}
		out.WriteByte('\n')
	}
}

// DecodeBinary returns source ANI text for the native frame/path-table format.
// It checks all reads and rejects unknown fields rather than losing sync or
// silently dropping data. Up to three zero alignment bytes are accepted.
func DecodeBinary(data []byte) (string, error) {
	r := &binaryReader{data: data}
	frames, paths := int(r.u16()), int(r.u16())
	if r.err != nil {
		return "", r.err
	}
	if paths > (len(data)-r.pos)/4 {
		return "", fmt.Errorf("%w: path count exceeds input", ErrBinary)
	}
	images := make([]string, paths)
	for i := range images {
		images[i] = r.ascii()
		if r.err != nil {
			return "", r.err
		}
	}
	var out strings.Builder
	out.WriteString("#PVF_File\n\n")
	globals := int(r.u16())
	for i := 0; i < globals && r.err == nil; i++ {
		id := r.u16()
		switch id {
		case 0:
			field(&out, "LOOP", r.u8())
		case 1:
			field(&out, "SHADOW", r.u8())
		case 3:
			field(&out, "COORD", r.u16())
		case 28:
			field(&out, "OPERATION", r.u16())
		case 18:
			field(&out, "SPECTRUM", r.u8())
			field(&out, "SPECTRUM TERM", r.i32())
			field(&out, "SPECTRUM LIFE TIME", r.i32())
			field(&out, "SPECTRUM COLOR", r.u8(), r.u8(), r.u8(), r.u8())
			field(&out, "SPECTRUM EFFECT", quote(enumName(r.u16(), effects)))
		default:
			r.fail("global", id)
		}
	}
	if r.err != nil {
		return "", r.err
	}
	if frames > (len(data)-r.pos)/14 {
		return "", fmt.Errorf("%w: frame count exceeds input", ErrBinary)
	}
	field(&out, "FRAME MAX", frames)
	for i := 0; i < frames && r.err == nil; i++ {
		out.WriteByte('\n')
		field(&out, fmt.Sprintf("FRAME%03d", i))
		var frameOut, boxes strings.Builder
		count := int(r.u16())
		for j := 0; j < count && r.err == nil; j++ {
			id := r.u16()
			name := "DAMAGE BOX"
			if id == 15 {
				name = "ATTACK BOX"
			} else if id != 14 {
				r.fail("box", id)
				break
			}
			field(&boxes, name, r.i32(), r.i32(), r.i32(), r.i32(), r.i32(), r.i32())
		}
		if r.err != nil {
			return "", r.err
		}
		image := int(r.i16())
		if image >= 0 {
			if image >= len(images) {
				return "", fmt.Errorf("%w: frame %d image path index %d exceeds %d", ErrBinary, i, image, len(images))
			}
			field(&frameOut, "IMAGE", quote(images[image]), r.u16())
		} else {
			field(&frameOut, "IMAGE", quote(""), 0)
		}
		field(&frameOut, "IMAGE POS", r.i32(), r.i32())
		count = int(r.u16())
		for j := 0; j < count && r.err == nil; j++ {
			id := r.u16()
			switch id {
			case 0:
				field(&frameOut, "LOOP", r.u8())
			case 1:
				field(&frameOut, "SHADOW", r.u8())
			case 3:
				field(&frameOut, "COORD", r.u16())
			case 7:
				field(&frameOut, "IMAGE RATE", r.float(), r.float())
			case 8:
				field(&frameOut, "IMAGE ROTATE", r.float())
			case 9:
				field(&frameOut, "RGBA", r.u8(), r.u8(), r.u8(), r.u8())
			case 10:
				field(&frameOut, "INTERPOLATION", r.u8())
			case 11:
				effect := r.u16()
				field(&frameOut, "GRAPHIC EFFECT", quote(enumName(effect, effects)))
				if effect == 5 {
					fmt.Fprintf(&frameOut, "\t%d\t%d\t%d\n", r.u8(), r.u8(), r.u8())
				}
				if effect == 6 {
					fmt.Fprintf(&frameOut, "\t%d\t%d\n", r.i16(), r.i16())
				}
			case 12:
				field(&frameOut, "DELAY", r.i32())
			case 13:
				field(&frameOut, "DAMAGE TYPE", quote(enumName(r.u16(), damageTypes)))
			case 16:
				field(&frameOut, "PLAY SOUND", quote(r.ascii()))
			case 17:
				field(&frameOut, "PRELOAD", 1)
			case 23:
				field(&frameOut, "SET FLAG", r.i32())
			case 24:
				field(&frameOut, "FLIP TYPE", quote(enumName(r.u16(), flips)))
			case 25:
				field(&frameOut, "LOOP START")
			case 26:
				field(&frameOut, "LOOP END", r.i32())
			case 27:
				field(&frameOut, "CLIP", r.i16(), r.i16(), r.i16(), r.i16())
			default:
				r.fail("frame", id)
			}
		}
		if out.Len()+frameOut.Len()+boxes.Len() > maxDecodedBytes {
			return "", fmt.Errorf("%w: decoded source exceeds limit", ErrBinary)
		}
		frameOut.WriteString(boxes.String())
		for _, line := range strings.SplitAfter(frameOut.String(), "\n") {
			if line != "" {
				out.WriteByte('\t')
				out.WriteString(line)
			}
		}
	}
	if r.err != nil {
		return "", r.err
	}
	tail := data[r.pos:]
	if len(tail) > 3 {
		return "", fmt.Errorf("%w: %d trailing bytes", ErrBinary, len(tail))
	}
	for _, b := range tail {
		if b != 0 {
			return "", fmt.Errorf("%w: nonzero trailing byte", ErrBinary)
		}
	}
	return out.String(), nil
}
