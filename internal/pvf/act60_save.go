package pvf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"math/bits"

	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func act60Encrypt(raw []byte, seed uint32) ([]byte, uint32) {
	out := make([]byte, (len(raw)+3)&^3)
	copy(out, raw)
	crc := crc32.Update(seed, crc32.IEEETable, out)
	for i := 0; i < len(out); i += 4 {
		binary.LittleEndian.PutUint32(out[i:], bits.RotateLeft32(binary.LittleEndian.Uint32(out[i:]), 6)^crc)
	}
	return out, crc
}

// Changed source files retain their original encoding. Unchanged entries never
// pass through a text decoder, including legacy files relocated during packing.
func (a *Archive) encodeACT60Payload(index int32, raw []byte, old *act60Entry) ([]byte, error) {
	typ := a.items[index].typ
	if typ == 0 {
		return raw, nil
	}
	var text string
	switch typ {
	case TypeUnicode:
		if len(raw)%2 != 0 {
			return nil, fmt.Errorf("ACT60 文本的 UTF-16 字节数必须为偶数")
		}
		text = stringFromUTF16(raw)
	case TypeScript:
		if len(raw)%5 != 0 {
			return nil, ErrInvalidSection
		}
		var err error
		text, err = a.CanonicalTextFromRaw(index, raw)
		if source, ok := a.act60Sources[a.FileIdentity(index)]; ok {
			text = source
		}
		if err != nil {
			return nil, err
		}
	default:
		return nil, ErrBadDataType
	}
	if typ == TypeScript {
		text = act60NativeSource(text)
	}
	if bytes.ContainsRune([]byte(text), 0) {
		return nil, fmt.Errorf("ACT60 文本不能包含 NUL 字符")
	}
	if old != nil {
		start := a.act60.body + int(old.offset)
		native := act60Decrypt(a.data[start:start+((int(old.size)+3)&^3)], old.crc)
		if bytes.HasPrefix(native, []byte{0xef, 0xbb, 0xbf}) {
			return append([]byte{0xef, 0xbb, 0xbf}, []byte(text)...), nil
		}
	}
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("ACT60 文本无法用 GBK 编码: %w", err)
	}
	return encoded, nil
}

