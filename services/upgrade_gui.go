package services

import (
	"fmt"
	"math"
	"strconv"

	"pvfine/internal/pvf"
)

// UpgradeRow exposes only the five editable columns. Level follows the source
// tool's row order. Fields holds weapon settings and shared materials;
// FieldsT2 holds armor probability, failure operation, and downgrade parameter.
// All other table parameters retain their source text.
type UpgradeRow struct {
	Level    int           `json:"level"`
	Fields   []SkillNumber `json:"fields"`
	FieldsT2 []SkillNumber `json:"fieldsT2"`
	CostName string        `json:"costName"`
}

type UpgradeDocument struct {
	Rows []UpgradeRow `json:"rows"`
}

func isUpgradePath(path string) bool {
	return sameSearchPath(path, "etc/upgrade.etc") || sameSearchPath(path, "etc/amplifyupgrade.etc")
}

func (s *FileGUIService) ReadUpgrade(fileIndex int32, text string) (*UpgradeDocument, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	a := s.c.archive
	if a == nil {
		return nil, ErrNoArchive
	}
	if err := validateAnnotationIndex(a, fileIndex); err != nil {
		return nil, err
	}
	if !isUpgradePath(a.Path(fileIndex)) {
		return nil, fmt.Errorf("强化/增幅 GUI 仅支持 etc/upgrade.etc 和 etc/amplifyupgrade.etc")
	}
	if len(text) > int(s.c.editableByteLimit()) {
		return nil, fmt.Errorf("强化/增幅文本过大")
	}
	tableType := "normal"
	if sameSearchPath(a.Path(fileIndex), "etc/amplifyupgrade.etc") {
		tableType = "amplify"
	}
	doc, err := parseUpgrade(text, tableType)
	if err != nil {
		return nil, err
	}
	for i := range doc.Rows {
		row := &doc.Rows[i]
		id := strconv.FormatInt(int64(row.Fields[3].Value), 10)
		if s.c.annotationEngine != nil {
			if ref, ok := s.c.resolveAnnotationReferenceLocked("物品", id); ok {
				row.CostName = resolvePreviewText(a, ref.Name)
			}
		}
	}
	return doc, nil
}

func upgradeTableTokens(text, tableType string) ([]pvf.ScriptElement, bool, error) {
	return pvf.SelectUpgradeTableTokens(text, tableType)
}

func parseUpgrade(text, tableType string) (*UpgradeDocument, error) {
	tokens, typed, err := upgradeTableTokens(text, tableType)
	if err != nil {
		return nil, err
	}
	values := []SkillNumber{}
	for _, e := range tokens {
		n, err := strconv.ParseFloat(e.Value, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || (e.TokenType != 0 && e.TokenType != 2) {
			return nil, fmt.Errorf("[table] 包含非数值数据")
		}
		values = append(values, SkillNumber{Value: n, Start: e.Start, End: e.End, TokenType: e.TokenType})
	}
	width := 17
	columns := []int{6, 7, 8, 9, 10}
	if typed {
		width = 24
		columns = []int{8, 10, 11, 14, 15}
	}
	if len(values) == 0 || len(values)%width != 0 {
		return nil, fmt.Errorf("[table] 每级需要 %d 个数值，当前共 %d 项", width, len(values))
	}
	doc := &UpgradeDocument{Rows: []UpgradeRow{}}
	limit := len(values) / width
	// The legacy editor exposes 31 rows. Typed tables expose every level.
	if !typed && limit > 31 {
		limit = 31
	}
	for level := 0; level < limit; level++ {
		offset := level * width
		fields := make([]SkillNumber, 0, 5)
		for _, column := range columns {
			fields = append(fields, values[offset+column])
		}
		row := UpgradeRow{Level: level + 1, Fields: fields, FieldsT2: []SkillNumber{}}
		if typed {
			for _, column := range []int{9, 12, 13} {
				row.FieldsT2 = append(row.FieldsT2, values[offset+column])
			}
		}
		allFields := append(append([]SkillNumber(nil), row.Fields...), row.FieldsT2...)
		for _, field := range allFields {
			if field.TokenType != 0 || field.Value < 0 || field.Value > math.MaxInt32 {
				return nil, fmt.Errorf("第 %d 级：可编辑字段必须是非负 32 位整数", level+1)
			}
		}
		if fields[0].Value > 100000 || fields[1].Value < 1 || fields[1].Value > 3 || (!typed && fields[2].Value > 31) {
			return nil, fmt.Errorf("第 %d 级：失败概率、失败操作或降级参数超出范围", level+1)
		}
		if typed && (row.FieldsT2[0].Value > 100000 || row.FieldsT2[1].Value < 1 || row.FieldsT2[1].Value > 3) {
			return nil, fmt.Errorf("第 %d 级：防具失败概率或失败操作超出范围", level+1)
		}
		doc.Rows = append(doc.Rows, row)
	}
	return doc, nil
}
