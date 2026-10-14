package pvf

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrCancelled is returned by ExtractTo when the cancel callback fires.
var ErrCancelled = errors.New("pvf: unpack cancelled")

// ErrPagedCapacity means the edited archive exceeds its client's sealed table.
var ErrPagedCapacity = errors.New("pvf: 编辑后归档超出配套页表容量，无法保存；请减少新增内容或使用容量更大的对应客户端配套数据")

// SaveAs writes the archive (with pending edits applied) to path.
// The write is atomic: data lands in a temp file that is renamed over path,
// so a failed write never destroys an existing archive.
func (a *Archive) SaveAs(path string) error {
	return a.SaveAsWithProgress(path, nil)
}

// SaveProgress reports completed work in the current phase, not an estimated
// percentage of the entire save. Total is zero for indeterminate phases.
type SaveProgress struct {
	Phase string `json:"phase"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Unit  string `json:"unit"`
}

type SaveProgressFunc func(SaveProgress)

func reportSaveProgress(progress SaveProgressFunc, phase string, done, total int64, unit string) {
	if progress != nil {
		progress(SaveProgress{Phase: phase, Done: done, Total: total, Unit: unit})
	}
}

// SaveAsWithProgress preserves the atomic save behavior and reports completion
// only after the temporary file has been synced and renamed successfully.
func (a *Archive) SaveAsWithProgress(path string, progress SaveProgressFunc) error {
	tmp := path + ".pvftmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	err = a.SaveToWithProgress(bw, progress)
	if err == nil {
		reportSaveProgress(progress, "syncing", 0, 0, "")
		err = bw.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	reportSaveProgress(progress, "replacing", 0, 0, "")
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if a.sourcePath == "" {
		a.sourcePath = path
	}
	reportSaveProgress(progress, "done", 1, 1, "")
	return nil
}

// Save writes the archive back to the file it was opened from.
func (a *Archive) Save() error {
	return a.SaveWithProgress(nil)
}

func (a *Archive) SaveWithProgress(progress SaveProgressFunc) error {
	if a.sourcePath == "" {
		return errors.New("pvf: archive has no source path")
	}
	return a.SaveAsWithProgress(a.sourcePath, progress)
}

// SourcePath returns the file the archive was opened from ("" for archives
// built from scratch that have not been saved yet).
func (a *Archive) SourcePath() string { return a.sourcePath }

// ModifiedCount returns a non-zero modification count for payload, string-pool,
// and structural edits. Structural edits do not belong to a single entry, so
// they contribute one indicator entry when no payload overlay exists.
func (a *Archive) ModifiedCount() int {
	if len(a.overlay) > 0 {
		return len(a.overlay)
	}
	if a.structuralDirty || a.poolsDirty {
		return 1
	}
	return 0
}

// ExtractTo unpacks every entry under dir, recreating the internal directory
// structure. Characters illegal in file names are replaced; the destination
// is never escaped. progress (optional) receives (done, total) periodically;
// cancel (optional) aborts with ErrCancelled.
func (a *Archive) ExtractTo(dir string, progress func(done, total int), cancel func() bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	total := len(a.items)
	createdDirs := map[string]bool{}
	for i := range a.items {
		if cancel != nil && cancel() {
			return ErrCancelled
		}
		p := a.Path(int32(i))
		if p == "" {
			continue
		}
		dst, err := safeJoin(dir, p)
		if err != nil {
			continue // skip entries that would escape the destination
		}
		if d := filepath.Dir(dst); !createdDirs[d] {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			createdDirs[d] = true
		}
		raw, err := a.RawBytes(int32(i))
		if err == nil && len(raw) > 0 {
			if err := os.WriteFile(dst, raw, 0o644); err != nil {
				return err
			}
		}
		if progress != nil && (i%500 == 0 || i == total-1) {
			progress(i+1, total)
		}
	}
	return nil
}

// safeJoin joins an internal slash path onto base, rejecting traversal and
// sanitizing characters that are illegal in file names.
func safeJoin(base, internal string) (string, error) {
	parts := strings.Split(internal, "/")
	var cleaned []string
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", fmt.Errorf("pvf: path %q escapes destination", internal)
		}
		cleaned = append(cleaned, sanitizeName(part))
	}
	if len(cleaned) == 0 {
		return "", fmt.Errorf("pvf: empty path")
	}
	return filepath.Join(append([]string{base}, cleaned...)...), nil
}

func sanitizeName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7F:
			return '_'
		case strings.ContainsRune(`<>:"|?*`, r):
			return '_'
		}
		return r
	}, name)
}

