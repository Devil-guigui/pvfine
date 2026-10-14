package pvf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

func comparisonFingerprint(t *testing.T, a *Archive, index int32) FileFingerprint {
	t.Helper()
	raw, err := a.RawBytes(index)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := a.NewFileComparisonReader().Fingerprint(context.Background(), a.File(index).DataType, raw)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}
func TestComparisonFingerprintResolvesDifferentStringPools(t *testing.T) {
	a, b := New(), New()
	b.StringOffset("extra pool entry")
	ai, err := a.AddFileText("test.equ", "[name]\n`equal`\n[price]\n42", TypeScript)
	if err != nil {
		t.Fatal(err)
	}
	bi, err := b.AddFileText("test.equ", "[name]\n`equal`\n[price]\n42", TypeScript)
	if err != nil {
		t.Fatal(err)
	}
	ar, _ := a.RawBytes(ai)
	br, _ := b.RawBytes(bi)
	if bytes.Equal(ar, br) {
		t.Fatal("fixture does not use different offsets")
	}
	left, right := comparisonFingerprint(t, a, ai), comparisonFingerprint(t, b, bi)
	if left.NeedsText || right.NeedsText || left.Digest != right.Digest {
		t.Fatal("equivalent tokens differ")
	}
	// Same token bytes may mean different text if a referenced pool value changes.
	changed := a.CloneForBatch()
	offset := a.StringOffset("equal")
	copy(changed.strA[int(offset>>1):], []byte("other"))
	if comparisonFingerprint(t, changed, ai).Digest == left.Digest {
		t.Fatal("same bytes with changed pool contents were treated as equal")
	}
}
func TestComparisonFingerprintPreservesTokenTypesAndFallback(t *testing.T) {
	a := New()
	i := a.AddFile("test.equ", []byte{0, 1, 0, 0, 0}, TypeScript)
	old := comparisonFingerprint(t, a, i)
	if err := a.SetRawBytes(i, []byte{2, 1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if comparisonFingerprint(t, a, i).Digest == old.Digest {
		t.Fatal("token type lost")
	}
	for _, raw := range [][]byte{{1, 0, 0, 0, 0}, {0, 1, 0}} {
		fp, err := a.NewFileComparisonReader().Fingerprint(context.Background(), TypeScript, raw)
		if err != nil || !fp.NeedsText {
			t.Fatalf("unsupported token needs fallback: %+v %v", fp, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.NewFileComparisonReader().Fingerprint(ctx, TypeScript, []byte{0, 1, 0, 0, 0}); err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
}
func TestComparisonChunkLargerThanInteractiveCache(t *testing.T) {
	a := New()
	// Every entry is packed into one >64 MiB chunk. Traditional per-file reads
	// cannot cache it and decompress the entire chunk again for each entry.
	a.AddFile("padding.bin", make([]byte, defaultChunkCacheLimit+1), 99)
	var selected []int32
	for i := 0; i < 24; i++ {
		index, err := a.AddFileText(fmt.Sprintf("items/%02d.equ", i), fmt.Sprintf("[price]\n%d", i), TypeScript)
		if err != nil {
			t.Fatal(err)
		}
		selected = append(selected, index)
	}
	var packed bytes.Buffer
	if err := a.SaveTo(&packed); err != nil {
		t.Fatal(err)
	}
	reopened, err := Parse(packed.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.groups) != 1 || int64(reopened.groups[0].origSize) <= defaultChunkCacheLimit {
		t.Fatal("fixture is not an oversized single chunk")
	}
	batches, visited := 0, 0
	err = reopened.ForEachComparisonChunk(context.Background(), selected, func(chunk ComparisonChunk) error {
		batches++
		for _, index := range chunk.Indexes() {
			raw, err := chunk.Bytes(index)
			if err != nil {
				return err
			}
			text, err := reopened.CanonicalTextFromRaw(index, raw)
			if err != nil {
				return err
			}
			if text == "" {
				t.Fatal("selected script payload missing")
			}
			visited++
		}
		return nil
	})
	if err != nil || batches != 1 || visited != len(selected) {
		t.Fatalf("batches=%d visited=%d err=%v", batches, visited, err)
	}
	if len(reopened.chunkCache) != 0 {
		t.Fatal("scan populated the interactive LRU")
	}
}
func TestComparisonChunkTWAndOverlay(t *testing.T) {
	a, err := Parse(twFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	indexes := make([]int32, a.FileCount())
	for i := range indexes {
		indexes[i] = int32(i)
	}
	err = a.ForEachComparisonChunk(context.Background(), indexes, func(chunk ComparisonChunk) error {
		for _, index := range chunk.Indexes() {
			raw, err := chunk.Bytes(index)
			if err != nil {
				return err
			}
			expected, err := a.RawBytes(index)
			if err != nil {
				return err
			}
			if !bytes.Equal(raw, expected) {
				t.Fatalf("TW payload %d changed", index)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Overlay-only batches must not decompress an unreadable original chunk.
	b := New()
	index, _ := b.AddFileText("test.equ", "[price]\n1", TypeScript)
	var packed bytes.Buffer
	if err := b.SaveTo(&packed); err != nil {
		t.Fatal(err)
	}
	b, err = Parse(packed.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.SetRawBytes(index, []byte{0, 7, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	b.data[b.bodyOff] ^= 0xff
	clear(b.chunkCache)
	clear(b.chunkCacheMeta)
	b.chunkCacheBytes = 0
	err = b.ForEachComparisonChunk(context.Background(), []int32{index}, func(chunk ComparisonChunk) error {
		raw, err := chunk.Bytes(index)
		if err == nil && binary.LittleEndian.Uint32(raw[1:]) != 7 {
			t.Fatal("overlay ignored")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestComparisonStreamingCanonicalHash(t *testing.T) {
	a := New()
	for _, text := range []string{"", "[name]\n`hello`\n[price]\n42\n1.25", "[name]\n`" + string(bytes.Repeat([]byte("long value "), 10000)) + "`"} {
		i, err := a.AddFileText(fmt.Sprintf("stream%d.equ", a.FileCount()), text, TypeScript)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := a.RawBytes(i)
		canonical, err := a.CanonicalText(i)
		if err != nil {
			t.Fatal(err)
		}
		var prefix [4]byte
		binary.LittleEndian.PutUint32(prefix[:], uint32(TypeScript))
		want := sha256.Sum256(append(prefix[:], []byte(canonical)...))
		reader := a.NewFileComparisonReader()
		got, err := reader.CanonicalFingerprint(context.Background(), i, raw)
		if err != nil || got.Digest != want {
			t.Fatalf("stream hash differs: %v", err)
		}
		if cap(reader.buffer) != 64<<10 {
			t.Fatal("hash buffer exceeded its bound")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.NewFileComparisonReader().CanonicalFingerprint(ctx, 0, nil); err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
	if _, err := a.NewFileComparisonReader().CanonicalFingerprint(context.Background(), -1, nil); err != ErrBadIndex {
		t.Fatalf("invalid index=%v", err)
	}
}

func BenchmarkComparisonCanonicalStreaming(b *testing.B) {
	a := New()
	i, err := a.AddFileText("stream.equ", strings.Repeat("[name]\n`long value for canonical hashing`\n", 4096), TypeScript)
	if err != nil {
		b.Fatal(err)
	}
	raw, _ := a.RawBytes(i)
	b.Run("materialized", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			text, err := a.CanonicalTextFromRaw(i, raw)
			if err != nil {
				b.Fatal(err)
			}
			_ = sha256.Sum256([]byte(text))
		}
	})
	b.Run("streamed", func(b *testing.B) {
		reader := a.NewFileComparisonReader()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := reader.CanonicalFingerprint(context.Background(), i, raw); err != nil {
				b.Fatal(err)
			}
		}
	})
}
