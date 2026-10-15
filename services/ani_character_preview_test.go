package services

import (
	"strings"
	"testing"

	"pvfine/internal/pvf"
)

func TestANICharacterDirectoryPlaceholder(t *testing.T) {
	for _, test := range []struct {
		name, aniPath, list, draft, want, issue string
	}{
		{"relative", "character/archer/animation/a.ani", "16 `archer/archer.chr`", "", "0016", ""},
		{"root relative", "character/archer/animation/a.ani", "16 `character/archer/archer.chr`", "", "0016", ""},
		{"ai shared paths", "aicharacter/archer/animation/a.ani", "16 `archer/archer.chr`", "", "0016", ""},
		{"ai list draft", "aicharacter/archer/animation/a.ani", "16 `archer/archer.chr`", "22 `archer/archer.chr`", "0022", ""},
		{"ai directory boundary", "aicharacter/archerextra/animation/a.ani", "16 `archer/archer.chr`", "", "", "没有匹配"},
		{"ai root boundary", "aicharacterextra/archer/animation/a.ani", "16 `archer/archer.chr`", "", "%04d", ""},
		{"case and slashes", "Character/Archer/Animation/a.ani", "16 `Character\\Archer\\Archer.chr`", "", "0016", ""},
		{"longest prefix", "character/archer/special/a.ani", "16 `archer/archer.chr` 23 `archer/special/other.chr`", "", "0023", ""},
		{"longest first", "character/archer/special/a.ani", "23 `archer/special/other.chr` 16 `archer/archer.chr`", "", "0023", ""},
		{"zero id", "character/archer/a.ani", "0 `archer/archer.chr`", "", "0000", ""},
		{"wide id", "character/archer/a.ani", "6200000 `archer/archer.chr`", "", "6200000", ""},
		{"list draft", "character/archer/a.ani", "16 `archer/archer.chr`", "22 `archer/archer.chr`", "0022", ""},
		{"boundary", "character/archerextra/a.ani", "16 `archer/archer.chr`", "", "", "没有匹配"},
		{"outside character", "monster/archer/a.ani", "16 `archer/archer.chr`", "", "%04d", ""},
		{"unresolved duplicates", "character/archer/a.ani", "16 `archer/one.chr` 17 `archer/two.chr`", "", "", "无法按 animation"},
		{"missing list", "character/archer/a.ani", "", "", "", "找不到"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := pvf.New()
			c := NewCore()
			c.archive = a
			if test.list != "" {
				i := mustAddText(t, a, "character/character.lst", test.list, pvf.TypeScript)
				if test.draft != "" {
					c.editorText = map[int32]string{i: test.draft}
				}
			}
			text := "[FRAME MAX]\n1\n[IMAGE PATH]\n`Character/Archer/skin/AC_body%04d.img`\n[/IMAGE PATH]\n" +
				"[FRAME000]\n[IMAGE EX]\n0 3\n[IMAGE POS]\n12 -4\n" +
				"[IMAGE]\n`Character/Archer/skin/AC_body%04d.img` 7\n[IMAGE POS]\n0 0\n[DELAY]\n80"
			index := mustAddText(t, a, test.aniPath, "[FRAME MAX]\n0", pvf.TypeScript)
			before := a.ModifiedCount()
			document, err := NewPreviewService(c).ParseANIForFile(index, text)
			if err != nil {
				t.Fatal(err)
			}
			if test.issue != "" {
				if document.Valid || len(document.Issues) != 1 || !strings.Contains(document.Issues[0].Message, test.issue) {
					t.Fatalf("document = %#v", document)
				}
				return
			}
			if !document.Valid || len(document.Issues) != 0 || len(document.Frames) != 1 || len(document.Frames[0].Layers) != 2 {
				t.Fatalf("document = %#v", document)
			}
			for _, layer := range document.Frames[0].Layers {
				if want := "Character/Archer/skin/AC_body" + test.want + ".img"; layer.Image.Path != want {
					t.Fatalf("image = %q, want %q", layer.Image.Path, want)
				}
			}
			if a.ModifiedCount() != before {
				t.Fatal("preview changed archive")
			}
			stored, _ := a.Text(index)
			if strings.Contains(stored, "AC_body") {
				t.Fatal("preview wrote caller text to archive")
			}
		})
	}
}

