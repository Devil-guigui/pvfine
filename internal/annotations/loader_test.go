package annotations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "pvfine/config"
	"pvfine/internal/pvf"
)

func TestParseRejectsUnsupportedVersionAndUnknownFields(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "version",
			json: `{"version":2,"relations":{},"rules":[]}`,
			want: "version 必须为 1",
		},
		{
			name: "unknown field",
			json: `{"version":1,"relations":{},"rules":[],"extra":true}`,
			want: "unknown field",
		},
		{
			name: "multiple documents",
			json: `{"version":1,"relations":{},"rules":[]} {}`,
			want: "只能包含一个 JSON 文档",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.json))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateRejectsInvalidRuleFields(t *testing.T) {
	index := -1
	document := Document{
		Version:   1,
		Relations: map[string]RelationSpec{},
		Rules: []Rule{{
			ID:         "invalid",
			Match:      MatchSpec{Extensions: []string{"equ"}},
			Target:     TargetSpec{Kind: "token", Section: "rarity", Index: &index},
			Annotation: AnnotationSpec{Title: "", Type: "reference", Relation: "missing"},
		}},
	}
	err := Validate(document)
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"以点开头", "不能为负数", "不存在的 relation"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want %q", err, want)
		}
	}
}

func TestValidateTokenGroupingFields(t *testing.T) {
	dynamicIndex := 0
	valid := Rule{
		ID: "records",
		Target: TargetSpec{
			Kind: "token", Section: "records", Index: intPtr(1),
			Offset: 1, RecordTokens: 3, TokensPerLineIndex: &dynamicIndex,
		},
		Annotation: AnnotationSpec{Title: "记录", Type: "text"},
	}
	if err := Validate(Document{Version: 1, Rules: []Rule{valid}}); err != nil {
		t.Fatalf("valid grouping target rejected: %v", err)
	}

	tests := []struct {
		name string
		edit func(*Rule)
		want string
	}{
		{
			name: "negative offset",
			edit: func(rule *Rule) { rule.Target.Offset = -1 },
			want: "offset 不能为负数",
		},
		{
			name: "negative group offset",
			edit: func(rule *Rule) { rule.Target.GroupOffset = -1 },
			want: "groupOffset 不能为负数",
		},
		{
			name: "negative dynamic index",
			edit: func(rule *Rule) {
				index := -1
				rule.Target.TokensPerLineIndex = &index
			},
			want: "tokensPerLineIndex 不能为负数",
		},
		{
			name: "standalone values need record width",
			edit: func(rule *Rule) {
				rule.Target.Offset = 0
				rule.Target.TokensPerLineIndex = nil
				rule.Target.RecordTokens = 0
				rule.Target.StandaloneValues = []int32{-1}
			},
			want: "需要配置正数 recordTokens",
		},
		{
			name: "missing fallback",
			edit: func(rule *Rule) { rule.Target.RecordTokens = 0 },
			want: "需要配置正数 recordTokens",
		},
		{
			name: "range target",
			edit: func(rule *Rule) {
				rule.Target.Index = nil
				rule.Target.Range = &TokenRange{Start: 0, EndExclusive: 1}
			},
			want: "需要配合单个 index 使用",
		},
		{
			name: "non-token target",
			edit: func(rule *Rule) {
				rule.Target.Kind = "section"
				rule.Target.Section = "records"
			},
			want: "只允许用于 token 标注",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule := valid
			test.edit(&rule)
			err := Validate(Document{Version: 1, Rules: []Rule{rule}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRuleGroupRoundTrip(t *testing.T) {
	for _, group := range []string{"", "装备"} {
		document := Document{Version: 1, Rules: []Rule{{
			ID: "name", Group: group, Target: TargetSpec{Kind: "section", Section: "name"},
			Annotation: AnnotationSpec{Title: "名称", Type: "text"},
		}}}
		data, err := Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Rules[0].Group != group {
			t.Fatalf("group = %q", parsed.Rules[0].Group)
		}
	}
}

func TestValidateInlineImageRequiresImageAnnotation(t *testing.T) {
	document := Document{Version: 1, Rules: []Rule{{
		ID:         "text",
		Target:     TargetSpec{Kind: "section", Section: "name"},
		Annotation: AnnotationSpec{Title: "名称", Type: "text", InlineImage: true},
	}}}
	err := Validate(document)
	if err == nil || !strings.Contains(err.Error(), "inlineImage 只允许用于 image 标注") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseMigratesLegacyImageTokenOrder(t *testing.T) {
	data := []byte(`{"version":1,"rules":[{"id":"icon","target":{"kind":"token","section":"icon","index":0,"recordTokens":2,"imageIndexToken":1},"annotation":{"title":"图标","type":"image"}}]}`)
	document, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	target := document.Rules[0].Target
	if target.Index == nil || *target.Index != 1 || target.ImagePathToken == nil || *target.ImagePathToken != 0 || target.ImageIndexToken != nil {
		t.Fatalf("migrated target = %#v", target)
	}
	formatted, err := MarshalRules(document)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(formatted), "imagePathToken") || strings.Contains(string(formatted), "imageIndexToken") {
		t.Fatalf("formatted target = %s", formatted)
	}
}

func TestLoadDefaultIncludesContextualSkillRelation(t *testing.T) {
	engine, err := LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	relation, ok := engine.Relation("技能")
	if !ok || relation.Kind != "contextual" || relation.ContextPaths["[fighter]"] == "" {
		t.Fatalf("skill relation = %#v, found=%v", relation, ok)
	}
	union, ok := engine.Relation("物品")
	if !ok || union.Kind != "union" || len(union.Relations) != 2 {
		t.Fatalf("item union relation = %#v, found=%v", union, ok)
	}
}

func TestMarshalRulesSeparatesRelations(t *testing.T) {
	document := Document{
		Version: 1,
		Relations: map[string]RelationSpec{
			"equipment": {ListPath: "equipment/equipment.lst", IDToken: 0, PathToken: 1, RecordTokens: 2, NameSection: "name"},
		},
		Rules: []Rule{{
			ID: "name", Target: TargetSpec{Kind: "section", Section: "name"},
			Annotation: AnnotationSpec{Title: "名称", Type: "text"},
		}},
	}
	data, err := MarshalRules(document)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "relations") {
		t.Fatalf("rules JSON contains relations: %s", data)
	}
	parsed, err := ParseRules(data)
	if err != nil || parsed.Relations != nil {
		t.Fatalf("parsed rules = %#v, err = %v", parsed, err)
	}
}

func TestParseLists(t *testing.T) {
	lists, err := ParseLists([]byte(`{"version":1,"relations":{"equipment":{"listPath":"equipment/equipment.lst","idToken":0,"pathToken":1,"recordTokens":2,"nameSection":"name"},"物品":{"kind":"union","relations":["装备","道具"]},"装备":{"listPath":"equipment/equipment.lst","idToken":0,"pathToken":1,"recordTokens":2,"nameSection":"name"},"道具":{"listPath":"stackable/stackable.lst","idToken":0,"pathToken":1,"recordTokens":2,"nameSection":"name"}}}`))
	if err != nil || lists.Relations["equipment"].ListPath != "equipment/equipment.lst" || lists.Relations["物品"].Kind != "union" {
		t.Fatalf("lists = %#v, err = %v", lists, err)
	}
}

func TestLoadFileMergesSiblingLists(t *testing.T) {
	dir := t.TempDir()
	rulesPath := filepath.Join(dir, "annotations.json")
	if err := os.WriteFile(rulesPath, []byte(`{"version":1,"rules":[{"id":"ref","target":{"kind":"token","section":"related","index":0},"annotation":{"title":"关联","type":"reference","relation":"equipment"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lists.json"), []byte(`{"version":1,"relations":{"equipment":{"listPath":"equipment/equipment.lst","idToken":0,"pathToken":1,"recordTokens":2,"nameSection":"name"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	engine, err := LoadFile(rulesPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.Relation("equipment"); !ok {
		t.Fatal("sibling lists.json was not merged")
	}
}

func TestLoadDefaultMergesPathNotes(t *testing.T) {
	paths, err := ParseRules(appconfig.PathsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths.Rules) == 0 {
		t.Fatal("config/paths.json has no rules")
	}
	engine, err := LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	merged := make(map[string]bool)
	for _, rule := range engine.Document().Rules {
		if rule.Target.Kind == "path" {
			merged[rule.ID] = true
		}
	}
	for _, rule := range paths.Rules {
		if !merged[rule.ID] {
			t.Fatalf("path rule %s was not merged", rule.ID)
		}
	}
	tests := []struct {
		path  string
		isDir bool
		title string
	}{
		{"equipment/equipment.lst", false, "装备列表"},
		{"equipment", true, "装备"},
		{"list/npc.lst", false, "NPC列表"},
		{"string", true, "字符串表目录"},
		{"etc", true, "杂项文件"},
		{"etc/advancealtar", true, "守护者祭坛"},
		// A generated directory note describes that directory only: paths without
		// a rule of their own stay unannotated instead of inheriting an ancestor.
		{"etc/100lvfarming", true, ""},
		{"etc/100lvfarming/anything.dat", false, ""},
		{"etc/advancealtar/unknown.dat", false, ""},
		// The hand-maintained rule in annotations.json keeps the primary title.
		{"etc/itemdropinfo_monster_hell.etc", false, "深渊掉落"},
		{"unknown/path.dat", false, ""},
	}
	for _, tc := range tests {
		results := engine.AnnotatePath(tc.path, tc.isDir)
		title := ""
		if len(results) > 0 {
			title = results[0].Title
		}
		if title != tc.title {
			t.Fatalf("%s: title = %q, want %q", tc.path, title, tc.title)
		}
	}
}

func TestLoadFilePrefersSiblingPaths(t *testing.T) {
	dir := t.TempDir()
	rulesPath := filepath.Join(dir, "annotations.json")
	if err := os.WriteFile(rulesPath, []byte(`{"version":1,"rules":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "paths.json"), []byte(`{"version":1,"rules":[{"id":"path.sibling","match":{"glob":"equipment/**"},"target":{"kind":"path"},"annotation":{"title":"本地目录说明","type":"text"},"group":"path"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sections.json"), []byte(`{"version":1,"rules":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	engine, err := LoadFile(rulesPath)
	if err != nil {
		t.Fatal(err)
	}
	results := engine.AnnotatePath("equipment/equipment.lst", false)
	if len(results) != 1 || results[0].Title != "本地目录说明" {
		t.Fatalf("results = %#v", results)
	}
	if len(engine.Document().Rules) != 1 {
		t.Fatalf("sibling paths.json should replace the built-in path notes, got %d rules", len(engine.Document().Rules))
	}
}

func TestLoadFileFallsBackToBuiltinGeneratedRules(t *testing.T) {
	dir := t.TempDir()
	rulesPath := filepath.Join(dir, "annotations.json")
	if err := os.WriteFile(rulesPath, []byte(`{"version":1,"rules":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	engine, err := LoadFile(rulesPath)
	if err != nil {
		t.Fatal(err)
	}
	if results := engine.AnnotatePath("equipment/equipment.lst", false); len(results) != 1 || results[0].Title != "装备列表" {
		t.Fatalf("results = %#v", results)
	}
	if results := engine.Annotate("a.act", pvf.ParseScriptView("[accelerate]\n100 5"), nil); len(results) != 1 || results[0].Title != "加速参数" {
		t.Fatalf("section results = %#v", results)
	}
}

func TestLoadDefaultMergesSectionNotes(t *testing.T) {
	sections, err := ParseRules(appconfig.SectionsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections.Rules) == 0 {
		t.Fatal("config/sections.json has no rules")
	}
	engine, err := LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	merged := make(map[string]bool)
	for _, rule := range engine.Document().Rules {
		merged[rule.ID] = true
	}
	for _, rule := range sections.Rules {
		if !merged[rule.ID] {
			t.Fatalf("section rule %s was not merged", rule.ID)
		}
	}
	tests := []struct {
		file string
		text string
		want string
	}{
		// pvfUtility PvfComments notes reach the section tag.
		{"a.act", "[accelerate]\n100 5", "加速参数"},
		// The rule is extension scoped, so another extension stays untouched.
		{"a.equ", "[accelerate]\n100 5", ""},
		// A hand-maintained rule keeps the primary title for its section.
		{"a.act", "[attack]\n1", "攻击"},
		{"a.act", "[unknown section]\n1", ""},
	}
	for _, tc := range tests {
		results := engine.Annotate(tc.file, pvf.ParseScriptView(tc.text), nil)
		title := ""
		if len(results) > 0 {
			title = results[0].Title
		}
		if title != tc.want {
			t.Fatalf("%s %q: title = %q, want %q", tc.file, tc.text, title, tc.want)
		}
	}
}

// TestLoadDefaultExplainFieldFeedsPreviewWithoutDuplicateTag checks that the
// shared [explain] preview field neither adds a second section tag nor stays
// invisible to the equipment preview.
func TestLoadDefaultExplainFieldFeedsPreviewWithoutDuplicateTag(t *testing.T) {
	engine, err := LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	view := pvf.ParseScriptView("[explain]\n`主要说明`")
	results := engine.Annotate("equipment/test.equ", view, nil)
	if len(results) != 1 || results[0].Title != "装备说明" {
		t.Fatalf("results = %#v", results)
	}
	found := false
	for _, field := range engine.PreviewFields("equipment/test.equ", "equ") {
		if field.ID == "equ.explain" && field.Preview != nil && field.Preview.Role == "base-explain" {
			found = true
		}
	}
	if !found {
		t.Fatal("equ.explain preview field is missing")
	}
	if values := engine.ExtractPreviewFields("equipment/test.equ", view, "equ"); len(values) == 0 {
		t.Fatal("[explain] produced no preview value")
	}
}

// handWrittenEngine compiles config/annotations.json with its relations but
// without the generated path and section rule sets, for tests that assert the
// behaviour of the curated rules alone.
func handWrittenEngine(t *testing.T) *Engine {
	t.Helper()
	document, err := ParseRules(appconfig.AnnotationsJSON)
	if err != nil {
		t.Fatal(err)
	}
	lists, err := ParseLists(appconfig.ListsJSON)
	if err != nil {
		t.Fatal(err)
	}
	document.Relations = lists.Relations
	engine, err := Compile(document)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestMergeGeneratedRulesSkipsExistingIDs(t *testing.T) {
	document := Document{Version: 1, Rules: []Rule{{
		ID: "path.keep", Match: MatchSpec{Glob: "equipment/**"}, Target: TargetSpec{Kind: "path"},
		Annotation: AnnotationSpec{Title: "保留", Type: "text"},
	}}}
	data := []byte(`{"version":1,"rules":[
		{"id":"path.keep","match":{"glob":"equipment/**"},"target":{"kind":"path"},"annotation":{"title":"重复","type":"text"}},
		{"id":"path.new","match":{"glob":"stackable/**"},"target":{"kind":"path"},"annotation":{"title":"新增","type":"text"}}
	]}`)
	if err := mergeGeneratedRules(&document, data, "path 标注"); err != nil {
		t.Fatal(err)
	}
	if len(document.Rules) != 2 || document.Rules[0].Annotation.Title != "保留" || document.Rules[1].ID != "path.new" {
		t.Fatalf("rules = %#v", document.Rules)
	}
	if err := mergeGeneratedRules(&document, data, "path 标注"); err != nil {
		t.Fatal(err)
	}
	if len(document.Rules) != 2 {
		t.Fatalf("merging twice duplicated rules: %#v", document.Rules)
	}
}