// SaveTo writes the archive to w. Unmodified archives are reproduced
// byte-identical; edited ones rebuild only the touched chunks plus derived
// sections. A Paged110 container gets its page guards re-encrypted on the way
// out, so the result is a file the client accepts.
func (a *Archive) SaveTo(w io.Writer) error {
	return a.SaveToWithProgress(w, nil)
}

// SaveToWithProgress reports reconstruction and output work. File durability
// is reported separately by SaveAsWithProgress after this method returns.
func (a *Archive) SaveToWithProgress(w io.Writer, progress SaveProgressFunc) error {
	reportSaveProgress(progress, "preparing", 0, 0, "")
	if a.tw != nil {
		return a.saveTWWithProgress(w, progress)
	}
	if err := a.validateWrite(); err != nil {
		return err
	}
	// Repair can create structural edits; reject before it mutates the archive
	// when the original HASH cannot be rebuilt.
	if len(a.strA) > 0 && a.ContentRules().UTF16Only && !a.canRebuildHash() {
		return a.structureLockedError()
	}
	if err := a.normalizeStringPools(); err != nil {
		return err
	}
	if err := a.validateWrite(); err != nil {
		return err
	}
	logical := a.data
	rebuilt := a.Modified() || logical == nil || int32(len(a.items)) != a.hdr.FileCount
	if rebuilt {
		out, err := a.rebuildWithProgress(progress)
		if err != nil {
			return err
		}
		logical = out
	}
	if err := a.writeContainerWithProgress(w, logical, progress); err != nil {
		return err
	}
	if rebuilt {
		a.adoptRebuilt(logical)
	}
	return nil
}

// writeContainer owns only the physical wrapping of logical archive bytes.
// Shared reconstruction and write validation stay in SaveTo.
func (a *Archive) writeContainer(w io.Writer, logical []byte) error {
	return a.writeContainerWithProgress(w, logical, nil)
}

func (a *Archive) writeContainerWithProgress(w io.Writer, logical []byte, progress SaveProgressFunc) error {
	if progress != nil {
		reportSaveProgress(progress, "writing", 0, int64(len(logical)), "bytes")
		w = &saveProgressWriter{writer: w, total: int64(len(logical)), progress: progress}
	}
	if a.format.container == pagedContainer {
		return writePageGuarded(w, logical, a.pageKeys)
	}
	n, err := w.Write(logical)
	if err == nil && n != len(logical) {
		return io.ErrShortWrite
	}
	return err
}

// Split large writes so plain containers also produce incremental byte counts.
type saveProgressWriter struct {
	writer      io.Writer
	done, total int64
	progress    SaveProgressFunc
}

func (w *saveProgressWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		size := min(len(p), 1<<20)
		n, err := w.writer.Write(p[:size])
		written += n
		w.done += int64(n)
		reportSaveProgress(w.progress, "writing", w.done, w.total, "bytes")
		if err != nil {
			return written, err
		}
		if n != size {
			return written, io.ErrShortWrite
		}
		p = p[size:]
	}
	return written, nil
}

type chunkOut struct {
	raw      []byte // untouched: still-encrypted slice to copy verbatim
	enc      []byte // rebuilt: encrypted+compressed replacement (nil when raw)
	origSize int32
	compSize int32 // cumulative, as stored in GRPI
}

// rebuild produces the complete archive bytes with edits applied.
func (a *Archive) rebuild() (result []byte, err error) {
	return a.rebuildWithProgress(nil)
}

