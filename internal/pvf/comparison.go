package pvf

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"reflect"
	"unicode/utf16"
)

// ComparisonChunk owns one decompressed chunk for a synchronous comparison
// batch. Bytes may be read concurrently, but must be released before the batch
// callback returns. The archive must remain immutable throughout the scan.
type ComparisonChunk struct {
	archive     *Archive
	raw         []byte
	indexes     []int32
	independent bool // TW payloads are independent files, not shared zlib chunks.
}

func (c ComparisonChunk) Indexes() []int32 { return c.indexes }
func (c ComparisonChunk) Bytes(index int32) ([]byte, error) {
	a := c.archive
	if index < 0 || index >= a.FileCount() {
		return nil, ErrBadIndex
	}
	if raw, ok := a.overlay[index]; ok {
		return raw, nil
	}
	item := a.items[index]
	if c.independent {
		return a.RawBytes(index)
	}
	if item.off < 0 || item.size <= 0 || int64(item.off)+int64(item.size) > int64(len(c.raw)) {
		return nil, nil
	}
	return c.raw[item.off : item.off+item.size], nil
}

// ForEachComparisonChunk decodes each selected zlib chunk once, bypassing the
// interactive LRU even for chunks larger than its cache limit. Empty selection
// does no IO. The callback must join its workers before returning.
func (a *Archive) ForEachComparisonChunk(ctx context.Context, indexes []int32, fn func(ComparisonChunk) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.tw != nil {
		for _, index := range indexes {
			if index < 0 || index >= a.FileCount() {
				return ErrBadIndex
			}
		}
		if len(indexes) == 0 {
			return nil
		}
		return fn(ComparisonChunk{archive: a, indexes: indexes, independent: true})
	}
	groups := make([][]int32, len(a.groups)+1)
	for _, index := range indexes {
		if index < 0 || index >= a.FileCount() {
			return ErrBadIndex
		}
		item := a.items[index]
		bucket := len(a.groups)
		if item.chunk >= 0 && int(item.chunk) < len(a.groups) {
			bucket = int(item.chunk)
		}
		groups[bucket] = append(groups[bucket], index)
	}
	for ci, selected := range groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(selected) == 0 {
			continue
		}
		chunk := ComparisonChunk{archive: a, indexes: selected}
		if ci < len(a.groups) {
			for _, index := range selected {
				if _, edited := a.overlay[index]; !edited && a.items[index].size > 0 {
					var err error
					chunk.raw, err = a.decompressChunk(int32(ci))
					if err != nil {
						return err
					}
					break
				}
			}
		}
		if err := fn(chunk); err != nil {
			return err
		}
		chunk.raw = nil
	}
	return ctx.Err()
}

// FileFingerprint avoids rendering text for ordinary scripts. NeedsText marks
// malformed/unknown tokens whose equality must be checked by the canonical
// renderer. Digest hashes token types, numeric bits and actual pool strings.
type FileFingerprint struct {
	Digest    [32]byte
	NeedsText bool
}

// FileComparisonReader is private to one worker. Its bounded string cache
// reads immutable pools directly, avoiding the archive's shared cache mutex.
type FileComparisonReader struct {
	archive    *Archive
	cache      map[int32]string
	cacheBytes int
	hash       hash.Hash
	buffer     []byte
}

func (a *Archive) NewFileComparisonReader() *FileComparisonReader {
	return &FileComparisonReader{archive: a, cache: make(map[int32]string), hash: sha256.New(), buffer: make([]byte, 0, 64<<10)}
}
func (r *FileComparisonReader) resolve(offset int32) string {
	if offset < 0 {
		return ""
	}
	if value, ok := r.cache[offset]; ok {
		return value
	}
	var value string
	if offset&1 != 0 {
		value = readUTF16(r.archive.strW, int(offset>>1)*2)
	} else {
		value = readUTF8(r.archive.strA, int(offset>>1))
	}
	const budget = 1 << 20
	size := len(value) + 32
	if size <= budget {
		if r.cacheBytes+size > budget {
			clear(r.cache)
			r.cacheBytes = 0
		}
		r.cache[offset] = value
		r.cacheBytes += size
	}
	return value
}
func (r *FileComparisonReader) write(data []byte) {
	for len(data) > 0 {
		available := cap(r.buffer) - len(r.buffer)
		count := min(available, len(data))
		r.buffer = append(r.buffer, data[:count]...)
		data = data[count:]
		if len(r.buffer) == cap(r.buffer) {
			_, _ = r.hash.Write(r.buffer)
			r.buffer = r.buffer[:0]
		}
	}
}

