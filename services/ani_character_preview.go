package services

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"pvfine/internal/pvf"
)

var aniCharacterPlaceholder = regexp.MustCompile(`%d|%0[1-9][0-9]?d`)

// ParseANIForFile adds archive context to ANI text preview. Image placeholders
// under character/ and aicharacter/ use character.lst directory and
// animation-prefix matching.
func (s *PreviewService) ParseANIForFile(fileIndex int32, text string) (*AniPreviewDocument, error) {
	document, err := s.ParseANI(text)
	if err != nil || s == nil || s.c == nil {
		return document, err
	}
	needsID := false
	for _, frame := range document.Frames {
		for _, layer := range frame.Layers {
			if aniCharacterPlaceholder.MatchString(layer.Image.Path) {
				needsID = true
			}
		}
	}
	if !needsID {
		return document, nil
	}
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	a := s.c.archive
	if a == nil {
		return document, nil
	}
	if err := validateAnnotationIndex(a, fileIndex); err != nil {
		return nil, err
	}
	aniPath := normalizeANICharacterPath(a.Path(fileIndex))
	if !strings.HasPrefix(aniPath, "character/") && !strings.HasPrefix(aniPath, "aicharacter/") {
		return document, nil
	}
	id, err := s.aniCharacterIDLocked(aniPath)
	if err != nil {
		document.Valid = false
		document.Issues = append(document.Issues, PreviewIssue{
			Severity: "error", Line: 1, Section: "IMAGE", Message: "无法替换角色图片通配符：" + err.Error(),
		})
		return document, nil
	}
	for i := range document.Frames {
		for j := range document.Frames[i].Layers {
			image := &document.Frames[i].Layers[j].Image
			image.Path = aniCharacterPlaceholder.ReplaceAllStringFunc(image.Path, func(format string) string {
				width := 0
				if format != "%d" {
					width, _ = strconv.Atoi(format[2 : len(format)-1])
				}
				return fmt.Sprintf("%0*d", width, id)
			})
		}
	}
	return document, nil
}

func (s *PreviewService) aniCharacterIDLocked(aniPath string) (int64, error) {
	const listPath = "character/character.lst"
	listIndex, ok := s.c.archive.FindList(listPath)
	if !ok {
		return 0, fmt.Errorf("找不到 %s", listPath)
	}
	text, err := s.previewRelatedTextLocked(listIndex)
	if err != nil {
		return 0, fmt.Errorf("读取 %s 失败：%w", listPath, err)
	}
	pairs, err := pvf.ParseListText(text)
	if err != nil {
		return 0, fmt.Errorf("解析 %s 失败：%w", listPath, err)
	}
	bestPrefix := ""
	characters := make([]pvf.ListPair, 0)
	normalized := make([]pvf.ListPair, 0, len(pairs))
	for _, pair := range pairs {
		chrPath := normalizeANICharacterPath(pair.Path)
		if !strings.HasPrefix(chrPath, "character/") {
			chrPath, ok = resolveListPath(listPath, chrPath)
			if !ok {
				continue
			}
		}
		if path.Ext(chrPath) != ".chr" {
			continue
		}
		normalized = append(normalized, pvf.ListPair{ID: pair.ID, Path: chrPath})
	}
	if strings.HasPrefix(aniPath, "aicharacter/") {
		return aiANICharacterID(aniPath, normalized)
	}
	for _, pair := range normalized {
		chrPath := pair.Path
		prefix := path.Dir(chrPath) + "/"
		if !strings.HasPrefix(aniPath, prefix) || len(prefix) < len(bestPrefix) {
			continue
		}
		if len(prefix) > len(bestPrefix) {
			bestPrefix = prefix
			characters = characters[:0]
		}
		characters = append(characters, pvf.ListPair{ID: pair.ID, Path: chrPath})
	}
	if bestPrefix == "" {
		return 0, fmt.Errorf("%s 中没有匹配 ANI 目录的 chr 条目", listPath)
	}
	// swordman/atanimation/... maps to swordman/atswordman.chr.
	// Match a complete directory segment, including nested animation files.
	matched := make([]pvf.ListPair, 0, 1)
	for _, character := range characters {
		directory := path.Dir(character.Path)
		base := path.Base(directory)
		stem := strings.TrimSuffix(path.Base(character.Path), ".chr")
		if !strings.HasSuffix(stem, base) {
			continue
		}
		prefix := strings.TrimSuffix(stem, base)
		animationPath := directory + "/" + prefix + "animation/"
		if strings.HasPrefix(aniPath, animationPath) {
			matched = append(matched, character)
		}
	}
	if len(matched) > 0 {
		characters = matched
	} else if directory := strings.SplitN(strings.TrimPrefix(aniPath, bestPrefix), "/", 2)[0]; strings.HasSuffix(directory, "animation") {
		return 0, fmt.Errorf("%s 中没有匹配 %s 目录前缀的 chr", listPath, directory)
	}
	if len(characters) != 1 {
		return 0, fmt.Errorf("%s 中无法按 animation 目录前缀唯一匹配 chr", listPath)
	}
	return parseANICharacterID(characters[0].ID)
}

func normalizeANICharacterPath(value string) string {
	return strings.ToLower(path.Clean(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")))
}

// AI character scripts may put the job name anywhere in their directory tree.
// Match complete directory names against chr stems, retaining animation-prefix
// matching for shared base-job directories such as swordman/atanimation/.
func aiANICharacterID(aniPath string, characters []pvf.ListPair) (int64, error) {
	parts := strings.Split(strings.TrimPrefix(path.Dir(aniPath), "aicharacter/"), "/")
	matched := make(map[string]struct{})
	for _, character := range characters {
		stem := strings.TrimSuffix(path.Base(character.Path), ".chr")
		base := path.Base(path.Dir(character.Path))
		for i, part := range parts {
			if part == stem && stem != base {
				matched[character.ID] = struct{}{}
				break
			}
			if part != base || !strings.HasSuffix(stem, base) {
				continue
			}
			animationPrefix := ""
			for _, directory := range parts[i+1:] {
				if strings.HasSuffix(directory, "animation") {
					animationPrefix = strings.TrimSuffix(directory, "animation")
					break
				}
			}
			if animationPrefix == strings.TrimSuffix(stem, base) {
				matched[character.ID] = struct{}{}
			}
		}
	}
	if len(matched) != 1 {
		return 0, fmt.Errorf("character/character.lst 中没有匹配 AI 路径职业标识的 chr，或存在多个匹配 ID")
	}
	value := ""
	for id := range matched {
		value = id
	}
	return parseANICharacterID(value)
}

func parseANICharacterID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("chr ID %q 不是非负整数", value)
	}
	return id, nil
}