func (a *Archive) rebuildWithProgress(progress SaveProgressFunc) (result []byte, err error) {
	// Classify edits.
	modifiedChunks := map[int32]bool{}
	var newFiles []int32
	for i := range a.items {
		it := &a.items[i]
		if it.chunk < 0 || it.chunk >= int32(len(a.groups)) {
			newFiles = append(newFiles, int32(i))
			continue
		}
		if _, ok := a.overlay[int32(i)]; ok {
			modifiedChunks[it.chunk] = true
		}
	}
	for chunk := range a.removedSpans {
		modifiedChunks[chunk] = true
	}
	// Rebuilding adjusts offsets in touched chunks before capacity is known.
	// Restore their original spans on failure so pending edits can be retried.
	originalItems := make(map[int]fileItem)
	for i, item := range a.items {
		if item.chunk < 0 || item.chunk >= int32(len(a.groups)) || modifiedChunks[item.chunk] {
			originalItems[i] = item
		}
	}
	defer func() {
		if err != nil {
			for i, item := range originalItems {
				a.items[i] = item
			}
		}
	}()

	// Pass 1: per-chunk output. Untouched chunks are copied as raw encrypted
	// bytes; modified chunks are rebuilt, re-compressed and re-encrypted.
	outs := make([]chunkOut, 0, len(a.groups)+1)
	totalChunks := int64(len(a.groups))
	if len(newFiles) > 0 {
		totalChunks++
	}
	reportSaveProgress(progress, "packing", 0, totalChunks, "chunks")
	var cumulative int32
	for ci := int32(0); ci < int32(len(a.groups)); ci++ {
		if !modifiedChunks[ci] {
			raw, ok := a.chunkSpan(ci)
			if !ok {
				return nil, fmt.Errorf("pvf: chunk %d out of bounds in source data", ci)
			}
			cumulative += int32(len(raw))
			outs = append(outs, chunkOut{raw: raw, origSize: a.groups[ci].origSize, compSize: cumulative})
			reportSaveProgress(progress, "packing", int64(ci)+1, totalChunks, "chunks")
			continue
		}
		rebuilt, err := a.rebuildChunk(ci)
		if err != nil {
			return nil, err
		}
		enc, err := zlibCompress(rebuilt)
		if err != nil {
			return nil, err
		}
		cryptSeed(a.keys.body.seed, a.keys.body.magic, enc)
		cumulative += int32(len(enc))
		outs = append(outs, chunkOut{enc: enc, origSize: int32(len(rebuilt)), compSize: cumulative})
		reportSaveProgress(progress, "packing", int64(ci)+1, totalChunks, "chunks")
	}

	// New files land in one appended chunk.
	if len(newFiles) > 0 {
		var nb bytes.Buffer
		newChunk := int32(len(outs))
		for _, i := range newFiles {
			it := &a.items[i]
			data := a.overlay[i]
			it.chunk = newChunk
			it.off = int32(nb.Len())
			it.size = int32(len(data))
			nb.Write(data)
		}
		enc, err := zlibCompress(nb.Bytes())
		if err != nil {
			return nil, err
		}
		cryptSeed(a.keys.body.seed, a.keys.body.magic, enc)
		cumulative += int32(len(enc))
		outs = append(outs, chunkOut{enc: enc, origSize: int32(nb.Len()), compSize: cumulative})
		reportSaveProgress(progress, "packing", totalChunks, totalChunks, "chunks")
	}

	reportSaveProgress(progress, "metadata", 0, 0, "")

	// File table (plaintext).
	table := make([]byte, len(a.items)*0x18)
	for i := range a.items {
		item := a.items[i]
		if payload, ok := a.overlay[int32(i)]; ok {
			item.size = int32(len(payload))
		}
		marshalItem(table[i*0x18:], &item)
	}

	// Hash section.
	//
	// It is regenerated whenever its seed is known (the standard key set, the
	// alternate variant's "hash" key, or a seed solved from the section itself),
	// so a rebuild indexes the current file list — including added or removed
	// files. Only an archive whose seed could not be established keeps its
	// original bytes, which is the best a rebuild can do there.
	var hashBytes []byte
	if a.canRebuildHash() {
		hashBytes = a.buildHashTable()
	} else {
		hashBytes = a.data[a.hashOff : a.hashOff+a.hashSize]
	}

	// Name table: rebuild only when pools changed.
	var nameBytes []byte
	if a.poolsDirty || a.data == nil {
		nb, err := a.buildNameTable()
		if err != nil {
			return nil, err
		}
		nameBytes = nb
	} else {
		nameBytes = a.data[a.nameOff : a.nameOff+a.nameSize]
	}

	// GRPI section.
	grpi := make([]byte, len(outs)*8)
	for i := range outs {
		binary.LittleEndian.PutUint32(grpi[i*8:], uint32(outs[i].compSize))
		binary.LittleEndian.PutUint32(grpi[i*8+4:], uint32(outs[i].origSize))
	}
	cryptSeed(a.keys.grpi.seed, a.keys.grpi.magic, grpi)

	// Recompression can grow the name pools enough to cross the sealed table's
	// page limit. Try denser pools before rejecting, without rewriting body
	// chunks that were not edited. Check before changing the header or writing.
	if a.IsPaged() {
		baseSize := int64(headerSize) + int64(len(table)) + int64(len(hashBytes)) + int64(len(grpi)) + int64(cumulative)
		if err := validatePagedCapacity(baseSize+int64(len(nameBytes)), a.pageKeys); err != nil {
			denseNames, compressErr := a.buildNameTableLevel(zlib.BestCompression)
			if compressErr != nil {
				return nil, compressErr
			}
			if len(denseNames) < len(nameBytes) {
				nameBytes = denseNames
			}
			if err := validatePagedCapacity(baseSize+int64(len(nameBytes)), a.pageKeys); err != nil {
				return nil, err
			}
		}
	}

	// Header.
	a.hdr.FileCount = int32(len(a.items))
	a.hdr.BodySize = cumulative
	a.hdr.GroupCount = int32(len(outs))
	a.hdr.HashTableSize = int32(len(hashBytes))
	a.hdr.NameTableSize = int32(len(nameBytes))
	hdr := encodeHeader(a.hdr)
	cryptSeed(a.keys.header.seed, a.keys.header.magic, hdr)
	if a.guard {
		applyGuard(hdr)
	}

	// Assemble.
	var buf bytes.Buffer
	buf.Grow(headerSize + len(table) + len(hashBytes) + len(nameBytes) + len(grpi) + int(cumulative))
	buf.Write(hdr)
	buf.Write(table)
	buf.Write(hashBytes)
	buf.Write(nameBytes)
	buf.Write(grpi)
	for i := range outs {
		if outs[i].enc != nil {
			buf.Write(outs[i].enc)
		} else {
			buf.Write(outs[i].raw)
		}
	}
	return buf.Bytes(), nil
}

