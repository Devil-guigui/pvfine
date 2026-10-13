// Package ruleconfig manages editable source documents and production overlays.
package ruleconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	appconfig "pvfine/config"
	"pvfine/internal/annotations"
	"pvfine/internal/rendering"
)

type Document struct {
	Version          int                                 `json:"version"`
	Description      string                              `json:"description,omitempty"`
	Rules            []json.RawMessage                   `json:"rules"`
	Fields           []json.RawMessage                   `json:"fields,omitempty"`
	Relations        map[string]annotations.RelationSpec `json:"relations,omitempty"`
	DisabledIDs      []string                            `json:"disabledIds,omitempty"`
	DisabledFieldIDs []string                            `json:"disabledFieldIds,omitempty"`
	// Dependencies retain the provenance of a field's cascading suppression.
	FieldSuppressions map[string][]string `json:"fieldSuppressions,omitempty"`
}

type Entry struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	JSON         string   `json:"json"`
	Source       string   `json:"source"`
	Overridden   bool     `json:"overridden"`
	Disabled     bool     `json:"disabled"`
	Effective    bool     `json:"effective"`
	Dependencies []string `json:"dependencies"`
}
type Snapshot struct {
	annotationEngine *annotations.Engine
	renderingEngine  *rendering.Engine
	Revision         string  `json:"revision"`
	Development      bool    `json:"development"`
	Entries          []Entry `json:"entries"`
	documents        map[string]Document
	base             Document
	merged           Document
}
type Store struct {
	builtin     map[string][]byte
	mu          sync.Mutex
	Development bool
	SourceDir   string
	UserDir     string
	// Writer may be replaced in tests to verify persistence failures.
	Writer func(string, []byte) error
}

