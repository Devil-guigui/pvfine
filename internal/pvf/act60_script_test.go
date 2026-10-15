package pvf

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"unicode/utf16"
)

const act60EquipmentSource = "// 源码注释 [rarity] 99\r\n" +
	"[name] `GBL教圣祭项坠`\r\n[name2] `GBL's ritual Neclace`\r\n" +
	"[flavor text]\r\n`    英勇的GBL教信徒不会屈服于任何困难。  --奥菲里亚`\r\n" +
	"[grade] 40\r\n[attach type] `[trade]`\r\n[creation rate] 500\r\n[rarity] 4\r\n[weight] 400\r\n" +
	"[usable job]\r\n`[all]`\r\n[/usable job]\r\n[minimum level] 35\r\n" +
	"[icon] `Item/common/necklace.img` 8\r\n[field image] `Item/FieldImage.img` 6\r\n" +
	"[equipment type] `[amulet]` 21\r\n[price] 48000\r\n[magical defense] +13\r\n" +
	"[equipment magical defense] +3760 +3120\r\n[set name] `GBL教圣祭首饰`\r\n[set item]\r\n22104\r\n24106\r\n[/set item]\r\n" +
	"[fullset basic explain]\r\n`被攻击时， 有2%%的几率召唤出1个信徒`\r\n" +
	"[set ability]\r\n[all elemental resistance] 10\r\n[MP regen speed] 5\r\n" +
	"[if]\r\n[cooltime] 100000\r\n[hit] 1\r\n[/if]\r\n[then]\r\n[target] `myself`\r\n[duration] 100000\r\n[probability] 2\r\n[summon monster] 60102 35 1\r\n[/then]\r\n"

func TestACT60ScriptSemanticCompatibility(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		a, err := Parse(act60Fixture(t, legacy))
		if err != nil {
			t.Fatal(err)
		}
		if err := a.SetText(0, act60EquipmentSource); err != nil {
			t.Fatal(err)
		}
		if _, err := a.AddFileText("text/test.str", "name_test>字符串表中的装备名称\r\n", TypeUnicode); err != nil {
			t.Fatal(err)
		}
		if _, err := a.AddFileText("string.lst", "// 映射\r\n3 `text/test.str`\r\n", TypeUnicode); err != nil {
			t.Fatal(err)
		}
		ref, err := a.AddFileText("equipment/ref.equ", "[name] <3::name_test>\n[rarity] +2\n", TypeUnicode)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.AddFileText("sqr/test.nut", "// [name] is a comment\nfunction test() {}", TypeUnicode); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := a.SaveTo(&out); err != nil {
			t.Fatal(err)
		}
		b, err := Parse(out.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if b.File(0).DataType != TypeScript || b.File(1).DataType != 0 || b.File(2).DataType != TypeUnicode || b.File(mustFind(b, "sqr/test.nut")).DataType != TypeUnicode {
			t.Fatal("ACT60 script/text/binary classification mismatch")
		}
		text, err := b.Text(0)
		if err != nil || text != act60EquipmentSource {
			t.Fatal("source formatting changed", err)
		}
		m, err := b.ScriptMetadata(0)
		if err != nil || m.Name != "GBL教圣祭项坠" || m.Rarity != 4 || !m.HasRarity || m.Icon == nil || m.Icon.Path != "Item/common/necklace.img" || m.Icon.Index != 8 || m.FieldImage == nil || m.FieldImage.Index != 6 {
			t.Fatalf("metadata mismatch: %+v %v", m, err)
		}
		pairs, err := b.ScriptListPairs(mustFind(b, "equipment/equipment.lst"))
		if err != nil || len(pairs) != 1 || pairs[0].ID != "1" || pairs[0].Path != "test.equ" {
			t.Fatal("list source was not parsed", err)
		}
		if name, ok := b.ItemName(ref); !ok || name != "字符串表中的装备名称" {
			t.Fatalf("ACT60 placeholder failed: %q", name)
		}
		m, err = b.ScriptMetadata(ref)
		if err != nil || len(m.StringTableReferences) != 1 || m.Rarity != 2 {
			t.Fatal("placeholder metadata failed", err)
		}
		seenPlus := false
		seenNested := false
		for _, e := range ParseScriptView(text).Elements {
			if e.Section == "magical defense" && e.Kind == ScriptElementToken && e.Value == "13" {
				seenPlus = true
			}
			if e.Section == "rarity" && e.Value == "99" {
				t.Fatal("comment became a token")
			}
			if e.Section == "summon monster" && e.Value == "60102" {
				seenNested = true
			}
		}
		if !seenPlus || !seenNested {
			t.Fatal("signed/nested source was not parsed")
		}
		query, err := b.EncodeScriptQuery("[name] <3::name_test> // ignored")
		if err != nil {
			t.Fatal(err)
		}
		refRaw, err := b.RawBytes(ref)
		if err != nil || len(query) != 10 || !bytes.HasPrefix(refRaw, query) {
			t.Fatal("ACT60 script query token mismatch", err)
		}
		// Search workers use the same immutable token pools as metadata and GUI.
		if _, err := b.BuildStringPoolIndex(context.Background()); err != nil {
			t.Fatal(err)
		}
		a.Release()
		b.Release()
	}
}

