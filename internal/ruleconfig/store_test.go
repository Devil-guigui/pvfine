package ruleconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const annotationFixture = `{"version":1,"fields":[{"id":"shared","match":{"glob":"a/test.etc"},"target":{"kind":"token","section":"name","index":0},"annotation":{"type":"text","title":"shared"},"preview":{"provider":"equ","role":"name","group":"header","format":"text"}}],"rules":[{"id":"note","match":{"glob":"a/test.etc"},"target":{"kind":"path"},"annotation":{"type":"text","title":"builtin"}},{"id":"dependent","field":"shared","match":{},"target":{},"annotation":{"title":"","type":""}}]}`
const renderFixture = `{"version":1,"rules":[{"id":"format","match":{},"target":{"kind":"section","section":"name"},"format":{"tokensPerLine":2}},{"id":"other","match":{},"target":{"kind":"section","section":"other"},"format":{"tokensPerLine":1}}]}`

func fixtureStore(t *testing.T, development bool) *Store {
	t.Helper()
	s := New(development, t.TempDir(), t.TempDir())
	s.builtin = map[string][]byte{"annotations.json": []byte(annotationFixture), "rendering.json": []byte(renderFixture), "paths.json": []byte(`{"version":1,"rules":[{"id":"generated","match":{"glob":"b"},"target":{"kind":"path"},"annotation":{"type":"text","title":"generated"}}]}`), "sections.json": []byte(`{"version":1,"rules":[]}`), "lists.json": []byte(`{"version":1,"relations":{}}`)}
	for name, data := range s.builtin {
		if err := os.WriteFile(filepath.Join(s.SourceDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
func load(t *testing.T, s *Store, category string) *Snapshot {
	t.Helper()
	snap, err := s.Load(category)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}
func apply(t *testing.T, s *Store, category, kind, id, action, raw string) *Snapshot {
	t.Helper()
	snap := load(t, s, category)
	next, err := s.Mutate(Mutation{Category: category, Revision: snap.Revision, Kind: kind, ID: id, Action: action, JSON: raw})
	if err != nil {
		t.Fatal(err)
	}
	return next
}
func row(t *testing.T, snap *Snapshot, id string) Entry {
	t.Helper()
	for _, e := range snap.Entries {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("missing entry %s", id)
	return Entry{}
}
func TestProductionOverrideOrderDisableRestore(t *testing.T) {
	s := fixtureStore(t, false)
	override := `{"id":"format","match":{},"target":{"kind":"section","section":"name"},"format":{"tokensPerLine":5}}`
	snap := apply(t, s, "rendering", "rule", "format", "save", override)
	_, engine, err := snap.Engines("rendering")
	if err != nil {
		t.Fatal(err)
	}
	if engine.SectionFormat("a/test.etc", "name").TokensPerLine != 5 || identity(snap.merged.Rules[0]) != "format" || !row(t, snap, "format").Overridden {
		t.Fatal("override not in original position")
	}
	snap = apply(t, s, "rendering", "rule", "new", "save", strings.Replace(override, `"format"`, `"new"`, 1))
	if len(snap.merged.Rules) != 3 || identity(snap.merged.Rules[2]) != "new" {
		t.Fatal("different IDs must coexist")
	}
	snap = apply(t, s, "rendering", "rule", "format", "delete", "")
	if row(t, snap, "format").Overridden {
		t.Fatal("delete override must restore builtin")
	}
	snap = apply(t, s, "rendering", "rule", "format", "disable", "")
	if !row(t, snap, "format").Disabled || len(snap.merged.Rules) != 2 {
		t.Fatal("builtin not suppressed")
	}
	snap = apply(t, s, "rendering", "rule", "format", "restore", "")
	if row(t, snap, "format").Disabled {
		t.Fatal("builtin not restored")
	}
	apply(t, s, "rendering", "rule", "new", "delete", "")
	data, err := os.ReadFile(filepath.Join(s.SourceDir, "rendering.json"))
	if err != nil || string(data) != renderFixture {
		t.Fatal("production changed source")
	}
}
func TestSharedFieldCascadePreservesIndependentSuppression(t *testing.T) {
	s := fixtureStore(t, false)
	apply(t, s, "annotations", "rule", "dependent", "disable", "")
	snap := apply(t, s, "annotations", "field", "shared", "disable", "")
	if !row(t, snap, "dependent").Disabled || len(snap.merged.Fields) != 0 {
		t.Fatal("field/dependency not suppressed")
	}
	snap = apply(t, s, "annotations", "field", "shared", "restore", "")
	if row(t, snap, "shared").Disabled || !row(t, snap, "dependent").Disabled {
		t.Fatal("restoring field erased independent rule suppression")
	}
	apply(t, s, "annotations", "rule", "dependent", "restore", "")
	apply(t, s, "annotations", "field", "shared", "disable", "")
	snap = apply(t, s, "annotations", "field", "shared", "restore", "")
	if row(t, snap, "dependent").Disabled {
		t.Fatal("cascade not restored")
	}
}
func TestDevelopmentWritesOriginalSource(t *testing.T) {
	s := fixtureStore(t, true)
	apply(t, s, "annotations", "rule", "generated", "save", `{"id":"generated","match":{"glob":"b"},"target":{"kind":"path"},"annotation":{"type":"text","title":"edited"}}`)
	data, _ := os.ReadFile(filepath.Join(s.SourceDir, "paths.json"))
	if !strings.Contains(string(data), "edited") {
		t.Fatal("did not write generated source")
	}
	data, _ = os.ReadFile(filepath.Join(s.SourceDir, "annotations.json"))
	if string(data) != annotationFixture {
		t.Fatal("rewrote wrong source")
	}
	apply(t, s, "annotations", "rule", "generated", "delete", "")
	snap := load(t, s, "annotations")
	for _, e := range snap.Entries {
		if e.ID == "generated" {
			t.Fatal("not deleted")
		}
	}
	if files, _ := os.ReadDir(s.UserDir); len(files) != 0 {
		t.Fatal("development wrote user directory")
	}
	s.SourceDir = ""
	if _, err := s.Load("annotations"); err == nil {
		t.Fatal("missing source must fail")
	}
}
func TestMutationConflictInvalidInputAndWriteFailure(t *testing.T) {
	s := fixtureStore(t, false)
	snap := load(t, s, "rendering")
	invalid := Mutation{Category: "rendering", Revision: snap.Revision, Kind: "rule", ID: "format", Action: "save", JSON: `{"id":"format","match":{},"target":{"kind":"section","section":"name"},"format":{"tokensPerLine":0}}`}
	if _, err := s.Mutate(invalid); err == nil {
		t.Fatal("invalid rule accepted")
	}
	if _, err := os.Stat(filepath.Join(s.UserDir, "rendering.user.json")); !os.IsNotExist(err) {
		t.Fatal("invalid rule persisted")
	}
	valid := invalid
	valid.JSON = strings.Replace(invalid.JSON, `"tokensPerLine":0`, `"tokensPerLine":3`, 1)
	s.Writer = func(string, []byte) error { return errors.New("disk full") }
	if _, err := s.Mutate(valid); err == nil {
		t.Fatal("write failure hidden")
	}
	s.Writer = atomicWrite
	apply(t, s, "rendering", "rule", "format", "save", valid.JSON)
	if _, err := s.Mutate(valid); err == nil || !strings.Contains(err.Error(), "外部修改") {
		t.Fatal("stale revision accepted")
	}
	path := filepath.Join(s.UserDir, "rendering.user.json")
	os.WriteFile(path, []byte("{"), 0o600)
	if _, err := s.Mutate(valid); err == nil {
		t.Fatal("corrupted overlay overwritten")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{" {
		t.Fatal("damaged file changed")
	}
}
func TestLegacyMigrationImportsDifferencesAndIsIdempotent(t *testing.T) {
	s := fixtureStore(t, false)
	legacy := strings.Replace(renderFixture, `"tokensPerLine":2`, `"tokensPerLine":7`, 1)
	legacyPath := filepath.Join(s.UserDir, "rendering.json")
	os.WriteFile(legacyPath, []byte(legacy), 0o600)
	if err := s.Migrate("rendering"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(s.UserDir, "rendering.user.json"))
	var overlay Document
	if err := json.Unmarshal(data, &overlay); err != nil {
		t.Fatal(err)
	}
	if len(overlay.Rules) != 1 || len(overlay.DisabledIDs) != 0 {
		t.Fatal("migration copied unchanged entries")
	}
	if err := s.Migrate("rendering"); err != nil {
		t.Fatal(err)
	}
	next, _ := os.ReadFile(filepath.Join(s.UserDir, "rendering.user.json"))
	if string(data) != string(next) {
		t.Fatal("migration not idempotent")
	}
	old, _ := os.ReadFile(legacyPath)
	if string(old) != legacy {
		t.Fatal("legacy changed")
	}
	// An omitted builtin in a legacy mirror is not a deletion.
	s = fixtureStore(t, false)
	os.WriteFile(filepath.Join(s.UserDir, "rendering.json"), []byte(`{"version":1,"rules":[]}`), 0o600)
	if err := s.Migrate("rendering"); err != nil {
		t.Fatal(err)
	}
	if len(load(t, s, "rendering").merged.Rules) != 2 {
		t.Fatal("missing legacy rows became deletions")
	}
}
func TestLegacyRelationsRemainAvailableReadOnly(t *testing.T) {
	s := fixtureStore(t, false)
	legacy := `{"version":1,"rules":[{"id":"ref","match":{},"target":{"kind":"token","section":"name","index":0},"annotation":{"type":"reference","title":"ref","relation":"custom"}}]}`
	os.WriteFile(filepath.Join(s.UserDir, "annotations.json"), []byte(legacy), 0o600)
	os.WriteFile(filepath.Join(s.UserDir, "lists.json"), []byte(`{"version":1,"relations":{"custom":{"listPath":"test.lst","idToken":0,"pathToken":1,"recordTokens":2,"nameSection":"name"}}}`), 0o600)
	if err := s.Migrate("annotations"); err != nil {
		t.Fatal(err)
	}
	snap := load(t, s, "annotations")
	if _, ok := snap.merged.Relations["custom"]; !ok {
		t.Fatal("legacy relation lost")
	}
}
func TestSuppressedFieldHandlesNewBuiltinDependencies(t *testing.T) {
	s := fixtureStore(t, false)
	apply(t, s, "annotations", "field", "shared", "disable", "")
	var d Document
	json.Unmarshal(s.builtin["annotations.json"], &d)
	d.Rules = append(d.Rules, json.RawMessage(`{"id":"new-dependent","field":"shared","match":{},"target":{},"annotation":{"title":"","type":""}}`))
	data, _ := json.Marshal(d)
	s.builtin["annotations.json"] = data
	snap := load(t, s, "annotations")
	if !row(t, snap, "new-dependent").Disabled {
		t.Fatal("upgrade dependency not suppressed")
	}
}
func TestMatches(t *testing.T) {
	for _, tc := range []struct {
		glob, file string
		ext        []string
		want       bool
	}{{"a/**", "a/deep/x.etc", nil, true}, {"a/**", "a2/x.etc", nil, false}, {"a/*", "a/deep/x.etc", nil, false}, {"", "A/X.ETC", []string{".etc"}, true}, {"a", "a/child", nil, false}} {
		if got := Matches(tc.glob, tc.file, tc.ext); got != tc.want {
			t.Fatalf("%q %q: %v", tc.glob, tc.file, got)
		}
	}
}

func TestUserFieldSuppressRestoreAndCascadeDelete(t *testing.T) {
	s := fixtureStore(t, false)
	field := `{"id":"custom","match":{},"target":{"kind":"token","section":"custom","index":0},"annotation":{"title":"custom","type":"text"}}`
	apply(t, s, "annotations", "field", "custom", "save", field)
	apply(t, s, "annotations", "rule", "custom-ref", "save", `{"id":"custom-ref","field":"custom","match":{},"target":{},"annotation":{"title":"","type":""}}`)
	snap := apply(t, s, "annotations", "field", "custom", "disable", "")
	if !row(t, snap, "custom").Disabled || !row(t, snap, "custom-ref").Disabled {
		t.Fatal("user field/dependency not suppressed")
	}
	snap = apply(t, s, "annotations", "field", "custom", "restore", "")
	if row(t, snap, "custom").Disabled || row(t, snap, "custom-ref").Disabled {
		t.Fatal("user field/dependency not restored")
	}
	snap = apply(t, s, "annotations", "field", "custom", "delete", "")
	for _, e := range snap.Entries {
		if e.ID == "custom" || e.ID == "custom-ref" {
			t.Fatal("user field/dependency not deleted")
		}
	}
}
func TestDevelopmentFieldDeletionRemovesSameSourceDependencies(t *testing.T) {
	s := fixtureStore(t, true)
	snap := apply(t, s, "annotations", "field", "shared", "delete", "")
	for _, e := range snap.Entries {
		if e.ID == "shared" || e.ID == "dependent" {
			t.Fatal("source field/dependency not deleted")
		}
	}
}
