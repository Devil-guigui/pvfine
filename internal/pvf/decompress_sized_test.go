package pvf

import (
	"bytes"
	"testing"
)

func TestZlibDecompressSizedValidatesLengthAndChecksum(t *testing.T) {
	for _, raw := range [][]byte{nil, bytes.Repeat([]byte("pool"), 4096)} {
		packed, err := zlibCompress(raw)
		if err != nil {
			t.Fatal(err)
		}
		out, err := zlibDecompressSized(packed, len(raw))
		if err != nil || !bytes.Equal(out, raw) || cap(out) != len(raw) {
			t.Fatalf("exact decompression failed: %v", err)
		}
		for _, size := range []int{-1, len(raw) + 1, len(raw) - 1} {
			if _, err := zlibDecompressSized(packed, size); err == nil {
				t.Fatalf("accepted incorrect length %d", size)
			}
		}
		packed[len(packed)-1] ^= 1
		if _, err := zlibDecompressSized(packed, len(raw)); err == nil {
			t.Fatal("accepted corrupt checksum")
		}
	}
}
