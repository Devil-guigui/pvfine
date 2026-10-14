package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"pvfine/internal/pvf"
)

func referencePipelineFixture(t testing.TB, files, tokens int) (*core, *advancedSQLite, []byte) {
	t.Helper()
	a := pvf.New()
	raw := make([]byte, tokens*5)
	for i := 0; i < tokens; i++ {
		raw[i*5] = []byte{3, 5, 6, 7}[i%4]
		binary.LittleEndian.PutUint32(raw[i*5+1:], uint32(a.StringOffset(fmt.Sprintf("value-%d", i%67))))
	}
	for i := 0; i < files; i++ {
		a.AddFile(fmt.Sprintf("dir/%d.equ", i), raw, pvf.TypeScript)
	}
	var buf bytes.Buffer
	if err := a.SaveTo(&buf); err != nil {
		t.Fatal(err)
	}
	a, err := pvf.Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &advancedSQLite{ctx: ctx, cancel: cancel, closed: make(chan struct{})}
	t.Cleanup(d.close)
	return &core{archive: a, advancedDisk: d}, d, buf.Bytes()
}

func TestAdvancedReferencePipelineMatchesSequential(t *testing.T) {
	for _, workers := range []int{1, 4} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			c, d, _ := referencePipelineFixture(t, 129, 1000)
			a := c.archive
			if err := a.SetRawBytes(0, []byte{7, 0, 0, 0, 0}); err != nil {
				t.Fatal(err)
			}
			a.AddFile("extra/new.equ", nil, pvf.TypeScript)
			a.AddFile("extra/text.str", []byte{7, 0, 0, 0, 0}, pvf.TypeUnicode)
			want := make(map[int32]advancedReferenceResult)
			scanner := a.NewStringReferenceScanner()
			for {
				index, refs, ok, err := scanner.Next(d.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					break
				}
				want[index] = advancedReferenceResult{index: index, path: a.Path(index), file: a.File(index), refs: refs, refCount: len(refs)}
			}
			p := d.startReferencePipeline(c, a, a.NewStringReferenceScanner(), workers, nil)
			defer p.close()
			for got := range p.results {
				if !reflect.DeepEqual(got, want[got.index]) {
					t.Fatalf("different or duplicate result for file %d", got.index)
				}
				delete(want, got.index)
			}
			if err := p.err(); err != nil || len(want) != 0 {
				t.Fatalf("missing=%d err=%v", len(want), err)
			}
		})
	}
}

func TestAdvancedReferencePipelineStopsWithBlockedWriter(t *testing.T) {
	for _, action := range []string{"writer-error", "cancel", "archive-replaced"} {
		t.Run(action, func(t *testing.T) {
			c, d, _ := referencePipelineFixture(t, 129, 1000)
			a := c.archive
			p := d.startReferencePipeline(c, a, a.NewStringReferenceScanner(), 4, nil)
			defer p.close()
			select {
			case <-p.results:
			case <-time.After(5 * time.Second):
				t.Fatal("reader did not start")
			}
			switch action {
			case "writer-error":
				go p.close()
			case "cancel":
				d.cancel()
			case "archive-replaced":
				c.mu.Lock()
				c.archive = nil
				c.mu.Unlock()
				// Let the reader reach its next identity check.
				go func() {
					for range p.results {
					}
				}()
			}
			select {
			case <-p.done:
			case <-time.After(5 * time.Second):
				t.Fatal("pipeline goroutines did not exit")
			}
			if !errors.Is(p.err(), context.Canceled) {
				t.Fatalf("unexpected cancellation: %v", p.err())
			}
		})
	}
}

func TestAdvancedReferencePipelineReadError(t *testing.T) {
	c, d, source := referencePipelineFixture(t, 8, 100)
	// Deliberately damage retained source bytes after parsing to exercise a
	// decompression failure; ordinary callers must treat these bytes as read-only.
	clear(source)
	a := c.archive
	p := d.startReferencePipeline(c, a, a.NewStringReferenceScanner(), 4, nil)
	defer p.close()
	for range p.results {
		t.Fatal("corrupt chunk produced a result")
	}
	if err := p.err(); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("reader error was lost: %v", err)
	}
}

