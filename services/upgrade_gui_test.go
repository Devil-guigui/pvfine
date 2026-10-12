package services

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"pvfine/internal/pvf"
)

const upgradeTestRow = "2 0.018 0.1 0.033 0.3 0.6 12500 2 3 3037 10 1 0.018 0.1 0.033 0.17 0.6"

// Representative 24-column rows from the supplied normal table: +1, +11, +18.
const upgradeTypedRow = "2.0 0 0 0 0 0.6 0 0.828 0 0 1 0 1 0 3037 10 0.3 0.018 0.1 0.033 0.17 0.228 0 0.2622"
const upgradeTypedDowngradeRow = "26.9 0 0 0 0 6.75 0 9.703125 75000 75000 2 3 3 0 3037 120 3 0.22 1.9 0.645 3.17 2.565 0 2.94975"
const upgradeTypedDifferentRatesRow = "74.3 0 0 0 0 63 0 90.5625 90000 88000 3 0 3 0 3037 260 8.2 0.62 10.3 3.501 17.17 23.94 0 27.531"

func TestUpgradeTypedRowsAndIndependentFields(t *testing.T) {
	text := "# 😀\r\n[table]\r\n[table type]\r\n`event` `unrelated`\r\n[/table]\r\n" +
		"[table]\r\n[table type]\r\n`normal` " + upgradeTypedRow + "\r\n" + upgradeTypedDowngradeRow + "\r\n" + upgradeTypedDifferentRatesRow + "\r\n[/table]\r\n" +
		"[table]\r\n[table type]\r\n`amplify` " + strings.Replace(upgradeTypedRow, "3037", "3038", 1) + "\r\n[/table]"
	doc, err := parseUpgrade(text, "normal")
	if err != nil || len(doc.Rows) != 3 {
		t.Fatalf("normal: %v %v", doc, err)
	}
	first, downgrade, different := doc.Rows[0], doc.Rows[1], doc.Rows[2]
	if first.Fields[3].Value != 3037 || first.Fields[4].Value != 10 {
		t.Fatal("incorrect material fields")
	}
	if downgrade.Fields[1].Value != 2 || downgrade.Fields[2].Value != 3 || downgrade.FieldsT2[1].Value != 3 || downgrade.FieldsT2[2].Value != 0 {
		t.Fatal("T1/T2 operations mixed")
	}
	if different.Fields[0].Value != 90000 || different.FieldsT2[0].Value != 88000 {
		t.Fatal("T1/T2 probabilities mixed")
	}
	source := utf16.Encode([]rune(strings.ReplaceAll(text, "\r\n", "\n")))
	for _, row := range doc.Rows {
		for _, field := range append(append([]SkillNumber(nil), row.Fields...), row.FieldsT2...) {
			if got := string(utf16.Decode(source[field.Start:field.End])); got != strconv.FormatInt(int64(field.Value), 10) {
				t.Fatalf("wrong field position: %s", got)
			}
		}
	}
	amplify, err := parseUpgrade(text, "amplify")
	if err != nil || len(amplify.Rows) != 1 || amplify.Rows[0].Fields[3].Value != 3038 {
		t.Fatalf("amplify: %v %v", amplify, err)
	}
	large, err := parseUpgrade("[table]\n[table type]\n`normal` "+strings.Repeat(upgradeTypedRow+"\n", 250)+"[/table]", "normal")
	if err != nil || len(large.Rows) != 250 || large.Rows[249].Level != 250 {
		t.Fatal("typed levels truncated")
	}
	if _, err := parseUpgrade("[table]\n[table type]\n`normal` "+upgradeTestRow+"\n[/table]", "normal"); err == nil {
		t.Fatal("accepted wrong typed row width")
	}
}

func TestUpgradeParseSourceAndRange(t *testing.T) {
	text := "#PVF_File\r\n[name]\r\n`😀`\r\n[table]\r\n" + strings.Repeat(upgradeTestRow+"\r\n", 32) + "[/table]\r\n[other]\r\n99"
	doc, err := parseUpgrade(text, "normal")
	if err != nil || len(doc.Rows) != 31 {
		t.Fatalf("rows: %v %v", doc, err)
	}
	row := doc.Rows[0]
	if row.Level != 1 || row.Fields[0].Value != 12500 || row.Fields[3].Value != 3037 || row.Fields[4].Value != 10 {
		t.Fatalf("fields: %+v", row)
	}
	source := utf16.Encode([]rune(strings.ReplaceAll(text, "\r\n", "\n")))
	field := row.Fields[0]
	if string(utf16.Decode(source[field.Start:field.End])) != "12500" {
		t.Fatal("invalid UTF-16 source positions")
	}
}

