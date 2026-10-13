package pvf

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestPagedWriterRejectsCapacityBeforeWriting(t *testing.T) {
	keys := make([]byte, paged110PageKeySize)
	for _, size := range []int{paged110PageSize - 1, paged110PageSize, paged110PageSize + 1} {
		var out bytes.Buffer
		err := writePageGuarded(&out, make([]byte, size), keys)
		if size > paged110PageSize {
			if !errors.Is(err, ErrPagedCapacity) || out.Len() != 0 {
				t.Fatal("over-capacity write was not rejected before output")
			}
		} else if err != nil || out.Len() != size {
			t.Fatal("valid page-table boundary was rejected")
		}
	}
	if err := validatePagedCapacity(1, keys[:len(keys)-1]); !errors.Is(err, ErrPagedCapacity) {
		t.Fatal("incomplete page key accepted")
	}
}

func TestPagedCapacityFailurePreservesDestinationAndCanRetry(t *testing.T) {
	a := New()
	defer a.Release()
	a.format = paged115Profile
	a.pageKeys = make([]byte, paged110PageKeySize)
	payload := make([]byte, paged110PageSize)
	_, _ = rand.New(rand.NewSource(115)).Read(payload)
	index := a.AddFile("etc/padding.bin", payload, TypeUnicode)
	before := a.Header()
	dest := filepath.Join(t.TempDir(), "existing.pvf")
	original := []byte("existing destination")
	if err := os.WriteFile(dest, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveAs(dest); !errors.Is(err, ErrPagedCapacity) {
		t.Fatalf("expected capacity error, got %v", err)
	}
	if got, err := os.ReadFile(dest); err != nil || !bytes.Equal(got, original) {
		t.Fatal("failed save replaced destination")
	}
	if _, err := os.Stat(dest + ".pvftmp"); !os.IsNotExist(err) {
		t.Fatal("failed save left temporary output")
	}
	if a.Header() != before || !a.Modified() {
		t.Fatal("failed save adopted rebuilt header or cleared edits")
	}
	if err := a.SetRawBytes(index, []byte("smaller payload")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := a.SaveTo(&out); err != nil {
		t.Fatalf("retry after reducing content: %v", err)
	}
}

func TestDenseNamePoolsPreserveOffsets(t *testing.T) {
	a := New()
	defer a.Release()
	// Structured names with varying repetitions exercise the denser fallback.
	rng := rand.New(rand.NewSource(115))
	for i := 0; i < 10000; i++ {
		a.StringOffset(fmt.Sprintf("equipment/character/%d/common/%d/装备%d.equ", rng.Intn(100), rng.Intn(100), i))
	}
	regular, err := a.buildNameTable()
	if err != nil {
		t.Fatal(err)
	}
	dense, err := a.buildNameTableLevel(zlib.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	b := New()
	defer b.Release()
	if err := b.parseNameTable(dense); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.strA, b.strA) || !bytes.Equal(a.strW, b.strW) {
		t.Fatal("dense compression changed pool bytes or offsets")
	}
	if len(dense) >= len(regular) {
		t.Fatal("fixture did not exercise denser compression")
	}
	// Place a real compressed payload close enough to the boundary that only
	// the denser name table fits. Start as a plain container to measure overhead.
	payload := make([]byte, paged110PageSize)
	_, _ = rng.Read(payload)
	index := a.AddFile("etc/padding.bin", payload, TypeUnicode)
	regular, err = a.buildNameTable()
	if err != nil {
		t.Fatal(err)
	}
	dense, err = a.buildNameTableLevel(zlib.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	oversized, err := a.rebuild()
	if err != nil {
		t.Fatal(err)
	}
	reduce := len(oversized) - paged110PageSize - (len(regular)-len(dense))/2
	payload = payload[:len(payload)-reduce]
	if err := a.SetRawBytes(index, payload); err != nil {
		t.Fatal(err)
	}
	plain, err := a.rebuild()
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) <= paged110PageSize || len(plain)-(len(regular)-len(dense)) > paged110PageSize {
		t.Fatalf("fixture does not straddle page capacity: size=%d saving=%d", len(plain), len(regular)-len(dense))
	}
	a.format = paged115Profile
	a.pageKeys = make([]byte, paged110PageKeySize)
	var out bytes.Buffer
	if err := a.SaveTo(&out); err != nil {
		t.Fatalf("dense pool fallback: %v", err)
	}
	if out.Len() > paged110PageSize {
		t.Fatal("fallback still exceeds page capacity")
	}
	logical := out.Bytes()
	decryptPageGuards(logical, a.pageKeys)
	reopened, err := Parse(logical)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Release()
	if got, err := reopened.RawBytes(mustFind(reopened, "etc/padding.bin")); err != nil || !bytes.Equal(got, payload) {
		t.Fatal("fallback changed file payload")
	}
	if !bytes.Equal(a.strW, reopened.strW) {
		t.Fatal("fallback changed string offsets")
	}
}

func TestPagedCapacityFailureRestoresExistingSpans(t *testing.T) {
	source := New()
	defer source.Release()
	source.strA = nil
	source.AddFile("etc/first.bin", []byte("first"), TypeUnicode)
	untouched := []byte("untouched second payload")
	source.AddFile("etc/second.bin", untouched, TypeUnicode)
	var initial bytes.Buffer
	if err := source.SaveTo(&initial); err != nil {
		t.Fatal(err)
	}
	a, err := Parse(initial.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	a.format = paged115Profile
	a.pageKeys = make([]byte, paged110PageKeySize)
	payload := make([]byte, paged110PageSize)
	_, _ = rand.New(rand.NewSource(115)).Read(payload)
	if err := a.SetRawBytes(0, payload); err != nil {
		t.Fatal(err)
	}
	spans := append([]fileItem(nil), a.items...)
	var rejected bytes.Buffer
	if err := a.SaveTo(&rejected); !errors.Is(err, ErrPagedCapacity) || rejected.Len() != 0 {
		t.Fatal("capacity failure wrote output")
	}
	for i, item := range a.items {
		if item != spans[i] {
			t.Fatal("capacity failure changed original file spans")
		}
	}
	if err := a.SetRawBytes(0, []byte("reduced first payload")); err != nil {
		t.Fatal(err)
	}
	var retried bytes.Buffer
	if err := a.SaveTo(&retried); err != nil {
		t.Fatal(err)
	}
	decryptPageGuards(retried.Bytes(), a.pageKeys)
	b, err := Parse(retried.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release()
	if got, err := b.RawBytes(mustFind(b, "etc/second.bin")); err != nil || !bytes.Equal(got, untouched) {
		t.Fatal("retry corrupted untouched payload")
	}
}
