package services

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"

	"pvfine/internal/pvf"
	pvfversion "pvfine/internal/version"
)

type archiveDiffEntry struct {
	index    int32
	path     string
	dataType int32
}

func archiveDiffEntries(ctx context.Context, a *pvf.Archive) (map[string]archiveDiffEntry, error) {
	entries := make(map[string]archiveDiffEntry, a.FileCount())
	for i := int32(0); i < a.FileCount(); i++ {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		path := pvfversion.DisplayPath(a.Path(i))
		key := pvfversion.CanonicalPath(path)
		if key == "" {
			return nil, fmt.Errorf("归档中存在空路径")
		}
		if _, exists := entries[key]; exists {
			return nil, fmt.Errorf("归档包含重复路径 %q，无法唯一比较", path)
		}
		typ, _, err := a.FileContentInfo(i)
		if err != nil {
			return nil, err
		}
		entries[key] = archiveDiffEntry{index: i, path: path, dataType: typ}
	}
	return entries, nil
}
func archiveDiffChange(key string, before, after archiveDiffEntry, operation pvfversion.Operation) pvfversion.FileChange {
	path := after.path
	if operation == pvfversion.OperationDelete {
		path = before.path
	}
	return pvfversion.FileChange{Path: key, DisplayPath: path, Operation: operation, BeforeType: before.dataType, AfterType: after.dataType}
}
func archiveDiffCompare(ctx context.Context, before, after *pvf.Archive, progress func(int)) ([]pvfversion.FileChange, error) {
	return archiveDiffComparePhases(ctx, before, after, progress, nil)
}

