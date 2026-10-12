package script

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"pvfine/internal/pvf"
)

const scriptUpgradeRow = "2.0 0 0 0 0 0.6 0 0.828 90000 88000 2 3 3 0 3037 10 0.3 0.018 0.1 0.033 0.17 0.228 0 0.2622"

func TestScriptUpgradeFunctionsAndAtomicValidation(t *testing.T) {
	for _, test := range []struct {
		name, source string
		failed       bool
	}{
		{"range", `const f=pvf.find("etc/upgrade.etc"); if(f.editUpgrade({equipment:"armor",fromLevel:2,toLevel:3,successRate:level=>100-level*5,costCount:(level,current)=>current+level*2})!==2)throw Error("count");`, false},
		{"invalid late callback", `const f=pvf.find("etc/upgrade.etc");const before=f.text();let rejected=false;try{f.editUpgrade({successRate:level=>level===3?101:50,costCount:level=>level*20});}catch(e){rejected=true;}if(!rejected||f.text()!==before)throw Error("atomic validation failed");`, false},
		{"fraction count", `pvf.find("etc/upgrade.etc").editUpgrade({costCount:level=>level/2});`, true},
		{"string result", `pvf.find("etc/upgrade.etc").editUpgrade({successRate:()=>"50"});`, true},
		{"bad range", `pvf.find("etc/upgrade.etc").editUpgrade({fromLevel:0,successRate:50});`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := pvf.New()
			defer a.Release()
			text := "[name]\n`😀`\n[table]\n[table type]\n`event` 1 2 3\n[/table]\n[table]\n[table type]\n`normal` " + strings.Repeat(scriptUpgradeRow+"\n", 3) + "[/table]\n[table]\n[table type]\n`amplify` " + scriptUpgradeRow + "\n[/table]"
			index, err := a.AddFileText("etc/upgrade.etc", text, pvf.TypeScript)
			if err != nil {
				t.Fatal(err)
			}
			tx := NewTransaction(a)
			defer tx.Rollback()
			host := NewBatchAPI(context.Background(), tx, nil, nil)
			before, err := tx.Stage().Text(index)
			if err != nil {
				t.Fatal(err)
			}
			result, err := NewGojaRuntime().Run(context.Background(), test.source, host)
			if err != nil {
				t.Fatal(err)
			}
			if test.failed {
				if result.Status != RunStatusFailed {
					t.Fatalf("expected failure: %+v", result)
				}
			} else if result.Status != RunStatusCompleted {
				t.Fatalf("run: %+v", result)
			}
			after, _ := tx.Stage().Text(index)
			if test.name != "range" {
				if after != before {
					t.Fatal("invalid batch changed file")
				}
				return
			}
			tokens, _, err := pvf.SelectUpgradeTableTokens(after, "normal")
			if err != nil {
				t.Fatal(err)
			}
			for level := 1; level <= 3; level++ {
				row := tokens[(level-1)*24 : level*24]
				if row[8].Value != "90000" {
					t.Fatal("weapon modified")
				}
				wantRate, wantCount := 88000, 10
				if level >= 2 {
					wantRate, wantCount = level*5000, 10+level*2
				}
				if row[9].Value != strconv.Itoa(wantRate) || row[15].Value != strconv.Itoa(wantCount) {
					t.Fatalf("level %d: %+v", level, row)
				}
			}
			original, _ := a.Text(index)
			if original != before {
				t.Fatal("script changed base archive")
			}
			unchanged, _, _ := pvf.SelectUpgradeTableTokens(after, "amplify")
			if unchanged[9].Value != "88000" || unchanged[15].Value != "10" {
				t.Fatal("wrong table changed")
			}
		})
	}
}

func TestUpgradeSourceOffsets(t *testing.T) {
	text := "`😀`\r\n123\r456"
	offsets := upgradeByteOffsets(text)
	for _, e := range pvf.ParseScriptView(text).Elements {
		if e.TokenType == 0 {
			if text[offsets[e.Start]:offsets[e.End]] != e.Value {
				t.Fatal("invalid CRLF/UTF-16 positions")
			}
		}
	}
}
