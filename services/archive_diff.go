package services

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"pvfine/internal/pvf"
	pvfversion "pvfine/internal/version"
)

type ArchiveDiffDraft struct {
	FileIndex int32  `json:"fileIndex"`
	Path      string `json:"path"`
	Text      string `json:"text"`
}

type ArchiveDiffRequest struct {
	Path   string             `json:"path"`
	Drafts []ArchiveDiffDraft `json:"drafts"`
}

type ArchiveDiffStatus struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	CurrentPath string `json:"currentPath"`
	OtherPath   string `json:"otherPath"`
	Processed   int    `json:"processed"`
	Total       int    `json:"total"`
	Added       int    `json:"added"`
	Modified    int    `json:"modified"`
	Deleted     int    `json:"deleted"`
	Error       string `json:"error"`
}

type ArchiveDiffNode struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	IsDir     bool   `json:"isDir"`
	Operation string `json:"operation"`
	Count     int    `json:"count"`
}

type ArchiveDiffFile struct {
	Path          string `json:"path"`
	Operation     string `json:"operation"`
	BeforeHash    string `json:"beforeHash"`
	AfterHash     string `json:"afterHash"`
	BeforeType    int32  `json:"beforeType"`
	AfterType     int32  `json:"afterType"`
	BeforeSize    int32  `json:"beforeSize"`
	AfterSize     int32  `json:"afterSize"`
	BeforeExists  bool   `json:"beforeExists"`
	AfterExists   bool   `json:"afterExists"`
	BeforeText    string `json:"beforeText"`
	AfterText     string `json:"afterText"`
	TextAvailable bool   `json:"textAvailable"`
	Message       string `json:"message"`
}

type archiveDiffSession struct {
	status         ArchiveDiffStatus
	cancel         context.CancelFunc
	current, other *pvf.Archive
	changes        map[string]pvfversion.FileChange
	children       map[string][]ArchiveDiffNode
}

var archiveDiffSequence atomic.Uint64

// ArchiveDiffService owns an isolated read-only comparison. It never installs
// the comparison archive in core or initializes workspace-derived indexes.
type ArchiveDiffService struct {
	c       *core
	mu      sync.Mutex
	scanMu  sync.Mutex
	session *archiveDiffSession
}

func NewArchiveDiffService(c *core) *ArchiveDiffService {
	s := &ArchiveDiffService{c: c}
	c.archiveDiff = s
	return s
}

func (s *ArchiveDiffService) SelectArchive() (string, error) {
	return application.Get().Dialog.OpenFile().CanChooseFiles(true).CanChooseDirectories(false).
		AddFilter("PVF 归档", "*.pvf").SetTitle("选择对比归档").PromptForSingleSelection()
}

func (s *ArchiveDiffService) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		s.session.cancel()
		s.session = nil
	}
}

func (s *ArchiveDiffService) Cancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil && s.session.status.ID == id {
		s.session.cancel()
		s.session.current, s.session.other = nil, nil
		s.session.changes, s.session.children = nil, nil
		s.session.status.State = "cancelled"
	}
}

func (s *ArchiveDiffService) Start(request ArchiveDiffRequest) (*ArchiveDiffStatus, error) {
	s.Release()
	// Lock ordering is always core then diff; workspace replacement can release
	// comparisons while holding the core lock without deadlocking detail reads.
	s.c.mu.RLock()
	defer s.c.mu.RUnlock()
	a := s.c.archive
	if a == nil {
		return nil, ErrNoArchive
	}
	if strings.TrimSpace(request.Path) == "" {
		return nil, fmt.Errorf("请选择对比归档")
	}
	stage := a.CloneForBatch()
	ctx, cancel := context.WithCancel(context.Background())
	p := &archiveDiffSession{cancel: cancel, status: ArchiveDiffStatus{
		ID: fmt.Sprint(archiveDiffSequence.Add(1)), State: "loading", CurrentPath: a.SourcePath(), OtherPath: request.Path,
	}}
	s.mu.Lock()
	if s.session != nil {
		s.session.cancel()
	}
	s.session = p
	status := p.status
	s.mu.Unlock()
	go s.compare(ctx, p, stage, request)
	return &status, nil
}

func (s *ArchiveDiffService) Status(id string) (*ArchiveDiffStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.findLocked(id, false)
	if err != nil {
		return nil, err
	}
	status := p.status
	return &status, nil
}

func (s *ArchiveDiffService) findLocked(id string, ready bool) (*archiveDiffSession, error) {
	p := s.session
	if p == nil || p.status.ID != id {
		return nil, fmt.Errorf("对比会话已关闭，请重新比较")
	}
	if ready && p.status.State != "ready" {
		return nil, fmt.Errorf("对比尚未完成")
	}
	return p, nil
}