// rebuildChunk returns chunk ci with overlay payloads spliced in, ported from
// the reference implementation: segments are written in offset order, gaps
// are copied from the original chunk except ranges belonging to deleted files.
func (a *Archive) rebuildChunk(ci int32) ([]byte, error) {
	orig, err := a.Chunk(ci)
	if err != nil {
		return nil, err
	}
	type seg struct {
		off, size, idx int32
		hasNew         bool
	}
	var segs []seg
	for i := range a.items {
		it := &a.items[i]
		if it.chunk != ci {
			continue
		}
		_, hasNew := a.overlay[int32(i)]
		if it.size <= 0 && !hasNew {
			continue
		}
		segs = append(segs, seg{off: it.off, size: it.size, idx: int32(i), hasNew: hasNew})
	}
	sort.Slice(segs, func(x, y int) bool { return segs[x].off < segs[y].off })
	removed := append([]removedFileSpan(nil), a.removedSpans[ci]...)
	sort.Slice(removed, func(x, y int) bool { return removed[x].off < removed[y].off })

	var buf bytes.Buffer
	var srcPos int32
	for _, s := range segs {
		writeOriginalRange(&buf, orig, srcPos, s.off, removed)
		it := &a.items[s.idx]
		it.off = int32(buf.Len())
		if s.hasNew {
			d := a.overlay[s.idx]
			buf.Write(d)
			it.size = int32(len(d))
		} else if orig != nil && int64(s.off)+int64(s.size) <= int64(len(orig)) {
			buf.Write(orig[s.off : s.off+s.size])
		}
		srcPos = s.off + s.size
	}
	writeOriginalRange(&buf, orig, srcPos, int32(len(orig)), removed)
	return buf.Bytes(), nil
}