func New(development bool, sourceDir, userDir string) *Store {
	return &Store{Development: development, SourceDir: sourceDir, UserDir: userDir, Writer: atomicWrite}
}
func decode(data []byte) (Document, error) {
	var d Document
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return d, fmt.Errorf("只能包含一个 JSON 文档")
	}
	if d.Version != 1 {
		return d, fmt.Errorf("version 必须为 1")
	}
	return d, nil
}
func identity(raw json.RawMessage) string {
	var r struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &r)
	return r.ID
}
func fieldRef(raw json.RawMessage) string {
	if !bytes.Contains(raw, []byte(`"field"`)) {
		return ""
	}
	var r struct {
		Field string `json:"field"`
	}
	_ = json.Unmarshal(raw, &r)
	return strings.TrimSpace(r.Field)
}
func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func without(xs []string, x string) []string {
	out := []string{}
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}
func upsert(xs []json.RawMessage, raw json.RawMessage) []json.RawMessage {
	for i, r := range xs {
		if identity(r) == identity(raw) {
			xs[i] = raw
			return xs
		}
	}
	return append(xs, raw)
}
func remove(xs []json.RawMessage, id string) []json.RawMessage {
	out := []json.RawMessage{}
	for _, r := range xs {
		if identity(r) != id {
			out = append(out, r)
		}
	}
	return out
}
func mergeRows(base, user []json.RawMessage, disabled []string) []json.RawMessage {
	out := []json.RawMessage{}
	seen := map[string]bool{}
	for _, r := range base {
		id := identity(r)
		seen[id] = true
		if contains(disabled, id) {
			continue
		}
		for _, u := range user {
			if identity(u) == id {
				r = u
				break
			}
		}
		out = append(out, r)
	}
	for _, r := range user {
		if !seen[identity(r)] && !contains(disabled, identity(r)) {
			out = append(out, r)
		}
	}
	return out
}
func validateOverlay(d Document, category string) error {
	for name, rows := range map[string][]json.RawMessage{"rules": d.Rules, "fields": d.Fields} {
		seen := map[string]bool{}
		for _, r := range rows {
			id := identity(r)
			if strings.TrimSpace(id) == "" || seen[id] {
				return fmt.Errorf("%s ID 为空或重复: %s", name, id)
			}
			seen[id] = true
		}
	}
	if category == "rendering" && (len(d.Fields) > 0 || len(d.Relations) > 0 || len(d.DisabledFieldIDs) > 0 || len(d.FieldSuppressions) > 0) {
		return fmt.Errorf("渲染配置不支持共享字段或关联列表")
	}
	for _, rows := range [][]string{d.DisabledIDs, d.DisabledFieldIDs} {
		seen := map[string]bool{}
		for _, id := range rows {
			if id == "" || seen[id] {
				return fmt.Errorf("屏蔽 ID 为空或重复")
			}
			seen[id] = true
		}
	}
	for field, ids := range d.FieldSuppressions {
		if !contains(d.DisabledFieldIDs, field) {
			return fmt.Errorf("字段依赖屏蔽缺少字段屏蔽: %s", field)
		}
		for _, id := range ids {
			if id == "" {
				return fmt.Errorf("依赖规则 ID 不能为空")
			}
		}
	}
	return nil
}
func (s *Store) read(category string) (*Snapshot, error) {
	if category != "annotations" && category != "rendering" {
		return nil, fmt.Errorf("未知规则类别")
	}
	if s.Development && s.SourceDir == "" {
		return nil, fmt.Errorf("开发环境未找到源规则目录")
	}
	snap := &Snapshot{Development: s.Development, Entries: []Entry{}, documents: map[string]Document{}}
	h := sha256.New()
	sources := []struct {
		name string
		data []byte
	}{{category + ".json", appconfig.AnnotationsJSON}}
	if category == "rendering" {
		sources[0].data = appconfig.RenderingJSON
	} else {
		sources = append(sources, struct {
			name string
			data []byte
		}{"paths.json", appconfig.PathsJSON}, struct {
			name string
			data []byte
		}{"sections.json", appconfig.SectionsJSON})
	}
	base := Document{Version: 1, Rules: []json.RawMessage{}}
	seenRules := map[string]bool{}
	seenFields := map[string]bool{}
	for _, src := range sources {
		data := src.data
		if override, ok := s.builtin[src.name]; ok {
			data = override
		}
		if s.Development {
			var err error
			data, err = os.ReadFile(filepath.Join(s.SourceDir, src.name))
			if err != nil {
				return nil, fmt.Errorf("读取 %s: %w", src.name, err)
			}
		}
		h.Write([]byte(src.name))
		h.Write(data)
		d, err := decode(data)
		if err != nil {
			return nil, fmt.Errorf("解析 %s: %w", src.name, err)
		}
		snap.documents[src.name] = d
		for _, r := range d.Rules {
			id := identity(r)
			if seenRules[id] {
				continue
			}
			seenRules[id] = true
			base.Rules = append(base.Rules, r)
			snap.Entries = append(snap.Entries, Entry{ID: id, Kind: "rule", JSON: string(r), Source: src.name, Dependencies: []string{}})
		}
		for _, r := range d.Fields {
			id := identity(r)
			if seenFields[id] {
				return nil, fmt.Errorf("共享字段 ID 重复: %s", id)
			}
			seenFields[id] = true
			base.Fields = append(base.Fields, r)
			snap.Entries = append(snap.Entries, Entry{ID: id, Kind: "field", JSON: string(r), Source: src.name, Dependencies: []string{}})
		}
	}
	if category == "annotations" {
		data := appconfig.ListsJSON
		if override, ok := s.builtin["lists.json"]; ok {
			data = override
		}
		if s.Development {
			var err error
			data, err = os.ReadFile(filepath.Join(s.SourceDir, "lists.json"))
			if err != nil {
				return nil, err
			}
		}
		h.Write(data)
		lists, err := annotations.ParseLists(data)
		if err != nil {
			return nil, err
		}
		base.Relations = lists.Relations
	}
	snap.base = base
	overlayName := category + ".user.json"
	overlay := Document{Version: 1, Rules: []json.RawMessage{}}
	if !s.Development {
		data, err := os.ReadFile(filepath.Join(s.UserDir, overlayName))
		if err == nil {
			overlay, err = decode(data)
			if err != nil {
				return nil, fmt.Errorf("用户规则损坏，请先修复 %s: %w", overlayName, err)
			}
			h.Write(data)
		} else if !os.IsNotExist(err) {
			return nil, err
		} else {
			h.Write([]byte("missing"))
		}
		if err := validateOverlay(overlay, category); err != nil {
			return nil, err
		}
		snap.documents[overlayName] = overlay
	}
	merged := base
	merged.Rules = mergeRows(base.Rules, overlay.Rules, overlay.DisabledIDs)
	merged.Fields = mergeRows(base.Fields, overlay.Fields, overlay.DisabledFieldIDs)
	merged.Rules = excludeFieldDependencies(merged.Rules, overlay.DisabledFieldIDs)
	for _, ids := range overlay.FieldSuppressions {
		for _, id := range ids {
			merged.Rules = remove(merged.Rules, id)
		}
	}
	if len(overlay.Relations) > 0 {
		merged.Relations = map[string]annotations.RelationSpec{}
		for k, v := range base.Relations {
			merged.Relations[k] = v
		}
		for k, v := range overlay.Relations {
			merged.Relations[k] = v
		}
	}
	ann, renderer, err := Engines(category, merged)
	if err != nil {
		return nil, err
	}
	snap.annotationEngine = ann
	snap.renderingEngine = renderer
	snap.merged = merged
	for i := range snap.Entries {
		e := &snap.Entries[i]
		rows := overlay.Rules
		disabled := overlay.DisabledIDs
		if e.Kind == "field" {
			rows = overlay.Fields
			disabled = overlay.DisabledFieldIDs
		}
		e.Disabled = contains(disabled, e.ID)
		if e.Kind == "rule" {
			for _, ids := range overlay.FieldSuppressions {
				e.Disabled = e.Disabled || contains(ids, e.ID)
			}
		}
		for _, r := range rows {
			if identity(r) == e.ID {
				e.JSON = string(r)
				e.Overridden = true
				break
			}
		}
	}
	for kind, rows := range map[string][]json.RawMessage{"rule": overlay.Rules, "field": overlay.Fields} {
		for _, r := range rows {
			exists := false
			for _, e := range snap.Entries {
				if e.Kind == kind && e.ID == identity(r) {
					exists = true
					break
				}
			}
			if !exists {
				snap.Entries = append(snap.Entries, Entry{ID: identity(r), Kind: kind, JSON: string(r), Source: overlayName, Dependencies: []string{}})
			}
		}
	}
	dependencyIDs := map[string][]string{}
	for _, r := range mergeRows(base.Rules, overlay.Rules, nil) {
		if field := fieldRef(r); field != "" {
			dependencyIDs[field] = append(dependencyIDs[field], identity(r))
		}
	}
	for i := range snap.Entries {
		e := &snap.Entries[i]
		if e.Kind == "rule" {
			e.Disabled = contains(overlay.DisabledIDs, e.ID) || contains(overlay.DisabledFieldIDs, fieldRef(json.RawMessage(e.JSON)))
			for _, ids := range overlay.FieldSuppressions {
				e.Disabled = e.Disabled || contains(ids, e.ID)
			}
		} else {
			e.Disabled = contains(overlay.DisabledFieldIDs, e.ID)
		}
		if e.Kind == "field" {
			e.Dependencies = append(e.Dependencies, dependencyIDs[e.ID]...)
		}
		e.Effective = !e.Disabled
	}
	snap.Revision = hex.EncodeToString(h.Sum(nil))
	return snap, nil
}
func Compile(category string, d Document) error {
	_, _, err := Engines(category, d)
	return err
}
func Engines(category string, d Document) (*annotations.Engine, *rendering.Engine, error) {
	if d.Rules == nil {
		d.Rules = []json.RawMessage{}
	}
	data, err := json.Marshal(d)
	if err != nil {
		return nil, nil, err
	}
	if category == "annotations" {
		doc, err := annotations.Parse(data)
		if err != nil {
			return nil, nil, err
		}
		e, err := annotations.Compile(doc)
		return e, nil, err
	}
	e, err := rendering.Parse(data)
	return nil, e, err
}
func (s *Store) Load(category string) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(category)
}
func (snap *Snapshot) Engines(category string) (*annotations.Engine, *rendering.Engine, error) {
	if (category == "annotations" && snap.annotationEngine != nil) || (category == "rendering" && snap.renderingEngine != nil) {
		return snap.annotationEngine, snap.renderingEngine, nil
	}
	return Engines(category, snap.merged)
}