func (s *ArchiveDiffService) compare(ctx context.Context, p *archiveDiffSession, stage *pvf.Archive, request ArchiveDiffRequest) {
	// Serialize scans so a cancelled worker finishes releasing its parsed
	// archive before the next worker opens another potentially large PVF.
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	fail := func(err error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.session == p && ctx.Err() == nil {
			p.status.State = "error"
			p.status.Error = err.Error()
		}
	}
	for _, draft := range request.Drafts {
		if ctx.Err() != nil {
			return
		}
		if draft.FileIndex < 0 || draft.FileIndex >= stage.FileCount() || pvfversion.CanonicalPath(stage.Path(draft.FileIndex)) != pvfversion.CanonicalPath(draft.Path) {
			fail(fmt.Errorf("文件草稿已过期，请重新比较"))
			return
		}
		if err := stage.SetText(draft.FileIndex, draft.Text); err != nil {
			fail(fmt.Errorf("应用草稿 %s 失败: %w", draft.Path, err))
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	// Minimal open: parsing only, no ArchiveService/core registration or events.
	other, err := pvf.Open(request.Path)
	if err != nil {
		fail(err)
		return
	}
	if ctx.Err() != nil {
		return
	}
	total := int(stage.FileCount() + other.FileCount())
	s.mu.Lock()
	if s.session != p || ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	p.status.State = "comparing"
	p.status.Total = total
	s.mu.Unlock()
	progress := func(n int) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.session == p {
			p.status.Processed = n
		}
	}
	before, err := archiveDiffSnapshot(ctx, other, 0, progress)
	if err != nil {
		fail(err)
		return
	}
	after, err := archiveDiffSnapshot(ctx, stage, int(other.FileCount()), progress)
	if err != nil {
		fail(err)
		return
	}
	changes := pvfversion.Diff(before, after)
	children := archiveDiffChildren(changes)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != p || ctx.Err() != nil {
		return
	}
	p.current, p.other = stage, other
	p.children = children
	p.changes = make(map[string]pvfversion.FileChange, len(changes))
	for _, change := range changes {
		p.changes[change.Path] = change
		switch change.Operation {
		case pvfversion.OperationAdd:
			p.status.Added++
		case pvfversion.OperationDelete:
			p.status.Deleted++
		default:
			p.status.Modified++
		}
	}
	p.status.Processed = total
	p.status.State = "ready"
}

// Keep one CPU available for the desktop UI; small archives never spawn more
// workers than files. Do not cap CPU-bound comparison work at a fixed four.
func archiveDiffWorkerCount(files int32) int {
	return min(int(files), max(1, runtime.GOMAXPROCS(0)-1))
}

func archiveDiffSnapshot(ctx context.Context, a *pvf.Archive, offset int, progress func(int)) (pvfversion.Snapshot, error) {
	return archiveDiffSnapshotWorkers(ctx, a, offset, progress, archiveDiffWorkerCount(a.FileCount()))
}

type archiveDiffHashResult struct {
	key   string
	entry pvfversion.Entry
	err   error
}

func archiveDiffSnapshotWorkers(parent context.Context, a *pvf.Archive, offset int, progress func(int), workers int) (pvfversion.Snapshot, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	result := make(pvfversion.Snapshot, a.FileCount())
	if a.FileCount() == 0 {
		progress(offset)
		return result, nil
	}
	workers = min(int(a.FileCount()), max(1, workers))
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	// Queue indexes rather than decoded payloads. Each worker retains only the
	// file it is hashing; the bounded result queue contains compact hashes.
	jobs := make(chan int32)
	results := make(chan archiveDiffHashResult, workers)
	largeFile := make(chan struct{}, 1)
	var done sync.WaitGroup
	done.Add(workers + 1)
	go func() {
		defer done.Done()
		defer close(jobs)
		for i := int32(0); i < a.FileCount(); i++ {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer done.Done()
			for {
				var index int32
				var ok bool
				select {
				case <-ctx.Done():
					return
				case index, ok = <-jobs:
					if !ok {
						return
					}
				}
				file := a.File(index)
				// Large logical text expansion is the main transient memory risk. Allow
				// at most one >=8 MiB payload to decode/hash at a time, independently of
				// the CPU-based worker count used for ordinary files.
				large := file.DataSize >= 8<<20
				if large {
					select {
					case largeFile <- struct{}{}:
					case <-ctx.Done():
						return
					}
				}
				value := archiveDiffHashFile(ctx, a, index, file)
				if large {
					<-largeFile
				}
				select {
				case results <- value:
				case <-ctx.Done():
					return
				}
				if value.err != nil {
					return
				}
			}
		}()
	}
	go func() { done.Wait(); close(results) }()
	processed := 0
	var firstErr error
	// One collector owns the map and progress updates, keeping progress ordered
	// even when file computations finish out of order. Drain after cancellation
	// so no producer or worker survives a released comparison task.
	for value := range results {
		if firstErr != nil || ctx.Err() != nil {
			continue
		}
		if value.err != nil {
			firstErr = value.err
		} else if _, exists := result[value.key]; exists {
			firstErr = fmt.Errorf("归档包含重复路径 %q，无法唯一比较", value.entry.Path)
		}
		if firstErr != nil {
			cancel()
			continue
		}
		result[value.key] = value.entry
		processed++
		if processed == 1 || processed%64 == 0 {
			progress(offset + processed)
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	progress(offset + processed)
	return result, nil
}

func archiveDiffHashFile(ctx context.Context, a *pvf.Archive, index int32, file pvf.File) archiveDiffHashResult {
	if err := ctx.Err(); err != nil {
		return archiveDiffHashResult{err: err}
	}
	path := pvfversion.DisplayPath(a.Path(index))
	key := pvfversion.CanonicalPath(path)
	if key == "" {
		return archiveDiffHashResult{err: fmt.Errorf("归档中存在空路径")}
	}
	var data []byte
	var err error
	if file.DataType == pvf.TypeScript || file.DataType == pvf.TypeUnicode {
		var value string
		value, err = a.CanonicalText(index)
		data = []byte(value)
	} else {
		data, err = a.RawBytes(index)
	}
	if err != nil {
		return archiveDiffHashResult{err: fmt.Errorf("读取 %s 失败: %w", path, err)}
	}
	if err := ctx.Err(); err != nil {
		return archiveDiffHashResult{err: err}
	}
	return archiveDiffHashResult{key: key, entry: pvfversion.Entry{Path: path, DataType: file.DataType, Hash: archiveDiffContentHash(file.DataType, data)}}
}

// Hash the same type-prefixed logical bytes as version snapshots without
// allocating a second full payload solely to prepend the four-byte type.
func archiveDiffContentHash(dataType int32, data []byte) string {
	var prefix [4]byte
	binary.LittleEndian.PutUint32(prefix[:], uint32(dataType))
	hash := sha256.New()
	_, _ = hash.Write(prefix[:])
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func archiveDiffChildren(changes []pvfversion.FileChange) map[string][]ArchiveDiffNode {
	nodes := make(map[string]map[string]ArchiveDiffNode)
	for _, change := range changes {
		parts := strings.Split(change.DisplayPath, "/")
		parent := ""
		for i, name := range parts {
			path := strings.Join(parts[:i+1], "/")
			key := pvfversion.CanonicalPath(path)
			if nodes[parent] == nil {
				nodes[parent] = make(map[string]ArchiveDiffNode)
			}
			node := nodes[parent][key]
			node.Path = path
			node.Name = name
			node.Count++
			node.IsDir = i < len(parts)-1
			if !node.IsDir {
				node.Operation = string(change.Operation)
			}
			nodes[parent][key] = node
			parent = key
		}
	}
	result := make(map[string][]ArchiveDiffNode, len(nodes))
	for parent, entries := range nodes {
		list := make([]ArchiveDiffNode, 0, len(entries))
		for _, node := range entries {
			list = append(list, node)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].IsDir != list[j].IsDir {
				return list[i].IsDir
			}
			return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
		})
		result[parent] = list
	}
	return result
}

func (s *ArchiveDiffService) Children(id, path string) ([]ArchiveDiffNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.findLocked(id, true)
	if err != nil {
		return nil, err
	}
	return append([]ArchiveDiffNode{}, p.children[pvfversion.CanonicalPath(path)]...), nil
}

func (s *ArchiveDiffService) FileDiff(id, path string) (*ArchiveDiffFile, error) {
	// Capture references under the session lock. Parsing/detail IO never holds
	// it, so closing or cancelling remains responsive.
	s.mu.Lock()
	p, err := s.findLocked(id, true)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	change, ok := p.changes[pvfversion.CanonicalPath(path)]
	before, after := p.other, p.current
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("文件不在差异列表中")
	}
	result := &ArchiveDiffFile{Path: change.DisplayPath, Operation: string(change.Operation), BeforeHash: change.BeforeHash, AfterHash: change.AfterHash, BeforeType: change.BeforeType, AfterType: change.AfterType}
	bi, bok := before.Find(change.Path)
	ai, aok := after.Find(change.Path)
	result.BeforeExists, result.AfterExists = bok, aok
	if bok {
		result.BeforeSize = before.File(bi).DataSize
	}
	if aok {
		result.AfterSize = after.File(ai).DataSize
	}
	isText := func(typ int32) bool { return typ == pvf.TypeScript || typ == pvf.TypeUnicode }
	if (bok && !isText(result.BeforeType)) || (aok && !isText(result.AfterType)) {
		result.Message = "二进制内容不同，不支持行级文本差异"
		if bok && aok && result.BeforeType != result.AfterType {
			result.Message = "文件类型发生变化，不支持行级文本差异"
		}
		return result, nil
	}
	limit := s.c.editableByteLimit()
	if result.BeforeSize > limit || result.AfterSize > limit {
		result.Message = "文件超过文本大小阈值，仅显示差异摘要；可在设置中调整阈值"
		return result, nil
	}
	if bok {
		result.BeforeText, err = before.CanonicalText(bi)
		if err != nil {
			return nil, err
		}
	}
	if aok {
		result.AfterText, err = after.CanonicalText(ai)
		if err != nil {
			return nil, err
		}
	}
	result.TextAvailable = true
	return result, nil
}
