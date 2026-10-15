package pvf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"unicode/utf16"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	textunicode "golang.org/x/text/encoding/unicode"
)

// TextEncoding is the session override; auto follows the container convention.
func (a *Archive) TextEncoding() string {
	if a.textEncoding == "" {
		return "auto"
	}
	return a.textEncoding
}

func (a *Archive) TextEncodingLabel() string {
	if a.textEncoding != "" {
		if a.act60 == nil && a.tw == nil && a.textEncoding != "UTF-16LE" {
			if !a.hasUTF8PoolLocked() {
				return "UTF-16LE"
			}
			return a.textEncoding + " / UTF-16LE"
		}
		return a.textEncoding
	}
	switch a.Format() {
	case FormatACT60:
		return "GBK"
	case FormatTW:
		return "Big5"
	default:
		if !a.hasUTF8PoolLocked() {
			return "UTF-16LE"
		}
		return "UTF-8 / UTF-16LE"
	}
}

func textCodec(name string) (encoding.Encoding, error) {
	switch name {
	case "", "auto", "UTF-8":
		return nil, nil
	case "GBK":
		return simplifiedchinese.GBK, nil
	case "Big5":
		return traditionalchinese.Big5, nil
	case "CP949":
		return korean.EUCKR, nil
	case "UTF-16LE":
		return textunicode.UTF16(textunicode.LittleEndian, textunicode.IgnoreBOM), nil
	default:
		return nil, fmt.Errorf("不支持的文本编码：%s", name)
	}
}

func (a *Archive) nativeTextCodec() encoding.Encoding {
	name := a.textEncoding
	if name == "" {
		if a.tw != nil {
			name = "Big5"
		} else {
			name = "GBK"
		}
	}
	codec, _ := textCodec(name)
	return codec
}

func (a *Archive) decodeNativeText(raw []byte) string {
	// A BOM identifies a file-local UTF-8 exception regardless of the default.
	if bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return string(raw[3:])
	}
	if a.textEncoding == "UTF-16LE" {
		raw = bytes.TrimPrefix(raw, []byte{0xff, 0xfe})
	}
	if codec := a.nativeTextCodec(); codec != nil {
		decoded, _ := codec.NewDecoder().Bytes(raw)
		return string(decoded)
	}
	return string(raw)
}

func (a *Archive) encodeNativeText(text string) ([]byte, error) {
	if codec := a.nativeTextCodec(); codec != nil {
		return codec.NewEncoder().Bytes([]byte(text))
	}
	return []byte(text), nil
}

func (a *Archive) decodeNarrowText(raw []byte) string {
	// Narrow pools have byte-aligned NUL terminators; their layout cannot
	// contain UTF-16 code units. Unicode entries already use the wide pool.
	if a.textEncoding == "UTF-16LE" {
		return string(raw)
	}
	if codec, _ := textCodec(a.textEncoding); codec != nil {
		decoded, _ := codec.NewDecoder().Bytes(raw)
		return string(decoded)
	}
	return string(raw)
}

func (a *Archive) nativePayloadIsText(raw []byte) bool {
	if a.textEncoding != "UTF-16LE" || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return !bytes.ContainsRune(raw, 0)
	}
	raw = bytes.TrimPrefix(raw, []byte{0xff, 0xfe})
	if len(raw)%2 != 0 {
		return false
	}
	for i := 0; i < len(raw); i += 2 {
		u := binary.LittleEndian.Uint16(raw[i:])
		if u < 0x20 && u != '\t' && u != '\r' && u != '\n' {
			return false
		}
		if utf16.IsSurrogate(rune(u)) {
			if u < 0xd800 || u > 0xdbff || i+3 >= len(raw) {
				return false
			}
			low := binary.LittleEndian.Uint16(raw[i+2:])
			if low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 2
		}
	}
	return true
}

// WithTextEncoding builds an isolated interpretation of the current source.
// Pending edits must be saved first; switching never transcodes untouched bytes.
func (a *Archive) WithTextEncoding(name string) (*Archive, error) {
	if _, err := textCodec(name); err != nil {
		return nil, err
	}
	if a.Modified() {
		return nil, fmt.Errorf("请先保存归档修改，再切换编码")
	}
	if name == "auto" {
		name = ""
	}
	var next *Archive
	var err error
	switch {
	case a.act60 != nil:
		tree, body, count, legacy, ok := probeACT60(a.data)
		if !ok {
			return nil, ErrInvalidSection
		}
		next, err = parseACT60(a.data, tree, body, count, legacy, name)
	case a.tw != nil:
		next, err = parseTW(a.data, name)
	default:
		next = a.CloneForBatch()
		next.textEncoding = name
		next.resolveCache = make(map[int32]string)
		next.resolveCacheOrder = nil
		next.resolveCacheBytes = 0
		next.strAIdx, next.strWIdx = nil, nil
		next.rebuildPathIndex()
	}
	if err != nil {
		return nil, err
	}
	next.textEncoding = name
	next.sourcePath = a.sourcePath
	next.scriptRenderer = a.scriptRenderer
	next.canonicalScriptRenderer = a.canonicalScriptRenderer
	for i := range next.items {
		next.items[i].identity = a.items[i].identity
	}
	next.ClearMutations()
	return next, nil
}
