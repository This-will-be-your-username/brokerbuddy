package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// Access tokens grant read access to bank data, so they are encrypted at rest.
type vault struct{ gcm cipher.AEAD }

func newVault(secret string) (*vault, error) {
	k := sha256.Sum256([]byte(secret))
	b, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(b)
	return &vault{g}, err
}

func (v *vault) seal(s string) (string, error) {
	n := make([]byte, v.gcm.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(v.gcm.Seal(n, n, []byte(s), nil)), nil
}

func (v *vault) open(s string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) < v.gcm.NonceSize() {
		return "", errors.New("bad ciphertext")
	}
	n, ct := raw[:v.gcm.NonceSize()], raw[v.gcm.NonceSize():]
	p, err := v.gcm.Open(nil, n, ct, nil)
	return string(p), err
}
