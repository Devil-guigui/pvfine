package annotations

import (
	"fmt"
	"strings"
	"testing"

	"pvfine/internal/pvf"
)

func BenchmarkAnnotateManyRules(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			rules := make([]Rule, count)
			for i := range rules {
				rules[i] = Rule{
					ID: fmt.Sprint(i), Match: MatchSpec{Extensions: []string{".equ"}},
					Target:     TargetSpec{Kind: "token", Section: fmt.Sprintf("section%d", i), Index: intPtr(0)},
					Annotation: AnnotationSpec{Title: "说明", Type: "text"},
				}
			}
			engine, err := Compile(Document{Version: 1, Rules: rules})
			if err != nil {
				b.Fatal(err)
			}
			var text strings.Builder
			for i := 0; i < 100; i++ {
				fmt.Fprintf(&text, "[section%d]\n1 2 3 4 5\n", i)
			}
			view := pvf.ParseScriptView(text.String())
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				engine.Annotate("a.equ", view, nil)
			}
		})
	}
}

// BenchmarkAnnotatePathManyRules guards the bulk path note lookup: without the
// literal glob and "<dir>/**" buckets every tree node would scan all rules.
func BenchmarkAnnotatePathManyRules(b *testing.B) {
	const files, directories = 15000, 2000
	rules := make([]Rule, 0, files+directories+4)
	for i := 0; i < files; i++ {
		rules = append(rules, Rule{
			ID: fmt.Sprintf("file.%d", i), Match: MatchSpec{Glob: fmt.Sprintf("dir%02d/sub%02d/file%05d.ani", i%40, i%50, i)},
			Target: TargetSpec{Kind: "path"}, Annotation: AnnotationSpec{Title: "文件说明", Type: "text"},
		})
	}
	for i := 0; i < directories; i++ {
		rules = append(rules, Rule{
			ID: fmt.Sprintf("dir.%d", i), Match: MatchSpec{Glob: fmt.Sprintf("dir%02d/sub%02d/**", i%40, i%50)},
			Target: TargetSpec{Kind: "path"}, Annotation: AnnotationSpec{Title: "目录说明", Type: "text"},
		})
	}
	rules = append(rules,
		Rule{ID: "star", Match: MatchSpec{Glob: "**/*.ani"}, Target: TargetSpec{Kind: "path"}, Annotation: AnnotationSpec{Title: "通配", Type: "text"}},
		Rule{ID: "all", Target: TargetSpec{Kind: "path"}, Annotation: AnnotationSpec{Title: "全部", Type: "text"}},
	)
	engine, err := Compile(Document{Version: 1, Rules: rules})
	if err != nil {
		b.Fatal(err)
	}
	paths := []string{
		"dir01/sub01/file00001.ani",
		"dir01/sub01/file00001.ani.bak",
		"dir39/sub49/deep/deeper/1.ani",
		"unknown/path.dat",
		"dir01/sub01",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.AnnotatePath(paths[i%len(paths)], false)
	}
}
