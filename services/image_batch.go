package services

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"pvfine/internal/npk"
)

const maxImageBatchSize = 128
const maxImageDecodeWorkers = 4

type imageBatchIO struct {
	open  func(string) (*os.File, error)
	parse func(io.ReaderAt, int64, int64) (*npk.IMGFile, error)
}

type imageBatchJob struct {
	key       string
	index     int32
	record    *imageRecord
	positions []int
}

type imageBatchGroup struct {
	path string
	jobs []*imageBatchJob
}

// GetImages groups a small visible-image batch by NPK and IMG. It opens each
// package once and parses each requested member's frame table once. Generation
// prevents delayed frontend requests from reading a different resource set.
func (s *ImageService) GetImages(references []ImageReference, generation uint64) ([]ImageBatchResult, error) {
	defer debugPhase("GetImages", fmt.Sprintf("frames=%d", len(references)))()
	return s.getImages(references, &generation)
}

func (s *ImageService) getImages(references []ImageReference, expectedGeneration *uint64) ([]ImageBatchResult, error) {
	if len(references) > maxImageBatchSize {
		return nil, errors.New("图像批量请求超过 128 帧")
	}
	results := make([]ImageBatchResult, len(references))
	if len(references) == 0 {
		return results, nil
	}
	s.mu.Lock()
	snapshot, generation := s.snapshot, s.generation
	if snapshot == nil || s.directory == "" || (expectedGeneration != nil && *expectedGeneration != generation) {
		s.mu.Unlock()
		return results, nil
	}
	if s.decodeSlots == nil {
		s.decodeSlots = make(chan struct{}, maxImageDecodeWorkers)
	}
	slots, batchIO := s.decodeSlots, s.batchIO
	groups := make([]*imageBatchGroup, 0)
	byPackage := make(map[string]*imageBatchGroup)
	byKey := make(map[string]*imageBatchJob)
	for position, reference := range references {
		path := normalizeImagePath(reference.Path)
		if path == "" || reference.Index < 0 {
			continue
		}
		record := snapshot.byPath[path]
		if record == nil {
			continue
		}
		key := fmt.Sprintf("%d\x00%s\x00%d", generation, record.path, reference.Index)
		if cached, ok := s.cache[key]; ok {
			data := cached.data
			results[position].Data = &data
			continue
		}
		if job := byKey[key]; job != nil {
			job.positions = append(job.positions, position)
			continue
		}
		npkPath := filepath.Join(snapshot.directory, filepath.FromSlash(record.npkRelative))
		if !isImagePathWithin(snapshot.directory, npkPath) {
			results[position].Error = "NPK 索引路径越界"
			continue
		}
		group := byPackage[npkPath]
		if group == nil {
			group = &imageBatchGroup{path: npkPath}
			byPackage[npkPath] = group
			groups = append(groups, group)
		}
		job := &imageBatchJob{key: key, index: reference.Index, record: record, positions: []int{position}}
		byKey[key] = job
		group.jobs = append(group.jobs, job)
	}
	s.mu.Unlock()
	if batchIO.open == nil {
		batchIO.open = os.Open
	}
	if batchIO.parse == nil {
		batchIO.parse = npk.ParseIMGAt
	}
	queue := make(chan *imageBatchGroup)
	var workers sync.WaitGroup
	for worker := 0; worker < min(len(groups), maxImageDecodeWorkers); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for group := range queue {
				slots <- struct{}{}
				s.readImageBatchGroup(group, snapshot, generation, batchIO, results)
				<-slots
			}
		}()
	}
	for _, group := range groups {
		queue <- group
	}
	close(queue)
	workers.Wait()
	if !s.imageSnapshotCurrent(snapshot, generation) {
		return make([]ImageBatchResult, len(references)), nil
	}
	return results, nil
}

func (s *ImageService) imageSnapshotCurrent(snapshot *imageSnapshot, generation uint64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot == snapshot && s.generation == generation
}

func (s *ImageService) readImageBatchGroup(group *imageBatchGroup, snapshot *imageSnapshot, generation uint64, batchIO imageBatchIO, results []ImageBatchResult) {
	if !s.imageSnapshotCurrent(snapshot, generation) {
		return
	}
	file, err := batchIO.open(group.path)
	if err != nil {
		for _, job := range group.jobs {
			setImageBatchResult(results, job, ImageBatchResult{Error: fmt.Sprintf("读取 NPK 文件失败: %v", err)})
		}
		return
	}
	defer file.Close()
	// Adjacent jobs reuse a frame table; only one IMG table lives per worker.
	sort.SliceStable(group.jobs, func(i, j int) bool {
		if group.jobs[i].record.offset != group.jobs[j].record.offset {
			return group.jobs[i].record.offset < group.jobs[j].record.offset
		}
		return group.jobs[i].record.path < group.jobs[j].record.path
	})
	var current *imageRecord
	var img *npk.IMGFile
	var parseErr error
	for _, job := range group.jobs {
		if !s.imageSnapshotCurrent(snapshot, generation) {
			return
		}
		if current != job.record {
			current = job.record
			img = nil
			img, parseErr = batchIO.parse(file, current.offset, current.size)
		}
		if parseErr != nil {
			setImageBatchResult(results, job, ImageBatchResult{Error: fmt.Sprintf("读取 IMG 帧表失败: %v", parseErr)})
			continue
		}
		if job.index >= int32(len(img.Images)) {
			continue
		}
		data, size, err := decodeBatchImage(file, current, img, job.index)
		if err != nil {
			setImageBatchResult(results, job, ImageBatchResult{Error: err.Error()})
			continue
		}
		s.mu.Lock()
		if s.snapshot == snapshot && s.generation == generation {
			s.putCacheLocked(job.key, *data, size)
			setImageBatchResult(results, job, ImageBatchResult{Data: data})
		}
		s.mu.Unlock()
	}
}

func setImageBatchResult(results []ImageBatchResult, job *imageBatchJob, result ImageBatchResult) {
	for _, position := range job.positions {
		results[position] = result
	}
}

func decodeBatchImage(reader io.ReaderAt, record *imageRecord, img *npk.IMGFile, index int32) (*ImageData, int, error) {
	finishDecode := debugPhase("GetImages.read-and-decode", fmt.Sprintf("frame=%d", index))
	decoded, err := npk.DecodeImage(reader, record.offset, record.size, img, index)
	finishDecode()
	if err != nil {
		return nil, 0, fmt.Errorf("解码 IMG 图片失败: %w", err)
	}
	var encoded bytes.Buffer
	finishEncode := debugPhase("GetImages.png-encode", fmt.Sprintf("frame=%d", index))
	if err := png.Encode(&encoded, decoded); err != nil {
		finishEncode()
		return nil, 0, fmt.Errorf("编码 PNG 失败: %w", err)
	}
	finishEncode()
	bounds := decoded.Bounds()
	data := &ImageData{
		DataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes()),
		Width:   int32(bounds.Dx()), Height: int32(bounds.Dy()),
	}
	return data, encoded.Len(), nil
}
