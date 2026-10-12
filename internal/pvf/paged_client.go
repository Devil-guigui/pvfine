package pvf

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
)

// SHA-256 of the RSA public key's SubjectPublicKeyInfo, verified against the
// 115US 2.38.2.34 client. This is an identity, not decryption key material.
const paged115PublicKeyFingerprint = "42631b7746e1b68ac21176a697522b8a569d29a8be1a640328dbf5dbd63d656e"

type pageKeyTable struct {
	data    []byte
	profile formatProfile
}

func pagedClientProfile(key *rsa.PublicKey) formatProfile {
	if embedded, err := paged110PrivateKey(); err == nil &&
		key.E == embedded.E && key.N.Cmp(embedded.N) == 0 {
		return paged110Profile
	}
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return pagedProfile
	}
	fingerprint := sha256.Sum256(der)
	if hex.EncodeToString(fingerprint[:]) == paged115PublicKeyFingerprint {
		return paged115Profile
	}
	return pagedProfile
}
