package pvf

import (
	"context"
	"encoding/binary"
	"sort"
)

// Limit retained workspace capacity, not the number of references a file may
// contain. The outer array is at most 2 MiB at this capacity on 64-bit systems;
// the offset map and the small token/field slices also use bounded capacity.
const maxRetainedStringReferences = 32768

// StringReferenceWorkspace reuses temporary storage across file snapshots. Its
// zero value is ready to use. Each worker must own its workspace exclusively.
type StringReferenceWorkspace struct {
	byOffset map[int32]int
	refs     []StringReference
}

// Reset invalidates borrowed results and discards oversized storage. Call it
// after consuming a large result, before waiting for more work, so a blocked
// worker does not keep that file's temporary allocations alive.
func (w *StringReferenceWorkspace) Reset() {
	if cap(w.refs) > maxRetainedStringReferences {
		w.byOffset = nil
		w.refs = nil
		return
	}
	clear(w.byOffset)
	w.refs = w.refs[:0]
}

// References returns sorted references borrowed from the workspace. The result
// and its nested slices are valid only until the next References or Reset call;
// consumers must finish reading them synchronously and must not modify them.
// Use StringReferenceInput.References when independently owned results are needed.
func (w *StringReferenceWorkspace) References(ctx context.Context, input StringReferenceInput) ([]StringReference, error) {
	w.Reset()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if w.byOffset == nil {
		w.byOffset = make(map[int32]int)
	}
	add := func(offset int32) *StringReference {
		index, ok := w.byOffset[offset]
		if !ok {
			index = len(w.refs)
			if index < cap(w.refs) {
				w.refs = w.refs[:index+1]
			} else {
				w.refs = append(w.refs, StringReference{})
			}
			ref := &w.refs[index]
			ref.Offset = offset
			ref.Occurrences = 0
			ref.TokenTypes = ref.TokenTypes[:0]
			// FileFields contains only the static "name" and "path" strings,
			// never text borrowed from an archive or its decompressed chunks.
			ref.FileFields = ref.FileFields[:0]
			w.byOffset[offset] = index
		}
		ref := &w.refs[index]
		ref.Occurrences++
		return ref
	}
	ref := add(input.nameOff)
	ref.FileFields = appendUniqueString(ref.FileFields, "name")
	ref = add(input.pathOff)
	ref.FileFields = appendUniqueString(ref.FileFields, "path")
	for pos := 0; pos+5 <= len(input.raw); pos += 5 {
		if pos%40960 == 0 {
			if err := ctx.Err(); err != nil {
				w.Reset()
				return nil, err
			}
		}
		switch input.raw[pos] {
		case 3, 5, 6, 7, 8, 10:
			ref = add(int32(binary.LittleEndian.Uint32(input.raw[pos+1:])))
			ref.TokenTypes = appendUniqueInt32(ref.TokenTypes, int32(input.raw[pos]))
		}
	}
	sort.Slice(w.refs, func(i, j int) bool { return w.refs[i].Offset < w.refs[j].Offset })
	return w.refs, nil
}
