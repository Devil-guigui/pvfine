package pvf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math/bits"

	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const act60Signature = "DNF_SCRIPT_PACK"

// ACT60 archives contain independent ANSI source files and, in later revisions,
// native binary resources. They have neither token pools nor stringtable.bin.
// Keep this transport separate from TW, whose similar directory encrypts
// token scripts and uses a different XOR constant.
type act60Entry struct {
	number, offset, size, crc uint32
	text                      bool
	script                    bool
	tokens                    []byte
	path                      []byte
}

type act60Archive struct {
	directory []byte // retains original native path bytes
	prefix    []byte // original signature or UUID/version bytes
	legacy    bool
	body      int
	entries   []act60Entry
}

func act60Decrypt(data []byte, crc uint32) []byte {
	out := bytes.Clone(data)
	for i := 0; i+4 <= len(out); i += 4 {
		v := binary.LittleEndian.Uint32(out[i:]) ^ crc
		binary.LittleEndian.PutUint32(out[i:], bits.RotateLeft32(v, -6))
	}
	return out
}

// probeACT uses the complete directory checksum, rather than a version number
// or UUID shared with other containers, to distinguish modern ACT60 from TW.
func probeACT60(data []byte) ([]byte, int, uint32, bool, bool) {
	legacy := bytes.HasPrefix(data, []byte(act60Signature))
	header := len(act60Signature)
	if !legacy {
		if !isTW(data) {
			return nil, 0, 0, false, false
		}
		header = 4 + int(binary.LittleEndian.Uint32(data)) + 4
	}
	if len(data)-header < 12 {
		return nil, 0, 0, legacy, false
	}
	h := data[header:]
	size := uint64(binary.LittleEndian.Uint32(h))
	crc := binary.LittleEndian.Uint32(h[4:])
	count := binary.LittleEndian.Uint32(h[8:])
	start := header + 12
	if size%4 != 0 || size > uint64(len(data)-start) || uint64(count) > size/20 {
		return nil, 0, 0, legacy, false
	}
	tree := act60Decrypt(data[start:start+int(size)], crc)
	if crc32.Update(count, crc32.IEEETable, tree) != crc {
		return nil, 0, 0, legacy, false
	}
	return tree, start + int(size), count, legacy, true
}

func parseACT60(data, tree []byte, body int, count uint32, legacy bool, codecs ...string) (*Archive, error) {
	a := New()
	a.data = data
	a.format = act60Profile
	if len(codecs) > 0 {
		a.textEncoding = codecs[0]
	}
	act60 := &act60Archive{directory: tree, prefix: data[:body-len(tree)-12], legacy: legacy, body: body, entries: make([]act60Entry, 0, count)}
	a.act60 = act60
	a.bodyOff = body
	a.items = make([]fileItem, 0, count)
	a.groups = make([]groupItem, 0, count)
	pos := 0
	for i := uint32(0); i < count; i++ {
		if len(tree)-pos < 20 {
			return nil, ErrTruncated
		}
		var e act60Entry
		var path []byte
		e.number = binary.LittleEndian.Uint32(tree[pos:])
		if legacy {
			e.offset = binary.LittleEndian.Uint32(tree[pos+4:])
			e.size = binary.LittleEndian.Uint32(tree[pos+8:])
			e.crc = binary.LittleEndian.Uint32(tree[pos+12:])
			n := uint64(binary.LittleEndian.Uint32(tree[pos+16:]))
			if n == 0 || n > uint64(len(tree)-pos-20) {
				return nil, ErrInvalidSection
			}
			path = tree[pos+20 : pos+20+int(n)]
			pos += 20 + int(n)
		} else {
			n := uint64(binary.LittleEndian.Uint32(tree[pos+4:]))
			if n == 0 || n > uint64(len(tree)-pos-20) {
				return nil, ErrInvalidSection
			}
			path = tree[pos+8 : pos+8+int(n)]
			base := pos + 8 + int(n)
			e.size = binary.LittleEndian.Uint32(tree[base:])
			e.crc = binary.LittleEndian.Uint32(tree[base+4:])
			e.offset = binary.LittleEndian.Uint32(tree[base+8:])
			pos += 20 + int(n)
		}
		e.path = path
		decoded, _ := korean.EUCKR.NewDecoder().Bytes(path)
		p := normalizePath(string(decoded))
		dir, name := splitDirName(p)
		if _, exists := a.pathIndex[p]; exists || name == "" || bytes.ContainsRune(path, 0) {
			return nil, ErrInvalidSection
		}
		act60.entries = append(act60.entries, e)
		raw, err := act60.payload(data, int32(i))
		if err != nil {
			return nil, err
		}
		e.text = a.nativePayloadIsText(raw)
		act60.entries[i].text = e.text
		typ := int32(0) // Native binary ANI and other resources are retained verbatim.
		size := len(raw)
		if e.text {
			typ = TypeUnicode
			source := a.decodeNativeText(raw)
			size = len(utf16le(source))
			if isACT60Script(p, source) {
				tokens, err := a.encodeACT60Script(source)
				if err != nil {
					return nil, fmt.Errorf("ACT60 script entry=%d: %w", i, err)
				}
				typ, size = TypeScript, len(tokens)
				act60.entries[i].script = true
				act60.entries[i].tokens = tokens
			}
		}
		a.items = append(a.items, fileItem{identity: nextFileIdentity.Add(1), nameOff: a.StringOffset(name), pathOff: a.StringOffset(dir), chunk: int32(i), size: int32(size), typ: typ})
		a.groups = append(a.groups, groupItem{origSize: int32(size)})
		a.pathIndex[p] = int32(i)
	}
	if len(tree)-pos > 3 {
		return nil, ErrInvalidSection
	}
	a.hdr = Header{FileCount: int32(count), BodySize: int32(len(data) - body), GroupCount: int32(count)}
	a.poolsDirty = false
	return a, nil
}

func (act60 *act60Archive) payload(data []byte, index int32) ([]byte, error) {
	if index < 0 || int(index) >= len(act60.entries) {
		return nil, ErrBadIndex
	}
	e := act60.entries[index]
	start := uint64(act60.body) + uint64(e.offset)
	end := start + ((uint64(e.size) + 3) &^ uint64(3))
	if end > uint64(len(data)) {
		return nil, ErrOverflow
	}
	raw := act60Decrypt(data[start:end], e.crc)
	seed := e.number
	if act60.legacy {
		seed = e.offset
	}
	if e.size > 0 && crc32.Update(seed, crc32.IEEETable, raw) != e.crc {
		return nil, fmt.Errorf("%w: ACT60 file checksum (entry=%d)", ErrInvalidSection, index)
	}
	return raw[:e.size], nil
}

// ACT60 fixtures are CN clients. Language suffixes are not reliable encoding
// identifiers: translated CN files commonly retain their original .kor.str name.
func act60DecodeText(raw []byte) string {
	if bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return string(raw[3:])
	}
	decoded, _ := simplifiedchinese.GBK.NewDecoder().Bytes(raw)
	return string(decoded)
}

func (a *Archive) act60Chunk(index int32) ([]byte, error) {
	if index < 0 || int(index) >= len(a.act60.entries) {
		return nil, ErrBadIndex
	}
	if a.act60.entries[index].script {
		return a.act60.entries[index].tokens, nil
	}
	raw, err := a.act60.payload(a.data, index)
	if err != nil {
		return nil, err
	}
	if a.act60.entries[index].text {
		return utf16le(a.decodeNativeText(raw)), nil
	}
	return raw, nil
}