func TestAdvancedReferencePipelineWriteError(t *testing.T) {
	c, d, _ := referencePipelineFixture(t, 32, 100)
	a := c.archive
	want := errors.New("injected shard write failure")
	p := d.startReferencePipeline(c, a, a.NewStringReferenceScanner(), 4, func(_ context.Context, _ int, _ advancedReferenceResult) error {
		return want
	})
	defer p.close()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("shard failure left workers blocked")
	}
	if !errors.Is(p.err(), want) {
		t.Fatalf("lost shard failure: %v", p.err())
	}
}

func BenchmarkAdvancedReferencePipeline(b *testing.B) {
	for _, workers := range []int{1, 4} {
		b.Run(fmt.Sprintf("workers-%d", workers), func(b *testing.B) {
			c, d, _ := referencePipelineFixture(b, 256, 16000)
			a := c.archive
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p := d.startReferencePipeline(c, a, a.NewStringReferenceScanner(), workers, nil)
				for range p.results {
				}
				err := p.err()
				p.close()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestAdvancedReferencePipelineWriterConsumesBeforeReuse(t *testing.T) {
	c, d, _ := referencePipelineFixture(t, 129, 1000)
	a := c.archive
	// Vary result sizes so a later parse cannot silently replace an earlier
	// file's count. Shared field offsets and token ordering are checked too.
	for i := int32(0); i < a.FileCount(); i += 7 {
		if err := a.SetRawBytes(i, []byte{7, 0, 0, 0, 0}); err != nil {
			t.Fatal(err)
		}
	}
	want := make(map[int32]advancedReferenceResult)
	scanner := a.NewStringReferenceScanner()
	for {
		index, refs, ok, err := scanner.Next(d.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		want[index] = advancedReferenceResult{index: index, path: a.Path(index), file: a.File(index), refs: refs, refCount: len(refs)}
	}
	var mu sync.Mutex
	written := make(map[int32]bool)
	p := d.startReferencePipeline(c, a, a.NewStringReferenceScanner(), 4, func(_ context.Context, _ int, result advancedReferenceResult) error {
		expected := want[result.index]
		// Reused empty slices can have non-nil backing storage. Compare their
		// values, including order, rather than allocation details.
		refsEqual := slices.EqualFunc(result.refs, expected.refs, func(left, right pvf.StringReference) bool {
			return left.Offset == right.Offset && left.Occurrences == right.Occurrences && slices.Equal(left.TokenTypes, right.TokenTypes) && slices.Equal(left.FileFields, right.FileFields)
		})
		if result.refCount != expected.refCount || result.path != expected.path || !reflect.DeepEqual(result.file, expected.file) || !refsEqual {
			return fmt.Errorf("writer received incorrect references for file %d", result.index)
		}
		mu.Lock()
		defer mu.Unlock()
		if written[result.index] {
			return fmt.Errorf("writer received duplicate file %d", result.index)
		}
		written[result.index] = true
		return nil
	})
	defer p.close()
	received := make(map[int32]bool)
	for result := range p.results {
		expected, ok := want[result.index]
		if !ok || received[result.index] || result.refs != nil || result.refCount != expected.refCount || result.path != expected.path || !reflect.DeepEqual(result.file, expected.file) {
			t.Fatalf("channel published borrowed or incorrect result for file %d", result.index)
		}
		mu.Lock()
		done := written[result.index]
		mu.Unlock()
		if !done {
			t.Fatal("channel result arrived before synchronous writer finished")
		}
		received[result.index] = true
	}
	if err := p.err(); err != nil || len(received) != len(want) {
		t.Fatalf("pipeline failed: received=%d want=%d err=%v", len(received), len(want), err)
	}
}

func TestAdvancedReferencePipelineCancelsBlockedSynchronousWriter(t *testing.T) {
	c, d, _ := referencePipelineFixture(t, 129, 1000)
	started := make(chan struct{}, 1)
	p := d.startReferencePipeline(c, c.archive, c.archive.NewStringReferenceScanner(), 4, func(ctx context.Context, _ int, _ advancedReferenceResult) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	})
	defer p.close()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not start")
	}
	d.cancel()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation left writer or reader blocked")
	}
	if !errors.Is(p.err(), context.Canceled) {
		t.Fatalf("lost cancellation: %v", p.err())
	}
	for range p.results {
		t.Fatal("canceled writer published a result")
	}
}
