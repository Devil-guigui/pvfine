package pvf

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// The paged container layout is shared by 110US, 115US and unknown clients:
//
//	PageSize    10 MiB pages, PageGuardSize 10,240 bytes, PageKeySize 32 bytes
//
// The first 10,240 bytes of every page are AES-256-CBC (IV = 0) encrypted with
// that page's 32-byte key; the rest of the page holds the ordinary 90US layout
// (48-byte header + file table + hash + name pool + GRPI + body), so once the
// guards are decrypted in place the archive parses exactly like the older
// formats.
//
// The per-page key table is not stored in the archive: it ships next to it as
// "sk.dat", RSA-1024 PKCS#1 v1.5 sealed (the private key lives in DFO.exe),
// and is additionally AES-256-CBC wrapped with a "metadata key" that is a
// 64-hex-character run found inside the executable. The embedded 110US RSA
// key is tried first; newer clients can supply PKCS#8 or PKCS#1 PEM literals.
//
// Every section uses the wide-string key derivation of the newer client with
// scheme-specific names:
//
//	Header iNfO, GRPI Gidx, sTrA stAs, sTrW stWs, body mAIn
const (
	paged110PageSize      = 10485760
	paged110PageGuardSize = 10240
	paged110PageKeySize   = 32
	paged110MetadataAlign = 256

	paged110KeyHeader = "iNfO"
	paged110KeyGrpi   = "Gidx"
	paged110KeyBody   = "mAIn"
	paged110KeyStrA   = "stAs"
	paged110KeyStrW   = "stWs"

	// Name-pool size obfuscation constants of the Paged110 scheme.
	paged110MaskStrA uint32 = 3886938090
	paged110MaskStrW uint32 = 3101599660

	// sealedPageKeyName / executableName are the sidecar files the client
	// reads: the sealed page key table and the executable holding the key.
	// The page-key table also has a built-in default for the bundled 110US
	// client data.
	sealedPageKeyName = "sk.dat"
	executableName    = "DFO.exe"
	cnExecutableName  = "DNF.exe"
)

// ErrPagedKeys indicates a paged archive whose page-key material is
// missing or does not unlock it.
var ErrPagedKeys = errors.New("pvf: 内置与同目录配套数据均无法解封归档；请将该 PVF 对应版本的 sk.dat 和 DNF.exe（或 DFO.exe）与 PVF 放在同一目录后重试")

// ErrPagedReadOnly reports that a paged container cannot be written back
// because its page keys are not in memory, so the decrypted page guards could
// not be re-encrypted.
var ErrPagedReadOnly = errors.New("pvf: paged archives cannot be written without their page keys")

// ErrPagedStructureLocked reports that a paged archive cannot take
// structural edits (adding or removing files) yet: its HASH section is carried
// over verbatim because that section's key is unknown, while a structural edit
// rebuilds the name pool and would leave the carried-over HASH pointing at stale
// name offsets. In-place edits of existing payloads are supported.
var ErrPagedStructureLocked = errors.New("pvf: paged archives only support in-place edits until the HASH key is known")

// Deprecated: use the client-neutral paged errors.
var (
	ErrPaged110Keys            = ErrPagedKeys
	ErrPaged110ReadOnly        = ErrPagedReadOnly
	ErrPaged110StructureLocked = ErrPagedStructureLocked
)

// wideSeed is the newer client's seed derivation: four 16-bit key units
// (UTF-16 code units), Horner over base 0x393 with a 0x339E9711 multiplier.
func wideSeed(name string) uint32 {
	var u [4]uint16
	if len(name) < 4 {
		return 0
	}
	for i := 0; i < 4; i++ {
		u[i] = uint16(name[i])
	}
	return 0x339E9711*uint32(u[0]) +
		0x393*(uint32(u[3])+0x393*(uint32(u[2])+0x393*uint32(u[1])))
}

// paged110Keys is the Paged110 section key set.
func paged110Keys() keySet {
	return keySet{
		header: sectionKey{wideSeed(paged110KeyHeader), magicMain},
		grpi:   sectionKey{wideSeed(paged110KeyGrpi), magicMain},
		body:   sectionKey{wideSeed(paged110KeyBody), magicMain},
		strA:   sectionKey{wideSeed(paged110KeyStrA), magicAlt},
		strW:   sectionKey{wideSeed(paged110KeyStrW), magicAlt},
		maskA:  paged110MaskStrA,
		maskW:  paged110MaskStrW,
	}
}

func unsealPageKeyTableWithKey(sealed []byte, key *rsa.PrivateKey) ([]byte, error) {
	blockSize := key.Size()
	if len(sealed) == 0 || len(sealed)%blockSize != 0 {
		return nil, ErrPagedKeys
	}
	out := make([]byte, 0, len(sealed))
	for off := 0; off < len(sealed); off += blockSize {
		block, err := rsa.DecryptPKCS1v15(nil, key, sealed[off:off+blockSize])
		if err != nil {
			return nil, ErrPagedKeys
		}
		out = append(out, block...)
	}
	return out, nil
}