type Mutation struct {
	Category string `json:"category"`
	Revision string `json:"revision"`
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Action   string `json:"action"`
	JSON     string `json:"json"`
}

func (s *Store) Mutate(m Mutation) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, err := s.read(m.Category)
	if err != nil {
		return nil, err
	}
	if m.Revision != snap.Revision {
		return nil, fmt.Errorf("规则已被外部修改，请重新加载后再保存")
	}
	if m.Kind != "rule" && m.Kind != "field" {
		return nil, fmt.Errorf("未知规则类型")
	}
	if m.Category == "rendering" && m.Kind != "rule" {
		return nil, fmt.Errorf("渲染不支持共享字段")
	}
	var entry *Entry
	for i := range snap.Entries {
		e := &snap.Entries[i]
		if e.Kind == m.Kind && e.ID == m.ID {
			entry = e
			break
		}
	}
	if m.Action != "save" && entry == nil {
		return nil, fmt.Errorf("规则不存在")
	}
	name := m.Category + ".user.json"
	if s.Development {
		name = m.Category + ".json"
		if entry != nil {
			name = entry.Source
		}
	}
	d := snap.documents[name]
	rows := &d.Rules
	if m.Kind == "field" {
		rows = &d.Fields
	}
	switch m.Action {
	case "save":
		raw := json.RawMessage(m.JSON)
		if !json.Valid(raw) || identity(raw) != m.ID || strings.TrimSpace(m.ID) == "" {
			return nil, fmt.Errorf("规则 JSON 无效，或 ID 与原规则不一致")
		}
		*rows = upsert(*rows, raw)
		if m.Kind == "field" {
			d.DisabledFieldIDs = without(d.DisabledFieldIDs, m.ID)
			delete(d.FieldSuppressions, m.ID)
		} else {
			d.DisabledIDs = without(d.DisabledIDs, m.ID)
		}
	case "delete":
		if s.Development {
			*rows = remove(*rows, m.ID)
			if m.Kind == "field" {
				for _, dependency := range snap.Entries {
					if dependency.Kind == "rule" && fieldRef(json.RawMessage(dependency.JSON)) == m.ID && dependency.Source != name {
						return nil, fmt.Errorf("共享字段有其他来源的引用，请先解除引用: %s", dependency.ID)
					}
				}
				d.Rules = excludeFieldDependencies(d.Rules, []string{m.ID})
			}
		} else if entry.Overridden || entry.Source == name {
			*rows = remove(*rows, m.ID)
			if m.Kind == "field" && entry.Source == name {
				d.Rules = excludeFieldDependencies(d.Rules, []string{m.ID})
				d.DisabledFieldIDs = without(d.DisabledFieldIDs, m.ID)
				delete(d.FieldSuppressions, m.ID)
			} else if m.Kind == "rule" && entry.Source == name {
				d.DisabledIDs = without(d.DisabledIDs, m.ID)
			}
		} else {
			disable(&d, m, snap)
		}
	case "disable":
		if s.Development {
			return nil, fmt.Errorf("开发环境请直接删除规则")
		}
		disable(&d, m, snap)
	case "restore":
		if s.Development {
			return nil, fmt.Errorf("开发环境不支持恢复内置")
		}
		if entry.Source != name {
			*rows = remove(*rows, m.ID)
		}
		if m.Kind == "field" {
			d.DisabledFieldIDs = without(d.DisabledFieldIDs, m.ID)
			delete(d.FieldSuppressions, m.ID)
		} else {
			d.DisabledIDs = without(d.DisabledIDs, m.ID)
		}
	default:
		return nil, fmt.Errorf("未知操作")
	}
	if d.Rules == nil {
		d.Rules = []json.RawMessage{}
	}
	if err := validateOverlay(d, m.Category); err != nil {
		return nil, err
	}
	// Validate the prospective configuration without persisting invalid data.
	candidate := snap.base
	if s.Development {
		candidate.Rules = nil
		candidate.Fields = nil
		known := map[string]bool{}
		names := []string{m.Category + ".json"}
		if m.Category == "annotations" {
			names = append(names, "paths.json", "sections.json")
		}
		for _, src := range names {
			doc := snap.documents[src]
			if src == name {
				doc = d
			}
			for _, r := range doc.Rules {
				if !known[identity(r)] {
					candidate.Rules = append(candidate.Rules, r)
					known[identity(r)] = true
				}
			}
			candidate.Fields = append(candidate.Fields, doc.Fields...)
		}
	} else {
		candidate.Rules = mergeRows(candidate.Rules, d.Rules, d.DisabledIDs)
		candidate.Fields = mergeRows(candidate.Fields, d.Fields, d.DisabledFieldIDs)
		candidate.Rules = excludeFieldDependencies(candidate.Rules, d.DisabledFieldIDs)
		for _, ids := range d.FieldSuppressions {
			for _, id := range ids {
				candidate.Rules = remove(candidate.Rules, id)
			}
		}
		if len(d.Relations) > 0 {
			candidate.Relations = map[string]annotations.RelationSpec{}
			for k, v := range snap.base.Relations {
				candidate.Relations[k] = v
			}
			for k, v := range d.Relations {
				candidate.Relations[k] = v
			}
		}
	}
	if err := Compile(m.Category, candidate); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	dir := s.UserDir
	if s.Development {
		dir = s.SourceDir
	}
	latest, err := s.read(m.Category)
	if err != nil {
		return nil, err
	}
	if latest.Revision != snap.Revision {
		return nil, fmt.Errorf("规则已被外部修改，请重新加载后再保存")
	}
	if err := s.Writer(filepath.Join(dir, name), append(data, '\n')); err != nil {
		return nil, fmt.Errorf("保存规则失败: %w", err)
	}
	return s.read(m.Category)
}
func disable(d *Document, m Mutation, snap *Snapshot) {
	if m.Kind == "field" {
		if !contains(d.DisabledFieldIDs, m.ID) {
			d.DisabledFieldIDs = append(d.DisabledFieldIDs, m.ID)
		}
		if d.FieldSuppressions == nil {
			d.FieldSuppressions = map[string][]string{}
		}
		ids := []string{}
		for _, r := range mergeRows(snap.base.Rules, d.Rules, nil) {
			if fieldRef(r) == m.ID {
				ids = append(ids, identity(r))
			}
		}
		d.FieldSuppressions[m.ID] = ids
	} else if !contains(d.DisabledIDs, m.ID) {
		d.DisabledIDs = append(d.DisabledIDs, m.ID)
	}
}
func atomicWrite(filename string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(filename), ".rules-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	mode := os.FileMode(0o600)
	if info, err := os.Stat(filename); err == nil {
		mode = info.Mode().Perm()
	}
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filename)
}

