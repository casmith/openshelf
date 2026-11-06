// Package secrets encrypts small values (provider credentials) at rest with
// AES-256-GCM under keys derived from OPENSHELF_SECRET_KEY.
//
// Ciphertext layout: version (1 byte) | key ID (4 bytes) | nonce (12) | sealed data.
// The header is authenticated as additional data.
package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

const (
	version   byte = 1
	keyIDLen       = 4
	nonceLen       = 12
	headerLen      = 1 + keyIDLen
	// DevKey is used when no secret key is configured outside production.
	DevKey = "openshelf-insecure-development-secret-key"
)

var (
	ErrMalformed  = errors.New("secrets: malformed ciphertext")
	ErrUnknownKey = errors.New("secrets: ciphertext was encrypted with an unknown key")
	ErrDecrypt    = errors.New("secrets: decryption failed")
)

type key struct {
	id   [keyIDLen]byte
	aead cipher.AEAD
}

// Box encrypts with the current key and decrypts with the current or previous key.
type Box struct {
	current  key
	previous *key
}

// New builds a Box. previous may be empty; it's only used for decryption during rotation.
func New(current, previous string) (*Box, error) {
	if current == "" {
		return nil, errors.New("secrets: empty key")
	}
	c, err := deriveKey(current)
	if err != nil {
		return nil, err
	}
	b := &Box{current: c}
	if previous != "" && previous != current {
		p, err := deriveKey(previous)
		if err != nil {
			return nil, err
		}
		b.previous = &p
	}
	return b, nil
}

func deriveKey(secret string) (key, error) {
	enc, err := hkdf.Key(sha256.New, []byte(secret), nil, "openshelf secrets v1 encryption", 32)
	if err != nil {
		return key{}, err
	}
	idBytes, err := hkdf.Key(sha256.New, []byte(secret), nil, "openshelf secrets v1 key id", keyIDLen)
	if err != nil {
		return key{}, err
	}
	block, err := aes.NewCipher(enc)
	if err != nil {
		return key{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return key{}, err
	}
	k := key{aead: aead}
	copy(k.id[:], idBytes)
	return k, nil
}

func (b *Box) Encrypt(plaintext []byte) ([]byte, error) {
	out := make([]byte, headerLen+nonceLen, headerLen+nonceLen+len(plaintext)+b.current.aead.Overhead())
	out[0] = version
	copy(out[1:headerLen], b.current.id[:])
	if _, err := rand.Read(out[headerLen:]); err != nil {
		return nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	return b.current.aead.Seal(out, out[headerLen:], plaintext, out[:headerLen]), nil
}

func (b *Box) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < headerLen+nonceLen || ciphertext[0] != version {
		return nil, ErrMalformed
	}
	id := ciphertext[1:headerLen]
	var k *key
	switch {
	case bytes.Equal(id, b.current.id[:]):
		k = &b.current
	case b.previous != nil && bytes.Equal(id, b.previous.id[:]):
		k = b.previous
	default:
		return nil, ErrUnknownKey
	}
	nonce := ciphertext[headerLen : headerLen+nonceLen]
	pt, err := k.aead.Open(nil, nonce, ciphertext[headerLen+nonceLen:], ciphertext[:headerLen])
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// NeedsRotation reports whether ciphertext was sealed with a key other than the current one.
func (b *Box) NeedsRotation(ciphertext []byte) bool {
	return len(ciphertext) < headerLen || !bytes.Equal(ciphertext[1:headerLen], b.current.id[:])
}
