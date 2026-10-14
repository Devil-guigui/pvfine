package services

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pvfine/internal/npk"
)

func writeImageBatchTestNPK(t *testing.T, path string, names ...string) {
	t.Helper()
	data := append([]byte("NeoplePack_Bill"), 0)
	putImageTestInt32(&data, len(names))
	member := append([]byte("Neople Img File"), 0)
	putImageTestInt32(&member, 72, 0, 2, 2)
	for range 2 {
		putImageTestInt32(&member, 16, 5, 1, 1, 4, 0, 0, 1, 1)
	}
	member = append(member, 3, 2, 1, 255, 6, 5, 4, 255)
	key := []byte("puchikon@neople dungeon and fighter " + strings.Repeat("DNF", 73) + "\x00")
	for i, name := range names {
		putImageTestInt32(&data, 20+264*len(names)+i*len(member), len(member))
		var encrypted [256]byte
		for j := range encrypted {
			var value byte
			if j < len(name) {
				value = name[j]
			}
			encrypted[j] = value ^ key[j%len(key)]
		}
		data = append(data, encrypted[:]...)
	}
	for range names {
		data = append(data, member...)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func imageBatchTestService(t *testing.T, packages int) *ImageService {
	t.Helper()
	root := t.TempDir()
	for n := 0; n < packages; n++ {
		writeImageBatchTestNPK(t, filepath.Join(root, fmt.Sprintf("%d.npk", n)), fmt.Sprintf("sprite/item/%d-a.img", n), fmt.Sprintf("sprite/item/%d-b.img", n))
	}
	s := &ImageService{directory: root, generation: 1, cachePath: filepath.Join(t.TempDir(), "index.json")}
	s.buildIndex(context.Background(), root, 1, time.Now())
	if s.IndexStatus().State != ImageIndexStateReady {
		t.Fatal("image index not ready")
	}
	return s
}

func TestImageBatchGroupsPackagesMembersAndDuplicateFrames(t *testing.T) {
	s := imageBatchTestService(t, 2)
	var opens, parses atomic.Int32
	s.batchIO = imageBatchIO{
		open: func(path string) (*os.File, error) { opens.Add(1); return os.Open(path) },
		parse: func(r io.ReaderAt, offset, size int64) (*npk.IMGFile, error) {
			parses.Add(1)
			return npk.ParseIMGAt(r, offset, size)
		},
	}
	refs := []ImageReference{
		{Path: "item/0-b.img"}, {Path: "ITEM\\0-A.IMG"}, {Path: "item/1-a.img"},
		{Path: "sprite/item/0-a.img"}, {Path: "item/0-a.img", Index: 1}, {Path: "item/0-a.img", Index: 9},
		{Path: "missing.img"}, {Path: "item/0-a.img", Index: -1},
	}
	results, err := s.GetImages(refs, 1)
	if err != nil || len(results) != len(refs) {
		t.Fatalf("batch result length: %d, %v", len(results), err)
	}
	for i, result := range results {
		if result.Error != "" {
			t.Fatalf("result %d: %s", i, result.Error)
		}
		if i < 5 && (result.Data == nil || result.Data.Width != 1) {
			t.Fatalf("result %d missing", i)
		}
		if i >= 5 && result.Data != nil {
			t.Fatalf("invalid result %d returned data", i)
		}
	}
	if results[1].Data != results[3].Data {
		t.Fatal("duplicate frame was decoded separately")
	}
	if opens.Load() != 2 || parses.Load() != 3 {
		t.Fatalf("opens/parses = %d/%d, want 2/3", opens.Load(), parses.Load())
	}
	if results[1].Data.DataURL == results[4].Data.DataURL {
		t.Fatal("different frames returned the same pixels")
	}
	_, err = s.GetImages(refs[:5], 1)
	if err != nil || opens.Load() != 2 || parses.Load() != 3 {
		t.Fatalf("cache did IO: %v, %d/%d", err, opens.Load(), parses.Load())
	}
}

func TestImageBatchIsolatesErrorsAndRejectsOldGeneration(t *testing.T) {
	s := imageBatchTestService(t, 2)
	if err := os.WriteFile(filepath.Join(s.directory, "0.npk"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	refs := []ImageReference{{Path: "item/0-a.img"}, {Path: "item/1-a.img"}}
	results, err := s.GetImages(refs, 1)
	if err != nil || results[0].Error == "" || results[1].Data == nil {
		t.Fatalf("partial results = %#v, %v", results, err)
	}
	results, err = s.GetImages(refs, 0)
	if err != nil || results[0].Error != "" || results[1].Data != nil {
		t.Fatalf("stale request = %#v, %v", results, err)
	}
	if _, err := s.GetImages(make([]ImageReference, maxImageBatchSize+1), 1); err == nil {
		t.Fatal("oversize batch accepted")
	}
}

func TestImageBatchDiscardsResultsWhenGenerationChangesDuringRead(t *testing.T) {
	s := imageBatchTestService(t, 1)
	s.batchIO.open = func(path string) (*os.File, error) {
		file, err := os.Open(path)
		s.mu.Lock()
		s.generation++
		s.clearCacheLocked()
		s.mu.Unlock()
		return file, err
	}
	results, err := s.GetImages([]ImageReference{{Path: "item/0-a.img"}}, 1)
	if err != nil || results[0].Data != nil || results[0].Error != "" || len(s.cache) != 0 {
		t.Fatalf("old generation published: %#v, %v, cache=%d", results, err, len(s.cache))
	}
}

func TestImageBatchBoundsDecodersAcrossConcurrentRequests(t *testing.T) {
	s := imageBatchTestService(t, 8)
	var active, peak atomic.Int32
	entered := make(chan struct{}, 32)
	release := make(chan struct{})
	s.batchIO.open = func(path string) (*os.File, error) {
		value := active.Add(1)
		for old := peak.Load(); value > old; old = peak.Load() {
			if peak.CompareAndSwap(old, value) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return os.Open(path)
	}
	refs := make([]ImageReference, 8)
	for i := range refs {
		refs[i] = ImageReference{Path: fmt.Sprintf("item/%d-a.img", i)}
	}
	var requests sync.WaitGroup
	requests.Add(3)
	for range 3 {
		go func() {
			defer requests.Done()
			if _, err := s.GetImages(refs, 1); err != nil {
				t.Error(err)
			}
		}()
	}
	defer requests.Wait()
	defer close(release)
	for range maxImageDecodeWorkers {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("decoders did not start")
		}
	}
	select {
	case <-entered:
		t.Fatal("decoder concurrency limit exceeded")
	case <-time.After(20 * time.Millisecond):
	}
	if peak.Load() != maxImageDecodeWorkers {
		t.Fatalf("peak=%d", peak.Load())
	}
}