// Migrate imports only differences; absence in a legacy mirror is never a deletion.
func (s *Store) Migrate(category string) error {
	if s.Development {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	filename := filepath.Join(s.UserDir, category+".user.json")
	if _, err := os.Stat(filename); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	legacyData, err := os.ReadFile(filepath.Join(s.UserDir, category+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	legacy, err := decode(legacyData)
	if err != nil {
		return fmt.Errorf("旧规则迁移失败: %w", err)
	}
	snap, err := s.read(category)
	if err != nil {
		return err
	}
	overlay := Document{Version: 1, Rules: []json.RawMessage{}}
	different := func(rows, base []json.RawMessage) []json.RawMessage {
		out := []json.RawMessage{}
		for _, r := range rows {
			same := false
			for _, b := range base {
				if identity(b) == identity(r) {
					var a, c any
					_ = json.Unmarshal(r, &a)
					_ = json.Unmarshal(b, &c)
					same = reflect.DeepEqual(a, c)
					break
				}
			}
			if !same {
				out = append(out, r)
			}
		}
		return out
	}
	overlay.Rules = different(legacy.Rules, snap.base.Rules)
	overlay.Fields = different(legacy.Fields, snap.base.Fields)
	if category == "annotations" {
		relations := legacy.Relations
		if data, e := os.ReadFile(filepath.Join(s.UserDir, "lists.json")); e == nil {
			lists, e := annotations.ParseLists(data)
			if e != nil {
				return e
			}
			relations = lists.Relations
		} else if !os.IsNotExist(e) {
			return e
		}
		for k, v := range relations {
			if b, ok := snap.base.Relations[k]; !ok || !reflect.DeepEqual(v, b) {
				if overlay.Relations == nil {
					overlay.Relations = map[string]annotations.RelationSpec{}
				}
				overlay.Relations[k] = v
			}
		}
	}
	if err := validateOverlay(overlay, category); err != nil {
		return err
	}
	candidate := snap.base
	candidate.Rules = mergeRows(candidate.Rules, overlay.Rules, nil)
	candidate.Fields = mergeRows(candidate.Fields, overlay.Fields, nil)
	if len(overlay.Relations) > 0 {
		candidate.Relations = map[string]annotations.RelationSpec{}
		for k, v := range snap.base.Relations {
			candidate.Relations[k] = v
		}
		for k, v := range overlay.Relations {
			candidate.Relations[k] = v
		}
	}
	if err := Compile(category, candidate); err != nil {
		return fmt.Errorf("旧规则迁移失败: %w", err)
	}
	data, err := json.MarshalIndent(overlay, "", "  ")
	if err != nil {
		return err
	}
	return s.Writer(filename, append(data, '\n'))
}

// Matches follows the archive-relative, case-insensitive glob semantics.
func Matches(glob, file string, extensions []string) bool {
	file = strings.ToLower(strings.Trim(strings.ReplaceAll(file, "\\", "/"), "/"))
	if len(extensions) > 0 {
		ok := false
		for _, e := range extensions {
			ok = ok || strings.EqualFold(strings.TrimSpace(e), path.Ext(file))
		}
		if !ok {
			return false
		}
	}
	if strings.TrimSpace(glob) == "" {
		return true
	}
	parts := strings.Split(strings.ToLower(strings.Trim(strings.ReplaceAll(glob, "\\", "/"), "/")), "/")
	segments := strings.Split(file, "/")
	var match func(int, int) bool
	match = func(i, j int) bool {
		if i == len(parts) {
			return j == len(segments)
		}
		if parts[i] == "**" {
			return match(i+1, j) || (j < len(segments) && match(i, j+1))
		}
		if j == len(segments) {
			return false
		}
		ok, _ := path.Match(parts[i], segments[j])
		return ok && match(i+1, j+1)
	}
	return match(0, 0)
}

func excludeFieldDependencies(rows []json.RawMessage, disabled []string) []json.RawMessage {
	result := []json.RawMessage{}
	for _, row := range rows {
		if !contains(disabled, fieldRef(row)) {
			result = append(result, row)
		}
	}
	return result
}