func writeOriginalRange(buf *bytes.Buffer, orig []byte, start, end int32, removed []removedFileSpan) {
	if len(orig) == 0 || end <= start {
		return
	}
	if start < 0 {
		start = 0
	}
	if end > int32(len(orig)) {
		end = int32(len(orig))
	}
	if end <= start {
		return
	}

	pos := start
	for _, span := range removed {
		spanStart := span.off
		spanEnd := span.off + span.size
		if spanEnd <= pos {
			continue
		}
		if spanStart >= end {
			break
		}
		if spanStart > pos {
			from, to := pos, minInt32(spanStart, end)
			buf.Write(orig[from:to])
		}
		if spanEnd > pos {
			pos = spanEnd
		}
		if pos >= end {
			return
		}
	}
	if pos < end {
		buf.Write(orig[pos:end])
	}
}

func minInt32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

// adoptRebuilt refreshes in-memory state so the archive keeps working after a
// save: sections move, chunks re-derive from the new layout.
func (a *Archive) adoptRebuilt(out []byte) {
	a.contentDigest = ""
	a.data = out
	pos := headerSize
	a.tableOff = pos
	pos += int(a.hdr.FileCount) * 0x18
	a.hashOff = pos
	a.hashSize = int(a.hdr.HashTableSize)
	pos += a.hashSize
	a.nameOff = pos
	a.nameSize = int(a.hdr.NameTableSize)
	pos += a.nameSize
	a.grpiOff = pos
	a.grpiSize = int(a.hdr.GroupCount) * 8
	pos += a.grpiSize
	a.bodyOff = pos

	a.groups = make([]groupItem, a.hdr.GroupCount)
	grpi := make([]byte, a.grpiSize)
	copy(grpi, out[a.grpiOff:a.grpiOff+a.grpiSize])
	cryptSeed(a.keys.grpi.seed, a.keys.grpi.magic, grpi)
	for i := range a.groups {
		a.groups[i].compSize = int32(binary.LittleEndian.Uint32(grpi[i*8:]))
		a.groups[i].origSize = int32(binary.LittleEndian.Uint32(grpi[i*8+4:]))
	}

	a.chunkCache = map[int32][]byte{}
	a.chunkCacheMeta = map[int32]chunkCacheEntry{}
	a.chunkCacheBytes = 0
	a.chunkCacheClock = 0
	a.overlay = map[int32][]byte{}
	a.poolsDirty = false
	a.structuralDirty = false
	a.removedSpans = make(map[int32][]removedFileSpan)
}

func marshalItem(b []byte, it *fileItem) {
	binary.LittleEndian.PutUint32(b[0:], uint32(it.nameOff))
	binary.LittleEndian.PutUint32(b[4:], uint32(it.pathOff))
	binary.LittleEndian.PutUint32(b[8:], uint32(it.chunk))
	binary.LittleEndian.PutUint32(b[12:], uint32(it.off))
	binary.LittleEndian.PutUint32(b[16:], uint32(it.size))
	binary.LittleEndian.PutUint32(b[20:], uint32(it.typ))
}

func encodeHeader(h Header) []byte {
	b := make([]byte, headerSize)
	binary.LittleEndian.PutUint32(b[0:], h.Signature)
	copy(b[4:24], h.Guid[:])
	binary.LittleEndian.PutUint32(b[24:], uint32(h.FileCount))
	binary.LittleEndian.PutUint32(b[28:], uint32(h.Padding))
	binary.LittleEndian.PutUint32(b[32:], uint32(h.BodySize))
	binary.LittleEndian.PutUint32(b[36:], uint32(h.GroupCount))
	binary.LittleEndian.PutUint32(b[40:], uint32(h.HashTableSize))
	binary.LittleEndian.PutUint32(b[44:], uint32(h.NameTableSize))
	return b
}

// Revert drops the pending edit for entry i.
func (a *Archive) Revert(i int32) { delete(a.overlay, i) }