func (a *Archive) saveACT60(w io.Writer, progress SaveProgressFunc) error {
	if !a.Modified() {
		return a.writeContainerWithProgress(w, a.data, progress)
	}
	act60 := a.act60
	var tree, body bytes.Buffer
	var nextNumber uint64
	for _, e := range act60.entries {
		if uint64(e.number) >= nextNumber {
			nextNumber = uint64(e.number) + 1
		}
	}
	reportSaveProgress(progress, "packing", 0, int64(len(a.items)), "files")
	for i, it := range a.items {
		var e act60Entry
		var old *act60Entry
		if it.chunk >= 0 {
			old = &act60.entries[it.chunk]
			e = *old
		}
		if old == nil {
			if nextNumber > math.MaxUint32 {
				return fmt.Errorf("ACT60 文件编号已用尽: %w", ErrOverflow)
			}
			e.number = uint32(nextNumber)
			nextNumber++
		}
		e.offset = uint32(body.Len())
		path := e.path
		if path == nil {
			var err error
			path, err = korean.EUCKR.NewEncoder().Bytes([]byte(a.Path(int32(i))))
			if err != nil {
				return fmt.Errorf("ACT60 文件路径无法用 CP949 编码: %w", err)
			}
		}
		raw, changed := a.overlay[int32(i)]
		var encrypted []byte
		if !changed && old != nil {
			start := act60.body + int(old.offset)
			encrypted = a.data[start : start+((int(old.size)+3)&^3)]
			// A1 authenticates the file offset, so a moved payload must be resealed.
			// Preserve the original alignment bytes, which also participate in CRC.
			if act60.legacy && e.offset != old.offset {
				plain := act60Decrypt(encrypted, old.crc)
				encrypted, e.crc = act60Encrypt(plain, e.offset)
			}
			expected := int32(0)
			if old.script {
				expected = TypeScript
			} else if old.text {
				expected = TypeUnicode
			}
			if it.typ != expected {
				return fmt.Errorf("ACT60 不支持直接改变现有文件的数据类型")
			}
		} else {
			var err error
			raw, err = a.encodeACT60Payload(int32(i), raw, old)
			if err != nil {
				return fmt.Errorf("%s: %w", a.Path(int32(i)), err)
			}
			if len(raw) > math.MaxInt32 {
				return ErrOverflow
			}
			e.size = uint32(len(raw))
			seed := e.number
			if act60.legacy {
				seed = e.offset
			}
			encrypted, e.crc = act60Encrypt(raw, seed)
		}
		if int64(body.Len())+int64(len(encrypted)) > math.MaxInt32 {
			return ErrOverflow
		}
		body.Write(encrypted)
		if act60.legacy {
			for _, v := range []uint32{e.number, e.offset, e.size, e.crc, uint32(len(path))} {
				binary.Write(&tree, binary.LittleEndian, v)
			}
			tree.Write(path)
		} else {
			binary.Write(&tree, binary.LittleEndian, e.number)
			binary.Write(&tree, binary.LittleEndian, uint32(len(path)))
			tree.Write(path)
			for _, v := range []uint32{e.size, e.crc, e.offset} {
				binary.Write(&tree, binary.LittleEndian, v)
			}
		}
		if tree.Len() > math.MaxInt32 {
			return ErrOverflow
		}
		reportSaveProgress(progress, "packing", int64(i+1), int64(len(a.items)), "files")
	}
	reportSaveProgress(progress, "metadata", 0, 0, "")
	encrypted, crc := act60Encrypt(tree.Bytes(), uint32(len(a.items)))
	var packed bytes.Buffer
	packed.Grow(len(act60.prefix) + 12 + len(encrypted) + body.Len())
	packed.Write(act60.prefix)
	for _, v := range []uint32{uint32(len(encrypted)), crc, uint32(len(a.items))} {
		binary.Write(&packed, binary.LittleEndian, v)
	}
	packed.Write(encrypted)
	packed.Write(body.Bytes())
	// Check both directory layouts and every payload before exposing output or
	// clearing edits. A failing writer must leave the in-memory archive intact.
	decoded, offset, count, legacy, ok := probeACT60(packed.Bytes())
	if !ok {
		return ErrInvalidSection
	}
	next, err := parseACT60(packed.Bytes(), decoded, offset, count, legacy)
	if err != nil {
		return err
	}
	for i, it := range a.items {
		expected := it.typ

		if next.items[i].typ != expected {
			return fmt.Errorf("ACT60 文件类型与载荷不匹配（entry=%d）", i)
		}
		next.items[i].identity = it.identity
	}
	if err := a.writeContainerWithProgress(w, packed.Bytes(), progress); err != nil {
		return err
	}
	a.data, a.hdr, a.act60 = next.data, next.hdr, next.act60
	a.contentDigest = ""
	a.items, a.groups = next.items, next.groups
	a.bodyOff = next.bodyOff
	a.strA, a.strW = next.strA, next.strW
	a.strAIdx, a.strWIdx = nil, nil
	a.pathIndex, a.compactPaths = next.pathIndex, next.compactPaths
	a.resolveCache = next.resolveCache
	a.resolveCacheOrder = nil
	a.resolveCacheBytes = 0
	a.overlay = next.overlay
	a.act60Sources = nil
	a.chunkCache, a.chunkCacheMeta = next.chunkCache, next.chunkCacheMeta
	a.chunkCacheBytes, a.chunkCacheClock = 0, 0
	a.poolsDirty, a.structuralDirty = false, false
	a.removedSpans = next.removedSpans
	return nil
}
