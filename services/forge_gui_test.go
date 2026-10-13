package services

import (
	"pvfine/internal/pvf"
	"strings"
	"testing"
	"unicode/utf16"
)

const forgeTestText = "#PVF_File\r\n[table]\r\n2 1 10000 1 3 2 5000 1.11 4 3 5000 1.21 6 4 3000 1.32 8 5 3000 1.42 13 9 1500 1.53 18 12 1500 1.63 25 18 750 1.74\r\n[/table]\r\n[separate upgrade max]\r\n8\r\n[level]\r\n8\r\n[item weights by grade]\r\n1 3326 1 2 3326 1 3 3326 2\r\n[/item weights by grade]\r\n[item weights by grade 100lv]\r\n96 3326 51 110 3326 51\r\n[/item weights by grade 100lv]\r\n[weapon weights]\r\n1.0 1.0\r\n[/weapon weights]"

func TestForgeStructureAndSource(t *testing.T) {
	doc, err := parseForge(forgeTestText)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Rows) != 8 || doc.Rows[7].Fields[2].Value != 750 || doc.Rows[7].Fields[3].Value != 1.74 || doc.Maximum.Value != 8 || len(doc.Grades) != 3 || len(doc.Grades100) != 2 || len(doc.Extras) != 2 {
		t.Fatalf("unexpected document: %+v", doc)
	}
	source := utf16.Encode([]rune(strings.ReplaceAll(forgeTestText, "\r\n", "\n")))
	token := doc.Grades100[1].Fields[0]
	if string(utf16.Decode(source[token.Start:token.End])) != "3326" {
		t.Fatal("wrong material source offsets")
	}
	for _, invalid := range []string{
		strings.Replace(forgeTestText, "10000", "10001", 1),
		strings.Replace(forgeTestText, "750 1.74", "750", 1),
		strings.Replace(forgeTestText, "[separate upgrade max]\r\n8", "[separate upgrade max]\r\n9", 1),
		strings.Replace(forgeTestText, "2 3326 1", "1 3326 1", 1),
		forgeTestText + "\r\n[table]\r\n2 1 10000 1\r\n[/table]",
		strings.Replace(forgeTestText, "3 3326 2", "3 3326 2.5", 1),
	} {
		if _, err := parseForge(invalid); err == nil {
			t.Fatal("accepted invalid forge text")
		}
	}
}

func TestForgeMaterialNamesAndPath(t *testing.T) {
	a := pvf.New()
	for path, text := range map[string]string{"stackable/stackable.lst": "3326 `forge.stk`", "stackable/forge.stk": "[name]\n`强烈的气息`"} {
		if _, err := a.AddFileText(path, text, pvf.TypeScript); err != nil {
			t.Fatal(err)
		}
	}
	index, err := a.AddFileText("etc/upgrade_separate.etc", forgeTestText, pvf.TypeScript)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := a.AddFileText("etc/other.etc", forgeTestText, pvf.TypeScript)
	if err != nil {
		t.Fatal(err)
	}
	c := NewCore()
	if err := c.setArchive(a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.closeArchive)
	s := NewFileGUIService(c)
	doc, err := s.ReadForge(index, forgeTestText)
	if err != nil {
		t.Fatal(err)
	}
	for _, rows := range [][]UpgradeRow{doc.Grades, doc.Grades100} {
		for _, row := range rows {
			if row.CostName != "强烈的气息" {
				t.Fatal("material name missing")
			}
		}
	}
	unknown, err := s.ReadForge(index, strings.ReplaceAll(forgeTestText, "3326", "999999"))
	if err != nil || unknown.Grades[0].CostName != "" {
		t.Fatal("unknown ID should keep empty name")
	}
	if _, err := s.ReadForge(wrong, forgeTestText); err == nil {
		t.Fatal("accepted wrong path")
	}
}
