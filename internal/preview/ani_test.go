package preview

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseANIStandardAnimation(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("[LOOP]\n\t1\n\n[SHADOW]\n\t0\n\n[FRAME MAX]\n\t105\n")
	for index := 0; index < 105; index++ {
		fmt.Fprintf(&builder, "\n[FRAME%03d]\n\n[IMAGE]\n\t`Item/Title/15_summer_2.img`\t%d\n\n[IMAGE POS]\n\t0\t0\n\n[DELAY]\n\t100\n", index, index)
	}

	document := ParseANI(builder.String())
	if !document.Valid {
		t.Fatalf("document invalid: %#v", document.Issues)
	}
	if !document.Loop || document.Shadow || document.FrameMax != 105 || len(document.Frames) != 105 {
		t.Fatalf("document metadata = %#v", document)
	}
	for index, frame := range document.Frames {
		if frame.Index != int32(index) || frame.Delay != 100 || len(frame.Layers) != 1 {
			t.Fatalf("frame %d = %#v", index, frame)
		}
		layer := frame.Layers[0]
		if layer.Path != "Item/Title/15_summer_2.img" || layer.Index != int32(index) || layer.X != 0 || layer.Y != 0 {
			t.Fatalf("frame %d layer = %#v", index, layer)
		}
	}
}

func TestParseANIMultipleLayersAndQuotedComments(t *testing.T) {
	text := "[LOOP] # play once\n" +
		" 0\n[SHADOW]\n 1\n[FRAME MAX]\n 1\n[FRAME000]\n" +
		"[IMAGE]\n  `Item/a``b.img` 2 # first layer\n[IMAGE POS]\n" +
		" -3 4\n[IMAGE]\n  `Item/second.img` 5\n[IMAGE POS]\n" +
		" 6 -7\n[DELAY]\n 80\n"

	document := ParseANI(text)
	if !document.Valid || document.Loop || !document.Shadow {
		t.Fatalf("document = %#v", document)
	}
	if len(document.Frames) != 1 || len(document.Frames[0].Layers) != 2 {
		t.Fatalf("layers = %#v", document.Frames)
	}
	first, second := document.Frames[0].Layers[0], document.Frames[0].Layers[1]
	if first.Path != "Item/a`b.img" || first.Index != 2 || first.X != -3 || first.Y != 4 {
		t.Fatalf("first layer = %#v", first)
	}
	if second.Path != "Item/second.img" || second.Index != 5 || second.X != 6 || second.Y != -7 {
		t.Fatalf("second layer = %#v", second)
	}
}

func TestParseANIRecoverableDefaultsAndStructuralErrors(t *testing.T) {
	text := "[FRAME MAX]\n 2\n[FRAME000]\n[IMAGE]\n `Item/a.img` 0\n" +
		"[FRAME002]\n[DELAY]\n 0\n"

	document := ParseANI(text)
	if document.Valid {
		t.Fatal("malformed document should not be valid")
	}
	if len(document.Frames) != 2 || document.Frames[0].Layers[0].X != 0 || document.Frames[0].Layers[0].Y != 0 {
		t.Fatalf("best effort frames = %#v", document.Frames)
	}
	if document.Frames[0].Delay != 100 || document.Frames[1].Delay != 100 {
		t.Fatalf("default delays = %#v", document.Frames)
	}
	assertIssue(t, document.Issues, IssueWarning, "图片缺少 [IMAGE POS]")
	assertIssue(t, document.Issues, IssueWarning, "帧缺少 [DELAY]")
	assertIssue(t, document.Issues, IssueWarning, "必须是正整数")
	assertIssue(t, document.Issues, IssueError, "帧编号不连续")
}

func TestParseANIDuplicateFrameAndMissingImage(t *testing.T) {
	text := "[FRAME MAX]\n 2\n[FRAME000]\n[FRAME000]\n[FRAME001]\n" +
		"[DELAY]\n 100\n"

	document := ParseANI(text)
	if document.Valid {
		t.Fatal("document with duplicate/missing frames should not be valid")
	}
	assertIssue(t, document.Issues, IssueError, "重复的帧编号")
	assertIssue(t, document.Issues, IssueError, "帧没有有效的 [IMAGE]")
}

