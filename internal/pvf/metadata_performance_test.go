package pvf

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestScriptMetadataSelectiveValues(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       ScriptMetadata
	}{
		{"nested", "[outer]\n[name]\n`nested`\n[/outer]\n[name]\n9 `` `top` `later`\n[unrelated]\n`ignored`", ScriptMetadata{Name: "top", HasName: true}},
		{"resumed-parent", "[icon]\n`parent.img`\n[child]\n[icon]\n`child.img` 8\n[/child]\n4\n[/icon]", ScriptMetadata{Icon: &ScriptImageReference{Path: "parent.img", Index: 4}}},
		{"invalid-pairs", "[icon]\n3 `` -1 `invalid.img` -2 `good.img` 6 `later.img` 7\n[field image]\n`field.img` 2", ScriptMetadata{Icon: &ScriptImageReference{Path: "good.img", Index: 6}, FieldImage: &ScriptImageReference{Path: "field.img", Index: 2}}},
		{"rarity-first-token", "[rarity]\n`broken` 4\n[unrelated]\n9\n[rarity]\n`3` 5", ScriptMetadata{Rarity: 3, HasRarity: true}},
		{"case-and-space", "[ NAME ]\n`title`\n[ICON]\n`item.img` 2", ScriptMetadata{Name: "title", HasName: true, Icon: &ScriptImageReference{Path: "item.img", Index: 2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := New()
			i, err := a.AddFileText("equipment/test.equ", tc.text, TypeScript)
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.ScriptMetadata(i)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("metadata = %#v, %v; want %#v", got, err, tc.want)
			}
		})
	}
}

func TestScriptMetadataDoesNotDecodeUnrelatedValues(t *testing.T) {
	a := New()
	i, err := a.AddFileText("equipment/test.equ", "[name]\n`title`\n[unrelated]\n`unused value`", TypeScript)
	if err != nil {
		t.Fatal(err)
	}
	unused := a.StringOffset("unused value")
	a.resolveCache = make(map[int32]string)
	if _, err := a.ScriptMetadata(i); err != nil {
		t.Fatal(err)
	}
	if _, decoded := a.resolveCache[unused]; decoded {
		t.Fatal("unrelated section value was decoded")
	}
}

// Most tokens in a real equipment script are unrelated to search metadata.
func BenchmarkScriptMetadataLargeScript(b *testing.B) {
	a := New()
	var text strings.Builder
	text.WriteString("[name]\n`sample`\n[icon]\n`item/test.img` 3\n[rarity]\n4\n")
	for section := 0; section < 100; section++ {
		fmt.Fprintf(&text, "[unrelated %d]\n", section)
		for value := 0; value < 40; value++ {
			fmt.Fprintf(&text, "`unused %d %d` %d\n", section, value, value)
		}
	}
	i, err := a.AddFileText("equipment/test.equ", text.String(), TypeScript)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		m, err := a.ScriptMetadata(i)
		if err != nil || m.Name != "sample" || m.Rarity != 4 || m.Icon == nil || m.Icon.Index != 3 {
			b.Fatalf("metadata mismatch: %#v, %v", m, err)
		}
	}
}
