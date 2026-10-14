package services

import (
	"fmt"
	"sort"

	pvfversion "pvfine/internal/version"
)

// The caller holds the core read lock. Take immutable snapshot references
// under the diff lock, then release it before decoding export content.
func (s *ExportService) archiveDiffExportSource(r ExportRequest) (*exportSource, []exportChange, error) {
	diff := s.c.archiveDiff
	if diff == nil {
		return nil, nil, fmt.Errorf("没有可导出的归档差异")
	}
	diff.mu.Lock()
	p, err := diff.findLocked(r.DiffID, true)
	if err != nil {
		diff.mu.Unlock()
		return nil, nil, err
	}
	before, after := p.other, p.current
	changes := make([]pvfversion.FileChange, 0, len(p.changes))
	for _, change := range p.changes {
		changes = append(changes, change)
	}
	diff.mu.Unlock()
	if r.Reverse {
		before, after = after, before
	}
	source := &exportSource{archive: after, canonical: true}
	baseline := &exportSource{archive: before, canonical: true}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	result := make([]exportChange, 0, len(changes))
	for _, change := range changes {
		operation := change.Operation
		if r.Reverse {
			switch operation {
			case pvfversion.OperationAdd:
				operation = pvfversion.OperationDelete
			case pvfversion.OperationDelete:
				operation = pvfversion.OperationAdd
			}
		}
		item := exportChange{}
		if operation == pvfversion.OperationDelete {
			if r.Mode == "mod" {
				i, ok := before.Find(change.Path)
				if !ok {
					return nil, nil, fmt.Errorf("差异基线文件不存在: %s", change.Path)
				}
				typ, _, _ := before.FileContentInfo(i)
				item.before = &exportContent{path: before.Path(i), dataType: typ}
			} else {
				item.before, err = baseline.read(change.Path)
			}
		} else {
			item.after, err = source.read(change.Path)
			if err == nil && r.Mode == "mod" && operation == pvfversion.OperationModify && isExportMergeTable(change.Path) {
				item.before, err = baseline.read(change.Path)
			}
		}
		if err != nil {
			return nil, nil, err
		}
		result = append(result, item)
	}
	return source, result, nil
}
