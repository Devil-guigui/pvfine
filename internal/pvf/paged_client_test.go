package pvf

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPagedExecutableKeyFallback(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	decoy, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	chain := make([]byte, 15*paged110PageKeySize)
	metadata := make([]byte, paged110PageKeySize)
	padding := make([]byte, 20000)
	for _, data := range [][]byte{chain, metadata, padding} {
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}
	}
	a := New()
	a.format = paged110Profile
	a.keys = paged110Keys()
	a.keys.hash = standardKeys().hash
	a.keys.hashKnown = true
	a.pageKeys = chain
	a.AddFile("etc/padding.txt", padding, TypeUnicode)
	if _, err := a.AddFileText("etc/probe.txt", "client key fallback", TypeUnicode); err != nil {
		t.Fatal(err)
	}
	var packed bytes.Buffer
	if err := a.SaveTo(&packed); err != nil {
		t.Fatal(err)
	}
	wrapped := append([]byte(nil), chain...)
	block, err := aes.NewCipher(metadata)
	if err != nil {
		t.Fatal(err)
	}
	prefix := len(wrapped) &^ (paged110MetadataAlign - 1)
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(wrapped[:prefix], wrapped[:prefix])
	var sealed []byte
	for off := 0; off < len(wrapped); off += 117 {
		end := off + 117
		if end > len(wrapped) {
			end = len(wrapped)
		}
		part, err := rsa.EncryptPKCS1v15(rand.Reader, &key.PublicKey, wrapped[off:end])
		if err != nil {
			t.Fatal(err)
		}
		sealed = append(sealed, part...)
	}
	if _, err := unsealPageKeyTable(sealed); err == nil {
		t.Fatal("synthetic sidecar unexpectedly accepted by embedded key")
	}
	decoyDER, err := x509.MarshalPKCS8PrivateKey(decoy)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"PRIVATE KEY", "RSA PRIVATE KEY"} {
		t.Run(kind, func(t *testing.T) {
			der := x509.MarshalPKCS1PrivateKey(key)
			if kind == "PRIVATE KEY" {
				var err error
				der, err = x509.MarshalPKCS8PrivateKey(key)
				if err != nil {
					t.Fatal(err)
				}
			}
			exe := []byte("-----BEGIN PRIVATE KEY-----\ninvalid\n")
			exe = append(exe, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: decoyDER})...)
			exe = append(exe, []byte(strings.Repeat("0", 64)+"\x00")...)
			exe = append(exe, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})...)
			exe = append(exe, []byte(hex.EncodeToString(metadata)+"\x00")...)
			tables := executablePageKeyTables(exe, sealed)
			if len(tables) != 1 || !bytes.Equal(tables[0].data, wrapped) {
				t.Fatal("client RSA candidate did not recover wrapped table")
			}
			if tables[0].profile.id != FormatPaged {
				t.Fatal("unrecognized RSA candidate acquired a known client version")
			}
			if got := executablePageKeyTables(exe, sealed[:len(sealed)-1]); len(got) != 0 {
				t.Fatal("truncated sidecar accepted")
			}
			corrupt := append([]byte(nil), sealed...)
			clear(corrupt[len(corrupt)-128:])
			if got := executablePageKeyTables(exe, corrupt); len(got) != 0 {
				t.Fatal("partially valid sidecar accepted")
			}
			if got := executablePageKeyTables([]byte("no PEM candidates"), sealed); len(got) != 0 {
				t.Fatal("table recovered without a client key")
			}
			dir := t.TempDir()
			for name, data := range map[string][]byte{
				"Script.pvf":      packed.Bytes(),
				sealedPageKeyName: sealed,
				executableName:    exe,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			b, err := Open(filepath.Join(dir, "Script.pvf"))
			if err != nil {
				t.Fatal(err)
			}
			defer b.Release()
			if b.Format() != FormatPaged || b.ClientVersion() != "" || !b.IsPaged() {
				t.Fatal("unknown paged client was classified as a known version")
			}
			if !bytes.Equal(b.pageKeys, chain) {
				t.Fatal("unwrapped page keys differ (including unaligned tail)")
			}
			index, ok := b.Find("etc/probe.txt")
			if !ok {
				t.Fatal("probe file missing")
			}
			if text, err := b.Text(index); err != nil || text != "client key fallback" {
				t.Fatalf("probe text did not round trip: %v", err)
			}
			var rewritten bytes.Buffer
			if err := b.SaveTo(&rewritten); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(rewritten.Bytes(), packed.Bytes()) {
				t.Fatal("untouched save differs")
			}
			wrong := make([]byte, 32)
			if _, _, _, ok := decryptPagedWith(packed.Bytes(), wrapped, paged110Keys(), wrong); ok {
				t.Fatal("wrong metadata accepted")
			}
			if _, _, _, ok := decryptPagedWith(packed.Bytes(), wrapped[:31], paged110Keys(), metadata); ok {
				t.Fatal("incomplete table accepted")
			}
		})
	}
}

func TestRealPagedClientCompatibility(t *testing.T) {
	path := os.Getenv(testFileEnv)
	if path == "" {
		t.Skip("PVF_TESTFILE required")
	}
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	if !a.IsPaged() {
		t.Skip("requires a paged client archive")
	}
	if want := os.Getenv("PVF_TEST_CLIENT_VERSION"); want != "" && a.ClientVersion() != want {
		t.Fatalf("client version = %q, want %q", a.ClientVersion(), want)
	}
	h := a.Header()
	t.Logf("opened: format=%s version=%s files=%d groups=%d page keys=%d", a.Format(), a.ClientVersion(), a.FileCount(), h.GroupCount, len(a.pageKeys)/32)
	for i := int32(0); i < a.FileCount() && i < 5000; i++ {
		if a.Path(i) == "" {
			t.Fatalf("missing path at index %d", i)
		}
	}
	chunks := 0
	for ci := int32(0); ci < h.GroupCount; ci += 1 + h.GroupCount/50 {
		raw, err := a.Chunk(ci)
		if err != nil || len(raw) != int(a.groups[ci].origSize) {
			t.Fatalf("chunk %d failed: %v", ci, err)
		}
		chunks++
	}
	scripts := 0
	for i := int32(0); i < a.FileCount() && i < 5000 && scripts < 3; i++ {
		if a.items[i].typ != TypeScript {
			continue
		}
		text, err := a.Text(i)
		if err != nil || !strings.Contains(text, "[") {
			t.Fatalf("script index %d failed: %v", i, err)
		}
		scripts++
	}
	if scripts == 0 {
		t.Fatal("no sample scripts found")
	}
	t.Logf("verified paths=5000 chunks=%d scripts=%d", chunks, scripts)
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	want, got := sha256.New(), sha256.New()
	if _, err := io.Copy(want, source); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveTo(got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want.Sum(nil), got.Sum(nil)) {
		t.Fatal("untouched save is not byte-identical")
	}
	t.Log("untouched save SHA256 matches source")
}