func TestParseANIRejectsInvalidFrameNameAndImageIndex(t *testing.T) {
	text := "[FRAME MAX]\n 1\n[FRAMEoops]\n[IMAGE]\n `Item/test.img` -1\n"

	document := ParseANI(text)
	if document.Valid {
		t.Fatal("document with invalid frame syntax should not be valid")
	}
	assertIssue(t, document.Issues, IssueError, "帧 section 名称必须是 FRAME 加数字")
	assertIssue(t, document.Issues, IssueError, "声明帧数为 1，实际解析到 0 帧")

	invalidIndex := ParseANI("[FRAME MAX]\n 1\n[FRAME000]\n[IMAGE]\n `Item/test.img` -1\n")
	if invalidIndex.Valid {
		t.Fatal("document with invalid image index should not be valid")
	}
	assertIssue(t, invalidIndex.Issues, IssueError, "图片索引必须是非负整数")
}

func TestParseANIAllowsEmptyImageFrame(t *testing.T) {
	document := ParseANI("[FRAME MAX]\n 1\n[FRAME000]\n[IMAGE]\n[IMAGE POS]\n 3 4\n[DELAY]\n 50\n")
	if !document.Valid || len(document.Frames) != 1 || len(document.Frames[0].Layers) != 1 {
		t.Fatalf("empty image document = %#v", document)
	}
	layer := document.Frames[0].Layers[0]
	if layer.Path != "" || layer.Index != -1 || layer.X != 3 || layer.Y != 4 {
		t.Fatalf("empty image layer = %#v", layer)
	}
	assertIssue(t, document.Issues, IssueWarning, "图片引用为空")
}

func assertIssue(t *testing.T, issues []Issue, severity IssueSeverity, message string) {
	t.Helper()
	for _, issue := range issues {
		if issue.Severity == severity && strings.Contains(issue.Message, message) {
			return
		}
	}
	t.Fatalf("missing %s issue containing %q: %#v", severity, message, issues)
}

func TestParseANIFrameFlagsStayInFrame(t *testing.T) {
	text := "[LOOP]\n1\n[SHADOW]\n1\n[FRAME MAX]\n1\n[FRAME000]\n\t[IMAGE]\n\t\t`test.img` 0\n\t[LOOP]\n\t\t0\n\t[SHADOW]\n\t\t0\n\t[DELAY]\n\t\t75\n"
	doc := ParseANI(text)
	if !doc.Valid || !doc.Loop || !doc.Shadow || len(doc.Frames) != 1 || doc.Frames[0].Delay != 75 {
		t.Fatalf("frame flags escaped into global scope: %+v", doc)
	}
	// Unindented global sections after frames retain the existing behavior.
	after := ParseANI("[FRAME MAX]\n1\n[FRAME000]\n[IMAGE]\n`test.img` 0\n[DELAY]\n75\n[LOOP]\n1\n")
	if !after.Valid || !after.Loop {
		t.Fatal("global section after frame stopped working")
	}
}

func TestParseANISharedImagePath(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("[LOOP]\n1\n[FRAME MAX]\n50\n[IMAGE PATH]\n`Item/Title/2012dnf_festival_yellow.img`\n[/IMAGE PATH]\n")
	for index := 0; index < 50; index++ {
		fmt.Fprintf(&builder, "[FRAME%03d]\n[IMAGE EX]\n0 %d\n[DELAY]\n80\n", index, index)
	}
	document := ParseANI(builder.String())
	if !document.Valid || !document.Loop || len(document.Frames) != 50 {
		t.Fatalf("shared image path document = %#v", document)
	}
	for index, frame := range document.Frames {
		if frame.Delay != 80 || len(frame.Layers) != 1 {
			t.Fatalf("frame %d = %#v", index, frame)
		}
		want := Layer{Path: "Item/Title/2012dnf_festival_yellow.img", Index: int32(index)}
		if frame.Layers[0] != want {
			t.Fatalf("frame %d layer = %#v, want %#v", index, frame.Layers[0], want)
		}
	}
}

