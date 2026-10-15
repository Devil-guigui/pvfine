package pvf

import (
	"bytes"
	"path/filepath"
	"strings"
)

// ACT60 source remains native ANSI on disk. The editor's semantic APIs consume
// the same token representation as binary PVF scripts, built once at open.
func isACT60Script(path, source string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".lst", ".equ", ".stk", ".ani", ".act", ".atk", ".obj", ".mob", ".skl", ".map", ".dgn", ".qst", ".etc", ".ai", ".aic", ".til", ".ptl":
		return true
	case ".str", ".txt", ".nut":
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(act60ScriptSource(source)), "[")
}

// Adapt source-only syntax before using the common tokenizer. Quoted strings
// (including doubled backticks) remain intact; comments cannot create tags.
func act60ScriptSource(source string) string {
	rs := []rune(source)
	var out strings.Builder
	for i := 0; i < len(rs); {
		if rs[i] == '`' {
			_, end, ok := readBacktickString(rs, i)
			if ok {
				out.WriteString(string(rs[i:end]))
				i = end
				continue
			}
		}
		if rs[i] == '/' && i+1 < len(rs) {
			if rs[i+1] == '/' {
				for i < len(rs) && rs[i] != '\n' {
					i++
				}
				out.WriteRune('\n')
				continue
			}
			if rs[i+1] == '*' {
				i += 2
				for i+1 < len(rs) && !(rs[i] == '*' && rs[i+1] == '/') {
					i++
				}
				i = min(i+2, len(rs))
				out.WriteRune(' ')
				continue
			}
		}
		if rs[i] == '<' {
			end := indexRune(rs, i+1, '>')
			if end > i {
				value := string(rs[i : end+1])
				if _, _, ok := ParsePlaceholder(value); ok {
					out.WriteString("{8=`" + strings.ReplaceAll(value, "`", "``") + "`}")
					i = end + 1
					continue
				}
			}
		}
		out.WriteRune(rs[i])
		i++
	}
	return out.String()
}

func (a *Archive) encodeACT60Script(text string) ([]byte, error) {
	return a.encodeScript(text)
}

func (a *Archive) act60ScriptText(index int32) (string, error) {
	if text, ok := a.act60Sources[a.FileIdentity(index)]; ok {
		return text, nil
	}
	if _, changed := a.overlay[index]; !changed && a.items[index].chunk >= 0 {
		native, err := a.act60.payload(a.data, a.items[index].chunk)
		if err != nil {
			return "", err
		}
		return a.decodeNativeText(native), nil
	}
	raw, err := a.RawBytes(index)
	if err != nil {
		return "", err
	}
	return act60NativeSource(a.decodeScriptForPath(raw, a.Path(index))), nil
}

func (a *Archive) setACT60ScriptText(index int32, text string) error {
	previous, err := a.act60ScriptText(index)
	if err != nil {
		return err
	}
	if previous == text {
		return nil
	}
	raw, err := a.encodeACT60Script(text)
	if err != nil {
		return err
	}
	before, err := a.RawBytes(index)
	if err != nil {
		return err
	}
	if err := a.SetRawBytes(index, raw); err != nil {
		return err
	}
	// Comments and whitespace are meaningful source edits even when their
	// semantic tokens are unchanged. Preserve those edits for native saving.
	if bytes.Equal(before, raw) {
		a.overlay[index] = bytes.Clone(raw)
		a.recordMutation(index, a.Path(index), MutationModified)
	}
	if a.act60Sources == nil {
		a.act60Sources = make(map[uint64]string)
	}
	a.act60Sources[a.FileIdentity(index)] = text
	return nil
}

// The common renderer exposes binary token variants as {N=`value`} markers.
// Native ACT60 source uses plain references or backtick strings instead.
func act60NativeSource(text string) string {
	rs := []rune(text)
	var out strings.Builder
	for i := 0; i < len(rs); {
		if rs[i] == '`' {
			_, end, ok := readBacktickString(rs, i)
			if ok {
				out.WriteString(string(rs[i:end]))
				i = end
				continue
			}
		}
		if rs[i] == '/' && i+1 < len(rs) && (rs[i+1] == '/' || rs[i+1] == '*') {
			start := i
			if rs[i+1] == '/' {
				for i < len(rs) && rs[i] != '\n' {
					i++
				}
			} else {
				i += 2
				for i+1 < len(rs) && !(rs[i] == '*' && rs[i+1] == '/') {
					i++
				}
				i = min(i+2, len(rs))
			}
			out.WriteString(string(rs[start:i]))
			continue
		}
		if rs[i] == '{' {
			if end := findMarkerEnd(rs, i+1); end > i {
				if typ, value, ok := parseTextMarker(string(rs[i : end+1])); ok {
					if _, _, ref := ParsePlaceholder(value); typ == 8 && ref {
						out.WriteString(value)
					} else {
						out.WriteString("`" + strings.ReplaceAll(value, "`", "``") + "`")
					}
					i = end + 1
					continue
				}
			}
		}
		out.WriteRune(rs[i])
		i++
	}
	return out.String()
}

func (a *Archive) copyACT60SourceFrom(stage *Archive, index int32, path string, raw []byte) {
	if a.act60 == nil {
		return
	}
	staged, ok := stage.Find(path)
	if !ok {
		return
	}
	stagedRaw, ok := stage.overlay[staged]
	if !ok || !bytes.Equal(stagedRaw, raw) {
		return
	}
	if source, ok := stage.act60Sources[stage.FileIdentity(staged)]; ok {
		if a.act60Sources == nil {
			a.act60Sources = make(map[uint64]string)
		}
		if _, changed := a.overlay[index]; !changed {
			a.overlay[index] = bytes.Clone(raw)
			a.recordMutation(index, a.Path(index), MutationModified)
		}
		a.act60Sources[a.FileIdentity(index)] = source
	}
}
