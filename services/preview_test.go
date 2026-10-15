package services

import "testing"

func TestPreviewServiceParseANI(t *testing.T) {
	result, err := NewPreviewService().ParseANI("[FRAME MAX]\n1\n[FRAME000]\n[IMAGE]\n`Item/test.img` 3\n[IMAGE POS]\n2 -4\n[DELAY]\n25")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Valid || result.FrameMax != 1 || len(result.Frames) != 1 {
		t.Fatalf("preview result = %#v", result)
	}
	frame := result.Frames[0]
	if frame.Delay != 25 || len(frame.Layers) != 1 {
		t.Fatalf("preview frame = %#v", frame)
	}
	layer := frame.Layers[0]
	if layer.Image.Path != "Item/test.img" || layer.Image.Index != 3 || layer.X != 2 || layer.Y != -4 {
		t.Fatalf("preview layer = %#v", layer)
	}
}

func TestPreviewServiceParseANISharedImagePath(t *testing.T) {
	result, err := NewPreviewService().ParseANI("[FRAME MAX]\n1\n[IMAGE PATH]\n`Item/test.img`\n[/IMAGE PATH]\n[FRAME000]\n[IMAGE EX]\n0 3\n[IMAGE POS]\n2 -4\n[DELAY]\n80")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Valid || len(result.Frames) != 1 || len(result.Frames[0].Layers) != 1 {
		t.Fatalf("preview result = %#v", result)
	}
	layer := result.Frames[0].Layers[0]
	if layer.Image.Path != "Item/test.img" || layer.Image.Index != 3 || layer.X != 2 || layer.Y != -4 || result.Frames[0].Delay != 80 {
		t.Fatalf("preview frame = %#v", result.Frames[0])
	}
}

func TestPreviewServiceParseANIImageExEmptyFrame(t *testing.T) {
	result, err := NewPreviewService().ParseANI("[FRAME MAX]\n1\n[FRAME000]\n[IMAGE EX]\n-1 0\n[IMAGE POS]\n12 0\n[DELAY]\n260")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Valid || len(result.Frames) != 1 || len(result.Frames[0].Layers) != 1 {
		t.Fatalf("preview result = %#v", result)
	}
	frame := result.Frames[0]
	layer := frame.Layers[0]
	if frame.Delay != 260 || layer.Image.Path != "" || layer.Image.Index != -1 || layer.X != 12 || layer.Y != 0 {
		t.Fatalf("empty preview frame = %#v", frame)
	}
}