func TestACT60SourceEditsCloneCommitAndRevert(t *testing.T) {
	a, err := Parse(act60Fixture(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	original, _ := a.Text(0)
	stage := a.CloneForBatch()
	defer stage.Release()
	changed := "// 只修改注释和空白\r\n" + original + "\r\n"
	if err := stage.SetText(0, changed); err != nil {
		t.Fatal(err)
	}
	if !stage.Modified() || a.Modified() {
		t.Fatal("source-only edit/clone isolation failed")
	}
	if err := a.CommitBatch(stage, map[int32]struct{}{0: {}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Text(0); got != changed {
		t.Fatal("batch lost source formatting")
	}
	a.Revert(0)
	if err := a.SetText(0, "[name] `新增字符串`\n[unique source tag] 1"); err != nil {
		t.Fatal(err)
	}
	a.Revert(0)
	if got, _ := a.Text(0); got != original || a.Modified() || a.ModifiedCount() != 0 {
		t.Fatal("revert left edited source")
	}
	if err := a.ApplyScriptChanges(stage, []ScriptChange{{Kind: ChangeKindChanged, Path: a.Path(0), Raw: stage.overlay[0], DataType: TypeScript}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Text(0); got != changed || !a.Modified() {
		t.Fatal("script transaction lost source-only edit")
	}
	var out bytes.Buffer
	if err := a.SaveTo(&out); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Text(0); got != changed {
		t.Fatal("saving lost source-only edit")
	}
}

func TestACT60GUIRawEditPreservesNativeReferences(t *testing.T) {
	a, err := Parse(act60Fixture(t, false))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	if err := a.SetText(0, "[name] <3::name_test>\n[rarity] 4\n"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := a.SaveTo(&out); err != nil {
		t.Fatal(err)
	}
	raw, err := a.encodeACT60Script("[name] <3::name_test>\n[rarity] 5\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetRawBytes(0, raw); err != nil {
		t.Fatal(err)
	}
	text, err := a.Text(0)
	if err != nil || !strings.Contains(text, "<3::name_test>") || strings.Contains(text, "{8=") {
		t.Fatal("GUI displayed binary marker", err)
	}
	out.Reset()
	if err := a.SaveTo(&out); err != nil {
		t.Fatal(err)
	}
	native, err := a.act60.payload(a.data, 0)
	if err != nil || !bytes.Contains(native, []byte("<3::name_test>")) || bytes.Contains(native, []byte("{8=")) {
		t.Fatal("binary marker leaked into native source", err)
	}
	m, err := a.ScriptMetadata(0)
	if err != nil || m.Rarity != 5 {
		t.Fatal("raw GUI edit failed", err)
	}
}

func TestACT60SourceLexingPositions(t *testing.T) {
	source := "// 😀注释 [fake] 9\r\n[name] <3::name_test> /* [fake2] 1 */\n[icon] `path//literal.img` +8"
	view := ParseScriptView(source)
	units := utf16.Encode([]rune(strings.ReplaceAll(source, "\r\n", "\n")))
	tokens := 0
	for _, e := range view.Elements {
		if e.Section == "fake" || e.Section == "fake2" {
			t.Fatal("comment tag parsed")
		}
		if e.Kind == ScriptElementToken {
			tokens++
			if e.Section == "name" && string(utf16.Decode(units[e.Start:e.End])) != "<3::name_test>" {
				t.Fatal("reference annotation offset mismatch")
			}
			if e.Section == "icon" && e.Value == "8" && string(utf16.Decode(units[e.Start:e.End])) != "+8" {
				t.Fatal("signed value annotation offset mismatch")
			}
			if e.Section == "name" && (e.TokenType != 8 || e.Value != "<3::name_test>") {
				t.Fatal("reference not recognized")
			}
			if e.Section == "icon" && e.Value == "8" && e.TokenType != 0 {
				t.Fatal("signed int not recognized")
			}
		}
	}
	if tokens != 3 {
		t.Fatalf("tokens=%d", tokens)
	}
}
