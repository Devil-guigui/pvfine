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
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
	// HASH recovery requires at least 64 bytes of structural evidence.
	for _, path := range []string{"etc/a.txt", "etc/b.txt", "etc/c.txt", "etc/d.txt"} {
		if _, err := a.AddFileText(path, "untouched", TypeUnicode); err != nil {
			t.Fatal(err)
		}
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
	for _, owned := range []bool{false, true} {
		input := bytes.Clone(packed.Bytes())
		logical, _, _, ok := decryptPagedWithOwnership(input, wrapped, paged110Keys(), metadata, owned)
		if !ok || (len(logical) > 0 && (&logical[0] == &input[0]) != owned) {
			t.Fatalf("paged buffer ownership failed: owned=%t", owned)
		}
		if !owned && !bytes.Equal(input, packed.Bytes()) {
			t.Fatal("paged parsing changed caller bytes")
		}
		if err := writePageGuarded(&bytes.Buffer{}, logical, chain); err != nil {
			t.Fatal(err)
		}
	}
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
	if _, err := unsealPageKeyTableWithKey(sealed, mustBundledPagedClient(t, FormatPaged110).key); err == nil {
		t.Fatal("synthetic sidecar unexpectedly accepted by embedded key")
	}
	decoyDER, err := x509.MarshalPKCS8PrivateKey(decoy)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name, kind string
		decoyDFO   bool
	}{
		{executableName, "PRIVATE KEY", false},
		{executableName, "RSA PRIVATE KEY", false},
		{cnExecutableName, "PRIVATE KEY", false},
		{cnExecutableName, "RSA PRIVATE KEY", true},
	} {
		t.Run(scenario.name+"/"+scenario.kind, func(t *testing.T) {
			kind := scenario.kind
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
				scenario.name:     exe,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.decoyDFO {
				if err := os.WriteFile(filepath.Join(dir, executableName), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: decoyDER}), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			missingDir := t.TempDir()
			if _, err := parse(packed.Bytes(), missingDir); !errors.Is(err, ErrBadSignature) ||
				!strings.Contains(err.Error(), "同一目录") || !strings.Contains(err.Error(), "sk.dat") {
				t.Fatal("missing fallback files did not provide placement guidance")
			}
			if err := os.WriteFile(filepath.Join(missingDir, sealedPageKeyName), sealed, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := parse(packed.Bytes(), missingDir); !errors.Is(err, ErrPagedKeys) ||
				!strings.Contains(err.Error(), "DNF.exe") {
				t.Fatal("missing client executable did not provide placement guidance")
			}
			foundMetadata := false
			for _, candidate := range pagedMetadataCandidates(exe) {
				if bytes.Equal(candidate, metadata) {
					foundMetadata = true
					break
				}
			}
			if !foundMetadata {
				t.Fatal("client metadata candidate was not discovered")
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
			if !b.WriteCapabilities().CanChangeStructure {
				t.Fatal("synthetic HASH was not recovered")
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
			if err := b.SetText(index, "国服编辑测试"); err != nil {
				t.Fatal(err)
			}
			if _, err := b.AddFileText("etc/added.txt", "[name]\n`新增测试`", TypeScript); err != nil {
				t.Fatal(err)
			}
			var edited bytes.Buffer
			if err := b.SaveTo(&edited); err != nil {
				t.Fatal(err)
			}
			reopened, err := parse(edited.Bytes(), dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Release()
			assertSavedHashMatchesFiles(t, reopened)
			if text, err := reopened.Text(mustFind(reopened, "etc/probe.txt")); err != nil || text != "国服编辑测试" {
				t.Fatalf("edited content did not round trip: %v", err)
			}
			if text, err := reopened.Text(mustFind(reopened, "etc/added.txt")); err != nil || !strings.Contains(text, "新增测试") {
				t.Fatalf("added script did not round trip: %v", err)
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
	a, err := OpenWithSidecars(path, os.Getenv("PVF_TEST_SIDECAR_DIR"))
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
	if os.Getenv("PVF_TEST_FULL_SCAN") == "1" {
		for ci := int32(0); ci < h.GroupCount; ci++ {
			raw, err := a.Chunk(ci)
			if err != nil || len(raw) != int(a.groups[ci].origSize) {
				t.Fatalf("full scan chunk %d failed: %v", ci, err)
			}
		}
		for i, item := range a.items {
			if item.chunk < 0 || int(item.chunk) >= len(a.groups) || item.off < 0 || item.size < 0 ||
				int64(item.off)+int64(item.size) > int64(a.groups[item.chunk].origSize) {
				t.Fatalf("file span outside chunk at index %d", i)
			}
		}
		t.Logf("full scan verified chunks=%d file spans=%d", h.GroupCount, len(a.items))
	}
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

func TestRealPagedClientEditRoundTrip(t *testing.T) {
	path := os.Getenv(testFileEnv)
	if path == "" {
		t.Skip("PVF_TESTFILE required")
	}
	dir := os.Getenv("PVF_TEST_SIDECAR_DIR")
	a, err := OpenWithSidecars(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	if !a.IsPaged() {
		t.Skip("requires a paged archive")
	}
	if !a.WriteCapabilities().CanChangeStructure {
		t.Fatal("HASH recovery did not enable structural edits")
	}
	var targets []int32
	for i, item := range a.items {
		if item.typ == TypeScript {
			targets = append(targets, int32(i))
			if len(targets) == 2 {
				break
			}
		}
	}
	if len(targets) != 2 {
		t.Fatal("requires two script entries")
	}
	editedPath, removedPath := a.Path(targets[0]), a.Path(targets[1])
	untouched := map[string][]byte{}
	for i := int32(0); i < a.FileCount(); i += 1 + a.FileCount()/20 {
		p := a.Path(i)
		if p == editedPath || p == removedPath {
			continue
		}
		if found, ok := a.Find(p); !ok || found != i {
			continue
		}
		raw, err := a.RawBytes(i)
		if err != nil {
			t.Fatal(err)
		}
		untouched[p] = bytes.Clone(raw)
	}
	beforeCount, beforeA := a.FileCount(), len(a.strA)
	if err := a.SetText(targets[0], "[name]\n`编辑兼容测试`\n[rarity]\n4"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RemoveFiles(targets[1:]); err != nil {
		t.Fatal(err)
	}
	const addedPath = "equipment/zz_pvf_compatibility/new.equ"
	if _, exists := a.Find(addedPath); exists {
		t.Fatal("test addition already exists in fixture")
	}
	if _, err := a.AddFileText(addedPath, "[name]\n`新增兼容测试`", TypeScript); err != nil {
		t.Fatal(err)
	}
	listIndex, hasList := a.Find("list/equipment.lst")
	const companion = "list/equipment_indexhash.etc"
	_, hasCompanion := a.Find(companion)
	var registeredID uint32
	if hasList && hasCompanion {
		delete(untouched, "list/equipment.lst")
		delete(untouched, companion)
		pairs, err := a.ListPairs(listIndex)
		if err != nil {
			t.Fatal(err)
		}
		used := map[string]bool{}
		for _, pair := range pairs {
			used[pair.ID] = true
		}
		registeredID = 900000001
		for used[strconv.FormatUint(uint64(registeredID), 10)] {
			registeredID++
		}
		if err := a.SetListPair(listIndex, strconv.FormatUint(uint64(registeredID), 10), addedPath); err != nil {
			t.Fatal(err)
		}
		if err := a.SetIndexHashEntriesForListIDs(companion, "list/equipment.lst", []uint32{registeredID}); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "edited.pvf")
	if err := a.SaveAs(out); err != nil {
		t.Fatal(err)
	}
	b, err := OpenWithSidecars(out, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release()
	if b.ClientVersion() != a.ClientVersion() || b.ContentRules() != a.ContentRules() || b.FileCount() != beforeCount {
		t.Fatal("version, content policy or file count changed")
	}
	if !a.ContentRules().UTF16Only && (len(b.strA) < beforeA || b.StringOffset("new ASCII pool probe")&1 != 0) {
		t.Fatal("mixed-pool client lost ASCII pool policy")
	}
	if b.StringOffset("中文池探测")&1 == 0 {
		t.Fatal("Chinese text did not use UTF-16 pool")
	}
	assertSavedHashMatchesFiles(t, b)
	if _, exists := b.Find(removedPath); exists {
		t.Fatal("removed entry reappeared")
	}
	for p, want := range map[string]string{editedPath: "编辑兼容测试", addedPath: "新增兼容测试"} {
		index, exists := b.Find(p)
		if !exists {
			t.Fatal("edited or added entry missing")
		}
		text, err := b.Text(index)
		if err != nil || !strings.Contains(text, want) {
			t.Fatalf("edited or added script failed to round trip: %v", err)
		}
	}
	for p, want := range untouched {
		index, exists := b.Find(p)
		if !exists {
			t.Fatal("untouched entry missing")
		}
		got, err := b.RawBytes(index)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("untouched payload changed: %v", err)
		}
	}
	if registeredID != 0 {
		pairs, err := b.ListPairs(mustFind(b, "list/equipment.lst"))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, pair := range pairs {
			if pair.ID == strconv.FormatUint(uint64(registeredID), 10) && pair.Path == addedPath {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("new item registration lost")
		}
		if ids, err := b.IndexHashIDsNeedingUpdate(companion, []uint32{registeredID}); err != nil || len(ids) != 0 {
			t.Fatalf("registered indexhash is not canonical: %v", err)
		}
		t.Log("verified new equipment list and indexhash registration")
	}
	t.Logf("verified edit/add/delete, rebuilt HASH and %d untouched payloads", len(untouched))
}