func TestUpgradeRejectMalformed(t *testing.T) {
	for _, text := range []string{
		"[other]\n1", "[table]\n", "[table]\n1 2",
		"[table]\n" + upgradeTestRow + "\n[table]\n" + upgradeTestRow,
		"[table]\n" + strings.Replace(upgradeTestRow, "12500", "100001", 1),
		"[table]\n" + strings.Replace(upgradeTestRow, "12500", "1.0", 1),
		"[table]\n" + strings.Replace(upgradeTestRow, "3037", "-1", 1),
		"[table]\n" + strings.Replace(upgradeTestRow, "12500 2 3", "12500 4 3", 1),
	} {
		if _, err := parseUpgrade(text, "normal"); err == nil {
			t.Fatalf("accepted malformed data: %q", text)
		}
	}
}

func TestUpgradeReadDraftAndPaths(t *testing.T) {
	a := pvf.New()
	text := "[table]\n" + upgradeTestRow + "\n[/table]"
	indexes := []int32{}
	for _, path := range []string{"etc/upgrade.etc", "etc/amplifyupgrade.etc", "etc/other.etc"} {
		i, err := a.AddFileText(path, text, pvf.TypeScript)
		if err != nil {
			t.Fatal(err)
		}
		indexes = append(indexes, i)
	}
	c := NewCore()
	if err := c.setArchive(a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.closeArchive)
	s := NewFileGUIService(c)
	draft := strings.Replace(text, "12500", "0", 1)
	for _, i := range indexes[:2] {
		doc, err := s.ReadUpgrade(i, draft)
		if err != nil || doc.Rows[0].Fields[0].Value != 0 {
			t.Fatalf("draft: %v %v", doc, err)
		}
		original, _ := a.Text(i)
		if !strings.Contains(original, "12500") {
			t.Fatal("read mutated archive")
		}
	}
	if _, err := s.ReadUpgrade(indexes[2], draft); err == nil {
		t.Fatal("accepted unrelated path")
	}
	if _, err := s.ReadUpgrade(-1, draft); err == nil {
		t.Fatal("accepted invalid index")
	}
	// Both documents may contain both types; choose by the file being edited.
	typedDraft := "[table]\n[table type]\n`normal` " + upgradeTypedRow + "\n[/table]\n" +
		"[table]\n[table type]\n`amplify` " + strings.Replace(upgradeTypedRow, "3037", "3038", 1) + "\n[/table]"
	for n, i := range indexes[:2] {
		doc, err := s.ReadUpgrade(i, typedDraft)
		if err != nil || doc.Rows[0].Fields[3].Value != float64(3037+n) {
			t.Fatalf("file type selection: %v %v", doc, err)
		}
	}
}

func TestUpgradeSelectTypedTables(t *testing.T) {
	text := "# 😀\r\n[table]\r\n[table type]\r\n`event` `other data`\r\n[/table]\r\n[table]\r\n[table type]\r\n`normal` 2.0 20000 3037\r\n[/table]\r\n[table]\r\n[table type]\r\n`amplify` 4.0 40000 3038\r\n[/table]"
	for _, kind := range []string{"normal", "amplify"} {
		values, typed, err := upgradeTableTokens(text, kind)
		if err != nil || !typed || len(values) != 3 {
			t.Fatalf("%s: %v %v", kind, values, err)
		}
		want := "20000"
		if kind == "amplify" {
			want = "40000"
		}
		if values[1].Value != want {
			t.Fatalf("wrong table selected: %v", values)
		}
		source := utf16.Encode([]rune(strings.ReplaceAll(text, "\r\n", "\n")))
		if string(utf16.Decode(source[values[1].Start:values[1].End])) != want {
			t.Fatal("source offsets lost")
		}
	}
	for _, candidate := range []string{
		"[table]\n[table type]\n`event` 1\n[/table]",
		"[table]\n[table type]\n`normal` 1\n[/table]\n[table]\n[table type]\n`normal` 2\n[/table]",
	} {
		if _, _, err := upgradeTableTokens(candidate, "normal"); err == nil {
			t.Fatal("accepted missing or ambiguous target")
		}
	}
}
