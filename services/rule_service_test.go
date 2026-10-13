package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pvfine/internal/pvf"
	"pvfine/internal/ruleconfig"
)

func ruleServiceFixture(t *testing.T) (*core, *RuleService) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"annotations.json": `{"version":1,"rules":[{"id":"path","match":{"glob":"a.etc"},"target":{"kind":"path"},"annotation":{"type":"text","title":"old"}},{"id":"section","match":{"extensions":[".etc"]},"target":{"kind":"section","section":"name"},"annotation":{"type":"text","title":"old"}}]}`,
		"rendering.json":   `{"version":1,"rules":[{"id":"name","match":{},"target":{"kind":"section","section":"name"},"format":{"tokensPerLine":2}}]}`,
		"paths.json":       `{"version":1,"rules":[]}`, "sections.json": `{"version":1,"rules":[]}`, "lists.json": `{"version":1,"relations":{}}`,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := newCore()
	c.ruleStore = ruleconfig.New(true, dir, t.TempDir())
	service := NewRuleService(c)
	for _, category := range []string{"annotations", "rendering"} {
		if err := service.reload(category); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(c.closeArchive)
	return c, service
}
func TestRuleServiceSaveWithoutArchiveAndRejectStaleRevision(t *testing.T) {
	c, s := ruleServiceFixture(t)
	q, err := s.Query(RuleTarget{Category: "annotations", Path: "a.etc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Entries) != 1 || !q.Entries[0].Effective {
		t.Fatalf("query = %#v", q)
	}
	m := ruleconfig.Mutation{Category: "annotations", Revision: q.Revision, Kind: "rule", ID: "path", Action: "save", JSON: `{"id":"path","match":{"glob":"a.etc"},"target":{"kind":"path"},"annotation":{"type":"text","title":"new"}}`}
	if err := s.Apply(m); err != nil {
		t.Fatal(err)
	}
	if got := c.annotationEngine.AnnotatePath("a.etc", false); len(got) != 1 || got[0].Title != "new" {
		t.Fatalf("inactive save: %#v", got)
	}
	if err := s.Apply(m); err == nil {
		t.Fatal("stale write accepted")
	}
}
func TestRuleServiceUpdatesArchiveAndPreservesDraft(t *testing.T) {
	c, s := ruleServiceFixture(t)
	a := pvf.New()
	index := mustAddText(t, a, "a.etc", "[name]\n1 2 3 4", pvf.TypeScript)
	if err := c.setArchive(a); err != nil {
		t.Fatal(err)
	}
	editor := NewEditorService(c)
	if err := editor.SetText(index, "[name]\n1 2 3 4 5"); err != nil {
		t.Fatal(err)
	}
	q, err := s.Query(RuleTarget{Category: "rendering", Path: "a.etc", Section: "name"})
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Entries) != 1 || !q.Entries[0].Effective {
		t.Fatalf("query=%#v", q)
	}
	if err := s.Apply(ruleconfig.Mutation{Category: "rendering", Revision: q.Revision, Kind: "rule", ID: "name", Action: "save", JSON: `{"id":"name","match":{},"target":{"kind":"section","section":"name"},"format":{"tokensPerLine":1}}`}); err != nil {
		t.Fatal(err)
	}
	meta, err := editor.GetFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Text != "[name]\n1 2 3 4 5" {
		t.Fatalf("draft replaced: %q", meta.Text)
	}
	if c.renderingEngine.SectionFormat("a.etc", "name").TokensPerLine != 1 {
		t.Fatal("renderer not activated")
	}
	q, err = s.Query(RuleTarget{Category: "annotations", Path: "a.etc", Section: "name"})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	json.Unmarshal([]byte(q.Entries[0].JSON), &raw)
	raw["annotation"] = map[string]any{"type": "text", "title": "changed"}
	data, _ := json.Marshal(raw)
	if err := s.Apply(ruleconfig.Mutation{Category: "annotations", Revision: q.Revision, Kind: "rule", ID: "section", Action: "save", JSON: string(data)}); err != nil {
		t.Fatal(err)
	}
	anns, err := editor.GetAnnotations(index)
	if err != nil || len(anns) == 0 || anns[0].Title != "changed" {
		t.Fatalf("annotations = %#v, %v", anns, err)
	}
}
func TestRuleServiceInvalidReloadPreservesEngine(t *testing.T) {
	c, s := ruleServiceFixture(t)
	previous := c.renderingEngine
	if err := os.WriteFile(filepath.Join(c.ruleStore.SourceDir, "rendering.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRenderingService(c).ReloadRules(); err == nil {
		t.Fatal("corruption accepted")
	}
	if c.renderingEngine != previous {
		t.Fatal("reload changed active engine")
	}
	if _, err := s.Query(RuleTarget{Category: "rendering", Path: "a.etc"}); err == nil {
		t.Fatal("damaged file editable")
	}
}

func TestRuleStartupFallsBackForDamagedUserConfiguration(t *testing.T) {
	c := newCore()
	previous := c.renderingEngine
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rendering.user.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.loadInitialRules(ruleconfig.New(false, "", dir))
	if c.renderingEngine != previous || c.renderingErr != nil || len(c.ruleWarnings) == 0 {
		t.Fatal("startup did not retain embedded renderer with warning")
	}
	if _, err := NewRenderingService(c).ReloadRules(); err == nil {
		t.Fatal("damaged user configuration accepted on reload")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "rendering.user.json"))
	if string(data) != "{" {
		t.Fatal("damaged configuration overwritten")
	}
}

func TestRuleDraftAnnotationsDoNotCommitText(t *testing.T) {
	c, _ := ruleServiceFixture(t)
	a := pvf.New()
	index := mustAddText(t, a, "a.etc", "[name]\n1", pvf.TypeScript)
	if err := c.setArchive(a); err != nil {
		t.Fatal(err)
	}
	before, _ := a.Text(index)
	wasModified := a.IsModified(index)
	cached := c.editorText[index]
	draft := "# draft\n[name]\n2"
	anns, err := NewEditorService(c).GetAnnotationsForText(index, draft)
	if err != nil || len(anns) == 0 || anns[0].Start < 8 {
		t.Fatalf("draft annotations=%#v, %v", anns, err)
	}
	after, _ := a.Text(index)
	if before != after || a.IsModified(index) != wasModified || c.editorText[index] != cached {
		t.Fatal("annotation refresh committed frontend draft")
	}
}