func archiveDiffComparePhases(ctx context.Context, before, after *pvf.Archive, progress func(int), phase func(string, int, int)) ([]pvfversion.FileChange, error) {
	report := func(state string, processed, total int) {
		if phase != nil {
			phase(state, processed, total)
		}
	}
	oldEntries, err := archiveDiffEntries(ctx, before)
	if err != nil {
		return nil, err
	}
	newEntries, err := archiveDiffEntries(ctx, after)
	if err != nil {
		return nil, err
	}
	compatible := before.TokenComparisonCompatible(after)
	changes := make([]pvfversion.FileChange, 0)
	oldIndexes, newIndexes := make([]int32, 0), make([]int32, 0)
	for key, old := range oldEntries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		next, exists := newEntries[key]
		if !exists {
			changes = append(changes, archiveDiffChange(key, old, archiveDiffEntry{}, pvfversion.OperationDelete))
			continue
		}
		if old.dataType != next.dataType {
			changes = append(changes, archiveDiffChange(key, old, next, pvfversion.OperationModify))
			continue
		}
		if old.dataType == pvf.TypeScript && !compatible {
			continue
		}
		oldIndexes = append(oldIndexes, old.index)
		newIndexes = append(newIndexes, next.index)
	}
	for key, next := range newEntries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, exists := oldEntries[key]; !exists {
			changes = append(changes, archiveDiffChange(key, archiveDiffEntry{}, next, pvfversion.OperationAdd))
		}
	}
	// Added/deleted/type-changed entries need no content scan to classify.
	processed := int(before.FileCount()+after.FileCount()) - len(oldIndexes) - len(newIndexes)
	progress(processed)
	oldHashes, err := archiveDiffScan(ctx, before, oldIndexes, false, func(n int) { progress(processed + n) })
	if err != nil {
		return nil, err
	}
	processed += len(oldIndexes)
	newHashes, err := archiveDiffScan(ctx, after, newIndexes, false, func(n int) { progress(processed + n) })
	if err != nil {
		return nil, err
	}
	processed += len(newIndexes)
	type candidate struct {
		key       string
		old, next archiveDiffEntry
	}
	candidates := make([]candidate, 0)
	oldFallback, newFallback := make([]int32, 0), make([]int32, 0)
	report("classifying", 0, len(oldEntries))
	classified := 0
	for key, old := range oldEntries {
		classified++
		if classified%64 == 0 {
			report("classifying", classified, len(oldEntries))
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		next, exists := newEntries[key]
		if !exists || old.dataType != next.dataType {
			continue
		}
		left, right := oldHashes.get(old.index), newHashes.get(next.index)
		needsText := left.NeedsText || right.NeedsText || (old.dataType == pvf.TypeScript && !compatible)
		if left.Digest == right.Digest && !needsText {
			continue
		}
		if old.dataType != pvf.TypeScript && old.dataType != pvf.TypeUnicode {
			change := archiveDiffChange(key, old, next, pvfversion.OperationModify)
			change.BeforeHash = hex.EncodeToString(left.Digest[:])
			change.AfterHash = hex.EncodeToString(right.Digest[:])
			changes = append(changes, change)
			continue
		}
		candidates = append(candidates, candidate{key, old, next})
		oldFallback = append(oldFallback, old.index)
		newFallback = append(newFallback, next.index)
	}
	// Re-scan only affected chunks. Fallback hashes stay identical to the old
	// canonical-text comparison, including unknown tokens, floats and mojibake.
	oldHashes = nil
	newHashes = nil
	fallbackTotal := len(oldFallback) + len(newFallback)
	report("verifying", 0, fallbackTotal)
	oldText, err := archiveDiffScan(ctx, before, oldFallback, true, func(n int) { report("verifying", n, fallbackTotal) })
	if err != nil {
		return nil, err
	}
	newText, err := archiveDiffScan(ctx, after, newFallback, true, func(n int) { report("verifying", len(oldFallback)+n, fallbackTotal) })
	if err != nil {
		return nil, err
	}
	for _, item := range candidates {
		left, right := oldText.get(item.old.index), newText.get(item.next.index)
		if left.Digest == right.Digest {
			continue
		}
		change := archiveDiffChange(item.key, item.old, item.next, pvfversion.OperationModify)
		change.BeforeHash = hex.EncodeToString(left.Digest[:])
		change.AfterHash = hex.EncodeToString(right.Digest[:])
		changes = append(changes, change)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	report("sorting", 0, 0)
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	progress(processed)
	return changes, nil
}

type archiveDiffScanJob struct {
	chunk pvf.ComparisonChunk
	index int32
	batch *sync.WaitGroup
}
type archiveDiffScanResult struct {
	index       int32
	fingerprint pvf.FileFingerprint
	err         error
}

// Dense common-file scans use compact arrays; sparse fallback scans allocate
// only for their candidates rather than a full archive-sized result table.
type archiveDiffFingerprints struct {
	dense  []pvf.FileFingerprint
	sparse map[int32]pvf.FileFingerprint
}

func (f *archiveDiffFingerprints) get(index int32) pvf.FileFingerprint {
	if f.dense != nil {
		return f.dense[index]
	}
	return f.sparse[index]
}
func (f *archiveDiffFingerprints) set(index int32, value pvf.FileFingerprint) {
	if f.dense != nil {
		f.dense[index] = value
	} else {
		f.sparse[index] = value
	}
}

// The producer owns one chunk; workers release all payload references before
// its batch completes. Results contain only digests, never decoded file bodies.
func archiveDiffScan(parent context.Context, a *pvf.Archive, indexes []int32, canonical bool, progress func(int)) (*archiveDiffFingerprints, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	values := &archiveDiffFingerprints{}
	if len(indexes) >= max(1, int(a.FileCount())/3) {
		values.dense = make([]pvf.FileFingerprint, a.FileCount())
	} else {
		values.sparse = make(map[int32]pvf.FileFingerprint, len(indexes))
	}
	if len(indexes) == 0 {
		return values, nil
	}
	workers := archiveDiffWorkerCount(int32(len(indexes)))
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	jobs := make(chan archiveDiffScanJob)
	results := make(chan archiveDiffScanResult, workers)
	largeFile := make(chan struct{}, 1)
	var running sync.WaitGroup
	running.Add(workers + 1)
	go func() {
		defer running.Done()
		defer close(jobs)
		err := a.ForEachComparisonChunk(ctx, indexes, func(chunk pvf.ComparisonChunk) error {
			var batch sync.WaitGroup
			defer batch.Wait()
			for _, index := range chunk.Indexes() {
				batch.Add(1)
				select {
				case jobs <- archiveDiffScanJob{chunk, index, &batch}:
				case <-ctx.Done():
					batch.Done()
					return ctx.Err()
				}
			}
			return nil
		})
		if err != nil {
			select {
			case results <- archiveDiffScanResult{err: err}:
			case <-ctx.Done():
			}
		}
	}()
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer running.Done()
			reader := a.NewFileComparisonReader()
			for job := range jobs {
				dataType, size, infoErr := a.FileContentInfo(job.index)
				result := archiveDiffScanResult{index: job.index, err: infoErr}
				large := size >= 8<<20
				acquired := false
				if large {
					select {
					case largeFile <- struct{}{}:
						acquired = true
					case <-ctx.Done():
						result.err = ctx.Err()
					}
				}
				if result.err == nil {
					if err := ctx.Err(); err != nil {
						result.err = err
					} else {
						raw, err := job.chunk.Bytes(job.index)
						if err != nil {
							result.err = err
						} else if canonical {
							result.fingerprint, result.err = reader.CanonicalFingerprint(ctx, job.index, raw)
						} else {
							result.fingerprint, result.err = reader.Fingerprint(ctx, dataType, raw)
						}
					}
				}
				if acquired {
					<-largeFile
				}
				batch := job.batch
				// Clear the chunk reference before allowing the producer to decode another.
				job = archiveDiffScanJob{}
				select {
				case results <- result:
				case <-ctx.Done():
				}
				batch.Done()
			}
		}()
	}
	go func() { running.Wait(); close(results) }()
	var firstErr error
	processed := 0
	for result := range results {
		if firstErr != nil || ctx.Err() != nil {
			continue
		}
		if result.err != nil {
			firstErr = result.err
			cancel()
			continue
		}
		values.set(result.index, result.fingerprint)
		processed++
		if progress != nil && (processed == 1 || processed%64 == 0) {
			progress(processed)
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if progress != nil {
		progress(processed)
	}
	return values, nil
}
