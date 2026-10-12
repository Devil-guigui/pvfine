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
	"sync"
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
)

// ErrPagedKeys indicates a paged archive whose page-key material is
// missing or does not unlock it.
var ErrPagedKeys = errors.New("pvf: paged page key table could not be unlocked")

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

// paged110RSADER is the RSA-1024 private key embedded in the 110US client
// executable (PKCS#1 DER). It unwraps the sealed page key table in sk.dat.
const paged110RSADER = "3082025d02010002818100b3120bba28e9db7a87bf61ea60fdd28141b06759259899c0d22369b497cca064fbfd2747f4" +
	"a46734774cc04c7f8b4f95db428aba88ce37222bca7244918c0971697d2a3d457143b9629fc3d510c95824e3c0ffe87f" +
	"aea0b7f5034a617ed3948442ee7df6c07595b137f448cfbec3f024630c76dbd56fcd9a3f7017184dc8394f0203010001" +
	"028181009acdeef570893b04227680df6e19fff15e28722fcf20ad4ad45f68f286888fe0bd378ccdd7e0889802ca8733" +
	"9acf846db8af3ddf2485a18418f75af18c21d3c69409bc169c4ad6c7240e451ee29e96e83a4797d29c96503f2bad80df" +
	"fcbe962d736b8aaf3a1a0c4ec013ef1354d2dab317deb13a65a19415fe9a22f91f7cf701024100ea47490bb1f9402393" +
	"a621b0c1389eac7d2b152a54c4acf2209532d688ce76d1de36c0ee6770f9d6eda2487a89706fec895f768518df42c8fa" +
	"ca5bf971b33589024100c3ac5f46b88cd24538cabe7ea551db33a453c1b475c5ac895624df29ce560081d6e79b5dc7a9" +
	"45214d20b30400982f25c3c31530f79388e74e6820ae802a9a17024100b8b60182860ca5a4272a49cfc957f1cabf5933" +
	"73cfa7cd4f8d9ef4992efdd1b2c007dd6f5a013a0a5a0ba42770ab44a372dfe05b29f404fcdeb6a3737550bd3902406b" +
	"f0523e78df75bea9ad6d97ff2a40792454efadd4a9ce9b93e1931944b13c66635e2fde739d747d0246df797dba7587a7" +
	"8d9dcafd476d65eb629564ad5ed2d102405a8e7445a4d6d4c9b36163bde05d73bda013d3b63a0d5c502e0cefb5ec478d" +
	"3f2114a15e796b91f0502a728a6b23473b45a2c04814ee22613ed018f8fc600d7a"

var (
	paged110KeyOnce sync.Once
	paged110Key     *rsa.PrivateKey
	paged110KeyErr  error
)

func paged110PrivateKey() (*rsa.PrivateKey, error) {
	paged110KeyOnce.Do(func() {
		der, err := hex.DecodeString(paged110RSADER)
		if err != nil {
			paged110KeyErr = err
			return
		}
		paged110Key, paged110KeyErr = x509.ParsePKCS1PrivateKey(der)
	})
	return paged110Key, paged110KeyErr
}

// unsealPageKeyTable RSA-decrypts every 128-byte block of the sealed key file.
func unsealPageKeyTable(sealed []byte) ([]byte, error) {
	key, err := paged110PrivateKey()
	if err != nil {
		return nil, err
	}
	return unsealPageKeyTableWithKey(sealed, key)
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
// decodes it to 32 bytes. The embedded candidate below is the one shipped with
// the 110US client; executablePath is scanned when it is present.
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

// paged110EmbeddedMetadataHex is the metadata key shipped with the retail
// 110US client: the third 64-hex-character run inside DFO.exe.
const paged110EmbeddedMetadataHex = "21ad8ff286bd11687520212e5dfd064bd9a7ec798f7f78a706b0486e77634489"

func paged110EmbeddedMetadataKey() []byte {
	b, _ := hex.DecodeString(paged110EmbeddedMetadataHex)
	return b
}

// paged110Candidates returns the metadata key candidates to try: the embedded
// 110US constant first, then anything found in the sidecar executable.
func paged110Candidates(dir string) [][]byte {
	embedded := paged110EmbeddedMetadataKey()
	out := [][]byte{embedded}
	if dir == "" {
		return out
	}
	exe, err := os.ReadFile(filepath.Join(dir, executableName))
	if err != nil {
		return out
	}
	seen := map[string]bool{string(embedded): true}
	for _, c := range metadataKeyCandidates(exe) {
		if !seen[string(c)] {
			seen[string(c)] = true
			out = append(out, c)
		}
	}
	return out
}

// loadPaged110SealedPageKeys returns an archive-local sk.dat when available,
// falling back to the table bundled into the binary. The external file wins
// so archives from other 110US client versions can still provide their own
// page-key table.
func loadPaged110SealedPageKeys(dir string) ([]byte, error) {
	if dir != "" {
		sealed, err := os.ReadFile(filepath.Join(dir, sealedPageKeyName))
		if err == nil {
			return sealed, nil
		}
	}
	if len(paged110EmbeddedSealedPageKeys) == 0 {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), paged110EmbeddedSealedPageKeys...), nil
}

// unlockPaged unlocks the shared paged layout and retains the identity of the
// RSA candidate that succeeded. A profile is accepted only after the full
// sidecar and the archive header validate. data is never modified.
//
// The bundled page-key table is used when no archive-local sk.dat exists, and
// the bundled metadata key is tried first, so the common case never reads the
// multi-hundred-megabyte sidecar executable.
func unlockPaged(data []byte, dir string) (detectedArchive, bool) {
	if len(data) < paged110PageGuardSize {
		return detectedArchive{}, false
	}
	sealed, err := loadPaged110SealedPageKeys(dir)
	if err != nil {
		return detectedArchive{}, false
	}
	table, err := unsealPageKeyTable(sealed)
	keys := paged110Keys()
	var tables []pageKeyTable
	if err == nil {
		tables = append(tables, pageKeyTable{data: table, profile: paged110Profile})
		if dec, pkeys, hdr, ok := decryptPagedWith(data, table, keys, paged110EmbeddedMetadataKey()); ok {
			return detectedArchive{data: dec, hdr: hdr, keys: keys, format: paged110Profile, pageKeys: pkeys}, true
		}
	}
	if dir == "" {
		return detectedArchive{}, false
	}
	exe, err := os.ReadFile(filepath.Join(dir, executableName))
	if err != nil {
		return detectedArchive{}, false
	}
	tables = append(tables, executablePageKeyTables(exe, sealed)...)
	candidates := append([][]byte{paged110EmbeddedMetadataKey()}, metadataKeyCandidates(exe)...)
	for _, table := range tables {
		for _, cand := range candidates {
			if dec, pkeys, hdr, ok := decryptPagedWith(data, table.data, keys, cand); ok {
				return detectedArchive{data: dec, hdr: hdr, keys: keys, format: table.profile, pageKeys: pkeys}, true
			}
		}
	}
	return detectedArchive{}, false
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
