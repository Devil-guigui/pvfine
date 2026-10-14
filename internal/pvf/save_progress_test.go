package pvf

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveProgressPreservesContainerBytes(t *testing.T) {
	for _, format := range []string{"standard", "paged", "TW"} {
		t.Run(format, func(t *testing.T) {
			var a *Archive
			if format == "TW" {
				var err error
				a, err = Parse(twFixture(t))
				if err != nil {
					t.Fatal(err)
				}
				i, _ := a.Find("equipment/test.equ")
				if err := a.SetText(i, "[name]\n`修改裝備`\n[rarity]\n5"); err != nil {
					t.Fatal(err)
				}
			} else {
				a = New()
				payload := make([]byte, 3<<20)
				_, _ = rand.New(rand.NewSource(42)).Read(payload)
				index := a.AddFile("etc/payload.bin", payload, TypeUnicode)
				if format == "paged" {
					a.format = paged115Profile
					a.pageKeys = make([]byte, 4*paged110PageKeySize)
				}
				var baseline bytes.Buffer
				if err := a.SaveTo(&baseline); err != nil {
					t.Fatal(err)
				}
				payload[0] ^= 0xff
				if err := a.SetRawBytes(index, payload); err != nil {
					t.Fatal(err)
				}
				a.AddFile("etc/new.bin", []byte("new content"), TypeUnicode)
			}
			defer a.Release()
			reference := a.CloneForBatch()
			defer reference.Release()
			var expected, actual bytes.Buffer
			if err := reference.SaveTo(&expected); err != nil {
				t.Fatal(err)
			}
			var events []SaveProgress
			if err := a.SaveToWithProgress(&actual, func(p SaveProgress) { events = append(events, p) }); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(expected.Bytes(), actual.Bytes()) {
				t.Fatal("progress changed archive bytes")
			}
			assertPhaseCounts(t, events, "packing")
			writes := assertPhaseCounts(t, events, "writing")
			if writes[len(writes)-1].Done != int64(actual.Len()) {
				t.Fatal("write progress did not count output bytes")
			}
			if format == "standard" && len(writes) < 4 {
				t.Fatal("large output did not report incremental writes")
			}
			// Saving an unchanged archive still counts its output, without a packing phase.
			events = nil
			actual.Reset()
			if err := a.SaveToWithProgress(&actual, func(p SaveProgress) { events = append(events, p) }); err != nil {
				t.Fatal(err)
			}
			for _, p := range events {
				if p.Phase == "packing" {
					t.Fatal("unchanged archive was reported as being rebuilt")
				}
			}
			assertPhaseCounts(t, events, "writing")
		})
	}
}

func assertPhaseCounts(t *testing.T, events []SaveProgress, phase string) []SaveProgress {
	t.Helper()
	var values []SaveProgress
	for _, p := range events {
		if p.Phase != phase {
			continue
		}
		if p.Total <= 0 || p.Done < 0 || p.Done > p.Total {
			t.Fatalf("invalid count: %+v", p)
		}
		if len(values) > 0 && (p.Done < values[len(values)-1].Done || p.Total != values[0].Total) {
			t.Fatal("phase counts regressed")
		}
		values = append(values, p)
	}
	if len(values) < 2 || values[0].Done != 0 || values[len(values)-1].Done != values[0].Total {
		t.Fatalf("incomplete %s phase: %+v", phase, values)
	}
	return values
}

func TestSaveProgressCompletesAfterAtomicReplacement(t *testing.T) {
	a := New()
	defer a.Release()
	a.AddFile("etc/raw.bin", []byte("saved data"), TypeUnicode)
	path := filepath.Join(t.TempDir(), "saved.pvf")
	var phases []string
	if err := a.SaveAsWithProgress(path, func(p SaveProgress) {
		phases = append(phases, p.Phase)
		if p.Phase == "done" {
			raw, err := os.ReadFile(path)
			if err != nil || len(raw) == 0 {
				t.Fatal("completion preceded destination replacement")
			}
			if _, err := os.Stat(path + ".pvftmp"); !os.IsNotExist(err) {
				t.Fatal("completion preceded temporary file removal")
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(phases) < 3 || phases[len(phases)-3] != "syncing" || phases[len(phases)-2] != "replacing" || phases[len(phases)-1] != "done" {
		t.Fatalf("missing final phases: %v", phases)
	}
	// Replacing a directory fails after writing; it must never report completion.
	destination := t.TempDir()
	if err := a.SaveAsWithProgress(destination, func(p SaveProgress) {
		if p.Phase == "done" {
			t.Fatal("failed replacement reported completion")
		}
	}); err == nil {
		t.Fatal("expected replacement failure")
	}
	if _, err := os.Stat(destination + ".pvftmp"); !os.IsNotExist(err) {
		t.Fatal("failed save left temporary output")
	}
}

type shortProgressWriter struct{}

func (shortProgressWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

func TestSaveProgressPropagatesShortWrite(t *testing.T) {
	a := New()
	defer a.Release()
	a.AddFile("etc/raw.bin", []byte("data"), TypeUnicode)
	var last SaveProgress
	err := a.SaveToWithProgress(shortProgressWriter{}, func(p SaveProgress) { last = p })
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v", err)
	}
	if last.Phase != "writing" || last.Done >= last.Total {
		t.Fatalf("failed write reported full progress: %+v", last)
	}
}
