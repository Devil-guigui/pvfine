package pvf

import "fmt"

// upgradeTableTokens selects by each table's own type, including values that
// remain under the unpaired [table type] child until [/table]. Untyped legacy
// files are accepted only when exactly one table is present.
func SelectUpgradeTableTokens(text, tableType string) ([]ScriptElement, bool, error) {
	view := ParseScriptView(text)
	type table struct {
		kind         string
		typeSections int
		awaitingType bool
		values       []ScriptElement
	}
	tables := []*table{}
	var current *table
	for _, e := range view.Elements {
		if e.Kind == ScriptElementSection && e.Section == "table" {
			if len(e.SectionPath) != 1 {
				return nil, false, fmt.Errorf("[table] 必须位于顶层")
			}
			current = &table{}
			tables = append(tables, current)
			continue
		}
		inside := len(e.SectionPath) > 0 && e.SectionPath[0] == "table"
		if current == nil || !inside {
			continue
		}
		if e.Kind == ScriptElementSection && e.Section == "table type" {
			current.typeSections++
			current.awaitingType = true
			continue
		}
		if e.Kind != ScriptElementToken {
			continue
		}
		if current.awaitingType {
			current.kind = e.Value
			current.awaitingType = false
			continue
		}
		current.values = append(current.values, e)
	}
	var selected *table
	for _, candidate := range tables {
		if candidate.kind != tableType {
			continue
		}
		if candidate.typeSections != 1 {
			return nil, false, fmt.Errorf("%s 表存在重复的 [table type]", tableType)
		}
		if selected != nil {
			return nil, false, fmt.Errorf("存在多个 table type = %s 的表，无法确定编辑目标", tableType)
		}
		selected = candidate
	}
	if selected != nil {
		return selected.values, true, nil
	}
	if len(tables) == 1 && tables[0].typeSections == 0 {
		return tables[0].values, false, nil
	}
	return nil, false, fmt.Errorf("未找到 table type = %s 的表", tableType)
}
