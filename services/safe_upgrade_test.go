package services

import (
	"pvfine/internal/pvf"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

const safeUpgradeSample = `[safe upgrade]
0 1 0 0 1 10327281 40 124448
1 1 0 0 1 10327281 41 124448
2 1 0 0 1 10327281 43 124448
3 1 0 0 1 10327281 44 124448
4 1 20000 5000 1 10327281 81 247893
5 1 30000 5000 1 10327281 91 276207
6 1 40000 5000 1 10327281 99 298734
7 1 50000 5000 1 10327281 110 332572
8 1 60000 5000 1 10327281 124 377149
9 1 70000 5000 1 10327281 145 436960
10 1 90000 2000 1 10327281 491 1485598
11 1 94000 1000 2 10327281 1529 4626036
[/safe upgrade]`

func TestSafeUpgradeSampleAndSource(t *testing.T) {
	text := strings.ReplaceAll("# 😀\n"+safeUpgradeSample, "\n", "\r\n")
	rows, err := parseSafeUpgrade(text)
	if err != nil || len(rows) != 12 {
		t.Fatalf("parse: %v %v", rows, err)
	}
	source := utf16.Encode([]rune(strings.ReplaceAll(text, "\r\n", "\n")))
	for i, row := range rows {
		if len(row.Fields) != 8 || row.Fields[0].Value != float64(i) {
			t.Fatalf("row %d: %+v", i, row)
		}
		for _, field := range row.Fields {
			if got := string(utf16.Decode(source[field.Start:field.End])); got != strconv.FormatInt(int64(field.Value), 10) {
				t.Fatal("source position lost")
			}
		}
	}
	last := rows[11].Fields
	if last[2].Value != 94000 || last[3].Value != 1000 || last[4].Value != 2 || last[5].Value != 10327281 || last[6].Value != 1529 || last[7].Value != 4626036 {
		t.Fatal("fields shifted")
	}
	normal := "[table]\n[table type]\n`normal` " + upgradeTypedRow + "\n[/table]\n"
	doc, err := parseUpgrade(normal+text, "normal")
	if err != nil || len(doc.Rows) != 1 || len(doc.SafeRows) != 12 || doc.SafeIssue != "" {
		t.Fatalf("combined: %+v %v", doc, err)
	}
	doc, err = parseUpgrade(normal+"[safe upgrade]\n1 2\n[/safe upgrade]", "normal")
	if err != nil || len(doc.Rows) != 1 || doc.SafeIssue == "" {
		t.Fatal("malformed safe data blocked normal editor")
	}
}

func TestSafeUpgradeMaterialNames(t *testing.T) {
	a := pvf.New()
	for path, text := range map[string]string{
		"stackable/stackable.lst": "3037 `cube.stk` 10327281 `core.stk`",
		"stackable/cube.stk":      "[name]\n`无色小晶块`",
		"stackable/core.stk":      "[name]\n`炉岩核`",
	} {
		if _, err := a.AddFileText(path, text, pvf.TypeScript); err != nil {
			t.Fatal(err)
		}
	}
	indexes := []int32{}
	for _, kind := range []string{"normal", "amplify"} {
		path := "etc/upgrade.etc"
		if kind == "amplify" {
			path = "etc/amplifyupgrade.etc"
		}
		index, err := a.AddFileText(path, "[table]\n[table type]\n`"+kind+"` "+upgradeTypedRow+"\n[/table]\n"+safeUpgradeSample, pvf.TypeScript)
		if err != nil {
			t.Fatal(err)
		}
		indexes = append(indexes, index)
	}
	c := NewCore()
	if err := c.setArchive(a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.closeArchive)
	s := NewFileGUIService(c)
	for _, index := range indexes {
		text, _ := a.Text(index)
		doc, err := s.ReadUpgrade(index, text)
		if err != nil {
			t.Fatal(err)
		}
		if doc.Rows[0].CostName != "无色小晶块" {
			t.Fatalf("normal name: %+v", doc.Rows[0])
		}
		for _, row := range doc.SafeRows {
			if row.CostName != "炉岩核" {
				t.Fatalf("safe name: %+v", row)
			}
		}
		draft := strings.ReplaceAll(text, "10327281", "999999")
		unknown, err := s.ReadUpgrade(index, draft)
		if err != nil || unknown.SafeRows[0].CostName != "" {
			t.Fatalf("unknown draft: %+v %v", unknown, err)
		}
	}
}

func TestSafeUpgradeMalformedAndAbsent(t *testing.T) {
	for _, text := range []string{
		"[safe upgrade]\n[/safe upgrade]",
		"[safe upgrade]\n1 2 3\n[/safe upgrade]",
		strings.Replace(safeUpgradeSample, "94000", "1.5", 1),
		strings.Replace(safeUpgradeSample, "94000", "2147483648", 1),
		strings.Replace(safeUpgradeSample, "94000", "100001", 1),
		strings.Replace(safeUpgradeSample, "94000", "-1", 1),
		safeUpgradeSample + "\n" + safeUpgradeSample,
	} {
		if _, err := parseSafeUpgrade(text); err == nil {
			t.Fatalf("accepted malformed: %s", text)
		}
	}
	rows, err := parseSafeUpgrade("[other]\n1")
	if err != nil || len(rows) != 0 {
		t.Fatal("absent section rejected")
	}
}
