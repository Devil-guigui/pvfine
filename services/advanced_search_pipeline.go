package services

import (
	"context"
	"sync"

	"pvfine/internal/pvf"
)

type advancedReferenceJob struct {
	index int32
	path  string
	file  pvf.File
	input pvf.StringReferenceInput
}

type advancedReferenceResult struct {
	index    int32
	path     string
	file     pvf.File
	refs     []pvf.StringReference
	refCount int
}

// A single reader owns archive access and decompression. Workers parse immutable
// snapshots and optionally write their own SQLite shards. Each worker has at
// most one queued input and one in-flight input/result, plus the reader/writer.
type advancedReferencePipeline struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	results chan advancedReferenceResult
	done    chan struct{}
}

// write must consume refs and their nested slices before returning. With a
// writer, channel results contain only file metadata and refCount; without a
// writer, they also contain independently owned refs.
func (d *advancedSQLite) startReferencePipeline(c *core, a *pvf.Archive, scanner *pvf.StringReferenceScanner, workers int, write func(context.Context, int, advancedReferenceResult) error) *advancedReferencePipeline {
	ctx, cancel := context.WithCancelCause(d.ctx)
	p := &advancedReferencePipeline{ctx: ctx, cancel: cancel, results: make(chan advancedReferenceResult), done: make(chan struct{})}
	var wg sync.WaitGroup
	workers = max(1, workers)
	jobs := make([]chan advancedReferenceJob, workers)
	for i := range jobs {
		jobs[i] = make(chan advancedReferenceJob, 1)
	}
	wg.Add(workers + 1)
	go func() {
		defer wg.Done()
		defer func() {
			for _, queue := range jobs {
				close(queue)
			}
		}()
		for {
			c.mu.RLock()
			if c.advancedDisk != d || c.archive != a || ctx.Err() != nil {
				c.mu.RUnlock()
				cancel(context.Canceled)
				return
			}
			index, input, ok, err := scanner.NextInput(ctx)
			job := advancedReferenceJob{index: index, input: input}
			if ok && err == nil {
				job.path, job.file = a.Path(index), a.File(index)
			}
			c.mu.RUnlock()
			if err != nil {
				cancel(err)
				return
			}
			if !ok {
				return
			}
			// Never wait on the writer or workers while holding core.mu.
			select {
			case jobs[int(index)%workers] <- job:
			case <-ctx.Done():
				return
			}
		}
	}()
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			var workspace pvf.StringReferenceWorkspace
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs[i]:
					if !ok {
						return
					}
					var refs []pvf.StringReference
					var err error
					if write != nil {
						refs, err = workspace.References(ctx, job.input)
					} else {
						refs, err = job.input.References(ctx)
					}
					if err != nil {
						cancel(err)
						return
					}
					result := advancedReferenceResult{index: job.index, path: job.path, file: job.file, refs: refs, refCount: len(refs)}
					// Release the raw snapshot before waiting for SQLite.
					job.input = pvf.StringReferenceInput{}
					if write != nil {
						if err := write(ctx, i, result); err != nil {
							cancel(err)
							return
						}
						// No borrowed slice may cross the channel. Drop all local
						// references before waiting, including oversized storage.
						result.refs = nil
						refs = nil
						workspace.Reset()
					}
					select {
					case p.results <- result:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(p.results)
		close(p.done)
	}()
	return p
}

func (p *advancedReferencePipeline) close() {
	p.cancel(context.Canceled)
	<-p.done
}

func (p *advancedReferencePipeline) err() error {
	<-p.done
	return context.Cause(p.ctx)
}