func TestParseANISharedPathsAndMixedLayers(t *testing.T) {
	document := ParseANI("[FRAME MAX]\n1\n[FRAME000]\n" +
		"[IMAGE EX]\n1 7\n[IMAGE POS]\n-3 4\n" +
		"[IMAGE]\n`Item/direct.img` 2\n[IMAGE POS]\n5 -6\n" +
		"[IMAGE EX]\n0 9\n[IMAGE POS]\n1 2\n[DELAY]\n80\n" +
		"[IMAGE PATH]\n`Item/first.img`\n`Item/second.img`\n[/IMAGE PATH]\n")
	if !document.Valid || len(document.Issues) != 0 || len(document.Frames) != 1 {
		t.Fatalf("mixed document = %#v", document)
	}
	want := []Layer{
		{Path: "Item/second.img", Index: 7, X: -3, Y: 4},
		{Path: "Item/direct.img", Index: 2, X: 5, Y: -6},
		{Path: "Item/first.img", Index: 9, X: 1, Y: 2},
	}
	got := document.Frames[0].Layers
	if len(got) != len(want) {
		t.Fatalf("layers = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("layer %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestParseANIRejectsInvalidImageEx(t *testing.T) {
	for _, test := range []struct {
		name, paths, reference, message string
	}{
		{"missing table", "", "0 1", "路径索引必须"},
		{"missing index", "`a.img`", "0", "需要路径索引和图片索引"},
		{"negative path", "`a.img`", "-2 0", "路径索引必须"},
		{"out of range", "`a.img`", "1 0", "路径索引必须"},
		{"invalid path", "`a.img`", "x 0", "路径索引必须"},
		{"overflow path", "`a.img`", "2147483648 0", "路径索引必须"},
		{"empty path", "``", "0 0", "图片路径不能为空"},
		{"negative image", "`a.img`", "0 -1", "图片索引必须"},
		{"invalid image", "`a.img`", "0 x", "图片索引必须"},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := ParseANI("[FRAME MAX]\n1\n[IMAGE PATH]\n" + test.paths +
				"\n[/IMAGE PATH]\n[FRAME000]\n[IMAGE EX]\n" + test.reference + "\n[DELAY]\n80\n")
			if document.Valid || len(document.Frames) != 1 || len(document.Frames[0].Layers) != 0 {
				t.Fatalf("invalid reference document = %#v", document)
			}
			assertIssue(t, document.Issues, IssueError, test.message)
		})
	}
}

func TestParseANIImageExEmptyFrame(t *testing.T) {
	for _, paths := range []string{"", "[IMAGE PATH]\n`Effect/WaveAuraEff.img`\n[/IMAGE PATH]\n"} {
		document := ParseANI("[FRAME MAX]\n2\n" + paths +
			"[FRAME000]\n[IMAGE EX]\n-1 0\n[IMAGE POS]\n12 0\n[IMAGE RATE]\n0.4124 0.4124\n[DELAY]\n260\n" +
			"[FRAME001]\n[IMAGE]\n`Effect/WaveAuraEff.img` 16\n[IMAGE POS]\n-507 -631\n[DELAY]\n72\n")
		if !document.Valid || len(document.Issues) != 0 || len(document.Frames) != 2 {
			t.Fatalf("empty frame document = %#v", document)
		}
		first := document.Frames[0]
		if first.Delay != 260 || len(first.Layers) != 1 || first.Layers[0] != (Layer{Index: -1, X: 12}) {
			t.Fatalf("empty frame = %#v", first)
		}
		second := document.Frames[1]
		if second.Delay != 72 || len(second.Layers) != 1 || second.Layers[0] != (Layer{Path: "Effect/WaveAuraEff.img", Index: 16, X: -507, Y: -631}) {
			t.Fatalf("following frame = %#v", second)
		}
	}
}