// Client revisions can move or replace their PEM literals. Only RSA candidates
// that unseal the complete sidecar are passed on to the header validator.
func executablePageKeyTables(executable, sealed []byte) []pageKeyTable {
	var tables []pageKeyTable
	for len(executable) > 0 {
		start := bytes.Index(executable, []byte("-----BEGIN "))
		if start < 0 {
			break
		}
		executable = executable[start:]
		block, rest := pem.Decode(executable)
		if block == nil {
			executable = executable[len("-----BEGIN "):]
			continue
		}
		executable = rest
		var key *rsa.PrivateKey
		switch block.Type {
		case "PRIVATE KEY":
			parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err == nil {
				key, _ = parsed.(*rsa.PrivateKey)
			}
		case "RSA PRIVATE KEY":
			key, _ = x509.ParsePKCS1PrivateKey(block.Bytes)
		}
		if key == nil {
			continue
		}
		if table, err := unsealPageKeyTableWithKey(sealed, key); err == nil {
			tables = append(tables, pageKeyTable{data: table, profile: pagedClientProfile(&key.PublicKey)})
		}
	}
	return tables
}

// unwrapPageKeyTable AES-256-CBC (IV = 0) decrypts the aligned prefix of the
// table in place; the trailing partial block is left untouched.
func unwrapPageKeyTable(table, metadataKey []byte) bool {
	n := len(table) &^ (paged110MetadataAlign - 1)
	if n == 0 || len(metadataKey) != 32 {
		return false
	}
	block, err := aes.NewCipher(metadataKey)
	if err != nil {
		return false
	}
	cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(table[:n], table[:n])
	return true
}

// decryptPageGuards AES-256-CBC (IV = 0) decrypts the first 10,240 bytes of
// every 10 MiB page in place, using 32 bytes of page key table per page.
func decryptPageGuards(data, pageKeys []byte) int {
	pages := len(pageKeys) / paged110PageKeySize
	done := 0
	for i := 0; i < pages; i++ {
		off := i * paged110PageSize
		if off+paged110PageGuardSize > len(data) {
			break
		}
		block, err := aes.NewCipher(pageKeys[i*paged110PageKeySize : (i+1)*paged110PageKeySize])
		if err != nil {
			break
		}
		cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).
			CryptBlocks(data[off:off+paged110PageGuardSize], data[off:off+paged110PageGuardSize])
		done++
	}
	return done
}

// encryptPageGuards re-applies the page protection in place: the first 10,240
// bytes of every 10 MiB page are AES-256-CBC (IV = 0) encrypted with that page's
// key. It is the exact inverse of decryptPageGuards.
func encryptPageGuards(data, pageKeys []byte) int {
	pages := len(pageKeys) / paged110PageKeySize
	done := 0
	for i := 0; i < pages; i++ {
		off := i * paged110PageSize
		if off+paged110PageGuardSize > len(data) {
			break
		}
		block, err := aes.NewCipher(pageKeys[i*paged110PageKeySize : (i+1)*paged110PageKeySize])
		if err != nil {
			break
		}
		cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).
			CryptBlocks(data[off:off+paged110PageGuardSize], data[off:off+paged110PageGuardSize])
		done++
	}
	return done
}

// writePageGuarded writes logical (an archive whose page guards are decrypted)
// to w with the page protection re-applied, streaming page by page so a save
// never needs a second copy of the whole container in memory.
func writePageGuarded(w io.Writer, logical, pageKeys []byte) error {
	pages := len(pageKeys) / paged110PageKeySize
	guard := make([]byte, paged110PageGuardSize)
	for i := 0; ; i++ {
		off := i * paged110PageSize
		if off >= len(logical) {
			return nil
		}
		end := off + paged110PageSize
		if end > len(logical) {
			end = len(logical)
		}
		head := off + paged110PageGuardSize
		if i >= pages || head > len(logical) {
			// Beyond the key table (or a trailing partial page) nothing is
			// guarded; mirror decryptPageGuards, which stops there too.
			if _, err := w.Write(logical[off:end]); err != nil {
				return err
			}
			continue
		}
		copy(guard, logical[off:head])
		block, err := aes.NewCipher(pageKeys[i*paged110PageKeySize : (i+1)*paged110PageKeySize])
		if err != nil {
			return err
		}
		cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(guard, guard)
		if _, err := w.Write(guard); err != nil {
			return err
		}
		if _, err := w.Write(logical[head:end]); err != nil {
			return err
		}
	}
}