func TestANICharacterAnimationPrefix(t *testing.T) {
	c := NewCore()
	a := pvf.New()
	c.archive = a
	// Deliberately put variants before the base chr to avoid list-order matching.
	mustAddText(t, a, "character/character.lst", "2 `swordman/atswordman.chr` 5 `swordman/dsswordman.chr` 9 `swordman/swordman.chr` 11 `fighter/fighter.chr` 12 `fighter/atfighter.chr` 16 `archer/archer.chr`", pvf.TypeScript)
	text := "[FRAME MAX]\n1\n[FRAME000]\n[IMAGE]\n`body%04d.img` 3\n[IMAGE POS]\n0 0\n[DELAY]\n80"
	service := NewPreviewService(c)
	for _, test := range []struct{ aniPath, wantPath string }{
		{"character/swordman/animation/a.ani", "body0009.img"},
		{"character/swordman/atanimation/a.ani", "body0002.img"},
		{"character/swordman/dsanimation/effect/nested/a.ani", "body0005.img"},
		{"Character/Swordman/AtAnimation/a.ani", "body0002.img"},
		{"character/fighter/animation/a.ani", "body0011.img"},
		{"character/fighter/atanimation/a.ani", "body0012.img"},
		{"character/archer/animation/a.ani", "body0016.img"},
		{"aicharacter/swordman/animation/a.ani", "body0009.img"},
		{"aicharacter/swordman/atanimation/a.ani", "body0002.img"},
		{"aicharacter/swordman/dsanimation/effect/nested/a.ani", "body0005.img"},
		{"AICharacter/Fighter/AtAnimation/a.ani", "body0012.img"},
	} {
		index := mustAddText(t, a, test.aniPath, "", pvf.TypeScript)
		document, err := service.ParseANIForFile(index, text)
		if err != nil {
			t.Fatal(err)
		}
		if !document.Valid || document.Frames[0].Layers[0].Image.Path != test.wantPath {
			t.Fatalf("animation %q: %#v", test.aniPath, document)
		}
	}
	for _, aniPath := range []string{"character/swordman/unknownanimation/a.ani", "character/swordman/atanimationextra/a.ani", "character/archer/atanimation/a.ani", "aicharacter/swordman/unknownanimation/a.ani", "aicharacter/archer/atanimation/a.ani"} {
		index := mustAddText(t, a, aniPath, "", pvf.TypeScript)
		document, err := service.ParseANIForFile(index, text)
		if err != nil || document.Valid {
			t.Fatalf("unmatched animation %q: document=%#v, err=%v", aniPath, document, err)
		}
	}
}

func TestANICharacterPreviewWithoutPlaceholder(t *testing.T) {
	c := NewCore()
	c.archive = pvf.New()
	document, err := NewPreviewService(c).ParseANIForFile(-1, "[FRAME MAX]\n1\n[FRAME000]\n[IMAGE]\n`fixed.img` 0\n[IMAGE POS]\n0 0\n[DELAY]\n80")
	if err != nil || !document.Valid || document.Frames[0].Layers[0].Image.Path != "fixed.img" {
		t.Fatalf("document = %#v, err = %v", document, err)
	}
}

func TestAIANICharacterJobAtAnyDepth(t *testing.T) {
	c := NewCore()
	a := pvf.New()
	c.archive = a
	mustAddText(t, a, "character/character.lst", "0 `gunner/gunner.chr` 4 `gunner/atgunner.chr` 1 `swordman/swordman.chr` 8 `swordman/dsswordman.chr`", pvf.TypeScript)
	text := "[FRAME MAX]\n1\n[IMAGE PATH]\n`body%04d.img`\n[/IMAGE PATH]\n[FRAME000]\n[IMAGE EX]\n0 3\n[IMAGE POS]\n0 0\n[DELAY]\n80"
	for _, test := range []struct{ aniPath, wantPath string }{
		{"aicharacter/_jojochan/atgunner/reina/animation/attack2.ani", "body0004.img"},
		{"aicharacter/atgunner/reina/animation/attack2.ani", "body0004.img"},
		{"aicharacter/a/b/c/atgunner/attack2.ani", "body0004.img"},
		{"AICharacter/Custom/AtGunner/Reina/Animation/attack2.ani", "body0004.img"},
		{"aicharacter/custom/gunner/reina/animation/a.ani", "body0000.img"},
		{"aicharacter/custom/gunner/reina/atanimation/a.ani", "body0004.img"},
		{"aicharacter/custom/dsswordman/name/animation/a.ani", "body0008.img"},
		{"aicharacter/custom/swordman/name/dsanimation/a.ani", "body0008.img"},
	} {
		index := mustAddText(t, a, test.aniPath, "", pvf.TypeScript)
		document, err := NewPreviewService(c).ParseANIForFile(index, text)
		if err != nil || !document.Valid || document.Frames[0].Layers[0].Image.Path != test.wantPath {
			t.Fatalf("path %q: document=%#v, err=%v", test.aniPath, document, err)
		}
	}
	for _, aniPath := range []string{
		"aicharacter/custom/notatgunner/reina/animation/a.ani",
		"aicharacter/custom/atgunnerextra/reina/animation/a.ani",
		"aicharacter/custom/gunner/animation/atgunner.ani",
		"aicharacter/custom/atgunner/swordman/animation/a.ani",
	} {
		index := mustAddText(t, a, aniPath, "", pvf.TypeScript)
		document, err := NewPreviewService(c).ParseANIForFile(index, text)
		// A job name in the filename is ignored; gunner/animation still wins.
		if strings.HasSuffix(aniPath, "/atgunner.ani") {
			if err != nil || !document.Valid || document.Frames[0].Layers[0].Image.Path != "body0000.img" {
				t.Fatalf("filename affected job: %#v, %v", document, err)
			}
		} else if err != nil || document.Valid {
			t.Fatalf("unmatched/ambiguous path %q: document=%#v, err=%v", aniPath, document, err)
		}
	}
}
