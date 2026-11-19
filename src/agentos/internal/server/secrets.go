package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sealer encrypts environment secrets with AES-256-GCM. The key lives in
// <data>/server/secret.key (32 random bytes, mode 0600), created on first use.
type sealer struct{ aead cipher.AEAD }

const sealPrefix = "v1:"

func loadSealer(dir string) (*sealer, error) {
	var key []byte
	if dir == "" { // tests / no data dir: ephemeral key
		key = make([]byte, 32)
		rand.Read(key)
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
	return &sealer{aead: aead}, nil
}

func (s *sealer) seal(plain []byte, aad string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := s.aead.Seal(nonce, nonce, plain, []byte(aad))
	return sealPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

func (s *sealer) open(sealed, aad string) ([]byte, error) {
	if !strings.HasPrefix(sealed, sealPrefix) {
		return nil, errors.New("secrets: unknown format")
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, sealPrefix))
	if err != nil {
		return nil, err
	}
	ns := s.aead.NonceSize()
	if len(b) < ns {
		return nil, errors.New("secrets: truncated")
	}
	return s.aead.Open(nil, b[:ns], b[ns:], []byte(aad))
}

// secretKeys returns the top-level config properties marked "x-secret": true
// in a provider's JSON schema.
func secretKeys(schema json.RawMessage) map[string]bool {
	var sc struct {
		Properties map[string]struct {
			Secret bool `json:"x-secret"`
		} `json:"properties"`
	}
	json.Unmarshal(schema, &sc)
	out := map[string]bool{}
	for k, p := range sc.Properties {
		if p.Secret {
			out[k] = true
		}
	}
	return out
}

func configMap(raw json.RawMessage) (map[string]json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if len(raw) == 0 || string(raw) == "null" {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("config must be a JSON object: %w", err)
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return m, nil
}

// splitConfig separates secret fields from the public config.
func splitConfig(cfg json.RawMessage, keys map[string]bool) (public json.RawMessage, secrets map[string]json.RawMessage, err error) {
	m, err := configMap(cfg)
	if err != nil {
		return nil, nil, err
	}
	secrets = map[string]json.RawMessage{}
	for k := range keys {
		if v, ok := m[k]; ok {
			if string(v) != "null" && string(v) != `""` {
				secrets[k] = v
			}
			delete(m, k)
		}
	}
	public, _ = json.Marshal(m)
	return public, secrets, nil
}

func mergeConfig(public json.RawMessage, secrets map[string]json.RawMessage) json.RawMessage {
	m, _ := configMap(public)
	for k, v := range secrets {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

func (s *sealer) sealSecrets(secrets map[string]json.RawMessage, envID string) (string, error) {
	if len(secrets) == 0 {
		return "", nil
	}
	b, _ := json.Marshal(secrets)
	return s.seal(b, envID)
}

func (s *sealer) openSecrets(sealed, envID string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if sealed == "" {
		return out, nil
	}
	b, err := s.open(sealed, envID)
	if err != nil {
		return nil, err
	}
	return out, json.Unmarshal(b, &out)
}

func sortedKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