// metadataKeyCandidates returns the AES keys that may wrap the page key table.
//
// The client takes every run of 64 hex characters found in the executable and
// decodes it to 32 bytes. Matching candidates are validated against the archive.
func metadataKeyCandidates(executable []byte) [][]byte {
	var out [][]byte
	seen := map[string]bool{}
	add := func(b []byte) {
		if len(b) != 32 || seen[string(b)] {
			return
		}
		seen[string(b)] = true
		out = append(out, b)
	}
	for i := 0; i+64 <= len(executable); {
		j := i
		for j < len(executable) && isHexDigit(executable[j]) {
			j++
		}
		if j-i == 64 {
			if b, err := hex.DecodeString(string(executable[i:j])); err == nil {
				add(b)
			}
		}
		if j == i {
			i++
		} else {
			i = j
		}
	}
	return out
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// unlockPaged tries complete bundled clients first, then archive-local sidecars.
// Client identity is accepted only after the decrypted header and layout validate.
// data is never modified; the bundled fast path does not read sibling files.
func unlockPaged(data []byte, dir string) (detectedArchive, bool) {
	if len(data) < paged110PageGuardSize {
		return detectedArchive{}, false
	}
	keys := paged110Keys()
	clients := bundledPagedClients()
	for _, client := range clients {
		if result, ok := openPagedTable(data, client.table, keys, client.metadata, client.profile); ok {
			return result, true
		}
	}
	sealedCandidates := loadSealedPageKeyCandidates(dir)
	var tables []pageKeyTable
	for _, client := range clients {
		for _, sealed := range sealedCandidates {
			table, err := unsealPageKeyTableWithKey(sealed, client.key)
			if err != nil {
				continue
			}
			if result, ok := openPagedTable(data, table, keys, client.metadata, client.profile); ok {
				return result, true
			}
			tables = append(tables, pageKeyTable{data: table, profile: client.profile})
		}
	}
	if dir == "" {
		return detectedArchive{}, false
	}
	for _, name := range [...]string{executableName, cnExecutableName} {
		exe, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		clientTables := append([]pageKeyTable(nil), tables...)
		for _, sealed := range sealedCandidates {
			clientTables = append(clientTables, executablePageKeyTables(exe, sealed)...)
		}
		clientCandidates := pagedMetadataCandidates(exe)
		for _, table := range clientTables {
			for _, cand := range clientCandidates {
				if result, ok := openPagedTable(data, table.data, keys, cand, table.profile); ok {
					return result, true
				}
			}
		}
	}
	return detectedArchive{}, false
}

func openPagedTable(data, table []byte, keys keySet, metadata []byte, profile formatProfile) (detectedArchive, bool) {
	dec, pkeys, hdr, ok := decryptPagedWith(data, table, keys, metadata)
	if !ok {
		return detectedArchive{}, false
	}
	return detectedArchive{data: dec, hdr: hdr, keys: keys, format: profile, pageKeys: pkeys}, true
}

// decryptPagedWith unwraps the page key table with cand and, if the header
// then validates, returns the archive with its page guards decrypted together
// with the unwrapped page key table needed to write it back.
func decryptPagedWith(data, table []byte, keys keySet, cand []byte) ([]byte, []byte, Header, bool) {
	pages := (len(data) + paged110PageSize - 1) / paged110PageSize
	if len(data) < paged110PageGuardSize || len(table)%paged110PageKeySize != 0 ||
		len(table)/paged110PageKeySize < pages {
		return nil, nil, Header{}, false
	}
	unwrapped := make([]byte, len(table))
	copy(unwrapped, table)
	if !unwrapPageKeyTable(unwrapped, cand) {
		return nil, nil, Header{}, false
	}
	block, err := aes.NewCipher(unwrapped[:paged110PageKeySize])
	if err != nil {
		return nil, nil, Header{}, false
	}
	var hdrRaw [headerSize]byte
	cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(hdrRaw[:], data[:headerSize])
	cryptSeed(keys.header.seed, magicMain, hdrRaw[:])
	hdr := decodeHeader(hdrRaw)
	if hdr.Signature != MagicSignature {
		return nil, nil, Header{}, false
	}
	if hdr.FileCount <= 0 || hdr.BodySize <= 0 || hdr.GroupCount <= 0 ||
		hdr.HashTableSize <= 0 || hdr.NameTableSize <= 0 {
		return nil, nil, Header{}, false
	}
	declared := int64(headerSize) + int64(hdr.FileCount)*0x18 + int64(hdr.HashTableSize) +
		int64(hdr.NameTableSize) + int64(hdr.GroupCount)*8 + int64(hdr.BodySize)
	if declared != int64(len(data)) {
		return nil, nil, Header{}, false
	}
	buf := make([]byte, len(data))
	copy(buf, data)
	if decryptPageGuards(buf, unwrapped) != pages {
		return nil, nil, Header{}, false
	}
	return buf, unwrapped, hdr, true
}

// hasSealedKeyFile reports whether dir contains the external sidecar key
// table. The embedded fallback is intentionally not included here so generic
// bad archives keep their existing error classification.
func hasSealedKeyFile(dir string) bool {
	if dir == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, sealedPageKeyName))
	return err == nil && st.Size() > 0
}
