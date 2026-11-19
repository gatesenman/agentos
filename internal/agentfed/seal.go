package agentfed

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TokenSealer encrypts federation auth tokens at rest (AES-256-GCM).
// Uses the same secret.key as env secrets when DataDir is shared, or an
// ephemeral key in tests.
type TokenSealer struct{ aead cipher.AEAD }

const tokPrefix = "fed1:"

// LoadSealer loads or creates <dir>/secret.key (32 bytes). dir "" => ephemeral.
func LoadSealer(dir string) (*TokenSealer, error) {
	var key []byte
	if dir == "" {
		key = make([]byte, 32)
		_, _ = rand.Read(key)
	} else {
		p := filepath.Join(dir, "secret.key")
		b, err := os.ReadFile(p)
		switch {
		case err == nil && len(b) == 32:
			key = b
		case err == nil:
			return nil, fmt.Errorf("%s: invalid key length %d", p, len(b))
		case errors.Is(err, os.ErrNotExist):
			key = make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				return nil, err
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(p, key, 0o600); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &TokenSealer{aead: aead}, nil
}

func (s *TokenSealer) Seal(token, agentID string) (string, error) {
	if token == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := s.aead.Seal(nonce, nonce, []byte(token), []byte("fed:"+agentID))
	return tokPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

func (s *TokenSealer) Open(sealed, agentID string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if !strings.HasPrefix(sealed, tokPrefix) {
		return "", errors.New("fed token: unknown format")
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, tokPrefix))
	if err != nil {
		return "", err
	}
	ns := s.aead.NonceSize()
	if len(b) < ns {
		return "", errors.New("fed token: truncated")
	}
	pt, err := s.aead.Open(nil, b[:ns], b[ns:], []byte("fed:"+agentID))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