// WriteString appends directly to the bounded hash buffer without converting
// each rendered value to a separately allocated byte slice.
func (r *FileComparisonReader) WriteString(value string) (int, error) {
	length := len(value)
	for len(value) > 0 {
		count := min(cap(r.buffer)-len(r.buffer), len(value))
		r.buffer = append(r.buffer, value[:count]...)
		value = value[count:]
		if len(r.buffer) == cap(r.buffer) {
			_, _ = r.hash.Write(r.buffer)
			r.buffer = r.buffer[:0]
		}
	}
	return length, nil
}
func (r *FileComparisonReader) WriteByte(value byte) error {
	var data [1]byte
	data[0] = value
	r.write(data[:])
	return nil
}
func (r *FileComparisonReader) Fingerprint(ctx context.Context, dataType int32, raw []byte) (FileFingerprint, error) {
	r.hash.Reset()
	r.buffer = r.buffer[:0]
	var prefix [5]byte
	binary.LittleEndian.PutUint32(prefix[:4], uint32(dataType))
	r.write(prefix[:4])
	value := FileFingerprint{}
	if dataType != TypeScript {
		r.write(raw)
	} else if len(raw)%5 != 0 {
		value.NeedsText = true
		r.write(raw)
	} else {
		for pos := 0; pos < len(raw); pos += 5 {
			if pos%(5*4096) == 0 {
				if err := ctx.Err(); err != nil {
					return FileFingerprint{}, err
				}
			}
			typ := raw[pos]
			switch typ {
			case 3, 5, 6, 7, 8, 10:
				text := r.resolve(int32(binary.LittleEndian.Uint32(raw[pos+1:])))
				prefix[0] = typ
				binary.LittleEndian.PutUint32(prefix[1:], uint32(len(text)))
				r.write(prefix[:])
				_, _ = r.WriteString(text)
			case 0, 2:
				r.write(raw[pos : pos+5])
			default:
				value.NeedsText = true
				r.write(raw[pos : pos+5])
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return FileFingerprint{}, err
	}
	_, _ = r.hash.Write(r.buffer)
	r.hash.Sum(value.Digest[:0])
	return value, nil
}

// TokenComparisonCompatible prevents an equality shortcut when canonical
// rendering depends on different client versions or rendering contexts.
func (a *Archive) TokenComparisonCompatible(other *Archive) bool {
	return a.ClientVersion() == other.ClientVersion() && reflect.DeepEqual(a.canonicalScriptRenderer, other.canonicalScriptRenderer)
}

// CanonicalTextFromRaw reuses an already decoded batch payload. It has exactly
// the same semantics as CanonicalText, without reading/decompressing it again.
func (a *Archive) CanonicalTextFromRaw(index int32, raw []byte) (string, error) {
	return a.canonicalTextFromRaw(index, raw, a.ResolveString)
}

func (a *Archive) canonicalTextFromRaw(index int32, raw []byte, resolve func(int32) string) (string, error) {
	if index < 0 || index >= a.FileCount() {
		return "", ErrBadIndex
	}
	if len(raw) == 0 {
		return "", nil
	}
	switch a.items[index].typ {
	case TypeUnicode:
		return fixKoreanMojibake(string(utf16.Decode(u16le(raw)))), nil
	case TypeScript:
		return a.decodeScriptForPathWithResolver(raw, a.Path(index), a.canonicalScriptRenderer, resolve), nil
	default:
		return "", nil
	}
}

// CanonicalFingerprint is the fallback and preserves version-compatible hashes.
func (r *FileComparisonReader) CanonicalFingerprint(ctx context.Context, index int32, raw []byte) (FileFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return FileFingerprint{}, err
	}
	if index < 0 || index >= r.archive.FileCount() {
		return FileFingerprint{}, ErrBadIndex
	}
	r.hash.Reset()
	r.buffer = r.buffer[:0]
	var prefix [4]byte
	binary.LittleEndian.PutUint32(prefix[:], uint32(r.archive.items[index].typ))
	r.write(prefix[:])
	if len(raw) > 0 && r.archive.items[index].typ == TypeScript {
		if err := r.archive.writeScriptForPath(ctx, raw, r.archive.Path(index), r.archive.canonicalScriptRenderer, r.resolve, r); err != nil {
			return FileFingerprint{}, err
		}
	} else {
		text, err := r.archive.canonicalTextFromRaw(index, raw, r.resolve)
		if err != nil {
			return FileFingerprint{}, err
		}
		_, _ = r.WriteString(text)
	}
	if err := ctx.Err(); err != nil {
		return FileFingerprint{}, err
	}
	_, _ = r.hash.Write(r.buffer)
	result := FileFingerprint{}
	r.hash.Sum(result.Digest[:0])
	return result, nil
}

// FileContentInfo reads only immutable payload metadata, without resolving
// file names through the shared string cache on every worker task.
func (a *Archive) FileContentInfo(index int32) (dataType, size int32, err error) {
	if index < 0 || index >= a.FileCount() {
		return 0, 0, ErrBadIndex
	}
	item := a.items[index]
	size = item.size
	if raw, ok := a.overlay[index]; ok {
		size = int32(len(raw))
	}
	return item.typ, size, nil
}
