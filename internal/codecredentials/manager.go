package codecredentials

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/context4ai/sourcegraph/internal/codesources"
	"github.com/context4ai/sourcegraph/internal/contract"
)

const Global = "global"

type Record struct {
	ID         string     `bson:"_id"`
	Revision   int64      `bson:"revision"`
	Ciphertext []byte     `bson:"ciphertext"`
	Expires    *time.Time `bson:"expires_at,omitempty"`
	Invalid    bool       `bson:"invalid"`
	Updated    time.Time  `bson:"updated_at"`
}
type Status struct {
	Revision   int64      `json:"revision"`
	Configured bool       `json:"configured"`
	Expires    *time.Time `json:"expires_at"`
	State      string     `json:"state"`
	Source     string     `json:"source"`
	// Retired marks an alias kept only for already registered repositories.
	Retired bool `json:"retired,omitempty"`
}
type Input struct {
	Revision int64      `json:"revision"`
	Token    *string    `json:"token,omitempty"`
	Expires  *time.Time `json:"expires_at"`
	Clear    bool       `json:"clear"`
}
type Candidate struct {
	Token       string
	owner, kind string
	revision    int64
}
type Manager struct {
	store         Store
	aead          cipher.AEAD
	mu            sync.Mutex
	legacyInvalid map[string]bool
}

// New reads deployment environment tokens through each code source's token_env.
func New(root string, store Store) (*Manager, error) {
	var key []byte
	var err error
	if raw := os.Getenv("SOURCEGRAPH_CREDENTIAL_KEY"); raw != "" {
		key, err = base64.StdEncoding.DecodeString(raw)
	} else {
		path := filepath.Join(root, "code-credentials.key")
		info, e := os.Lstat(path)
		if e == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
			return nil, errors.New("unsafe code credential key permissions")
		}
		key, err = os.ReadFile(path)
		if os.IsNotExist(err) {
			key = make([]byte, 32)
			if _, err = rand.Read(key); err != nil {
				return nil, errors.New("cannot generate code credential key")
			}
			var f *os.File
			f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err == nil {
				_, err = f.Write(key)
				if err == nil {
					err = f.Sync()
				}
				f.Close()
			}
		}
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("code credential key unavailable; expected 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Manager{store: store, aead: aead, legacyInvalid: map[string]bool{}}, nil
}
func recordID(owner, kind string) string {
	sum := sha256.Sum256([]byte(owner + "\x00" + kind))
	return hex.EncodeToString(sum[:])
}

// defaultProvider is the provider of repositories registered before providers were recorded.
const defaultProvider = "github"

func validKind(kind string) bool {
	for _, k := range codesources.Kinds() {
		if k == kind {
			return true
		}
	}
	return false
}
func (m *Manager) load(ctx context.Context, owner, kind string) (Record, string, error) {
	if !validKind(kind) {
		return Record{}, "", contract.Fail("INVALID_CREDENTIAL_KIND", "Unknown code source.", 400)
	}
	id := recordID(owner, kind)
	r, err := m.store.Get(ctx, id)
	if errors.Is(err, missing) {
		r = Record{ID: id}
		if owner == Global {
			m.mu.Lock()
			r.Invalid = m.legacyInvalid[kind]
			m.mu.Unlock()
			return r, codesources.EnvToken(kind), nil
		}
		return r, "", nil
	}
	if err != nil {
		return r, "", err
	}
	if len(r.Ciphertext) == 0 {
		return r, "", nil
	}
	n := m.aead.NonceSize()
	if len(r.Ciphertext) < n {
		return r, "", unavailable
	}
	plain, err := m.aead.Open(nil, r.Ciphertext[:n], r.Ciphertext[n:], []byte(id))
	if err != nil {
		return r, "", unavailable
	}
	return r, string(plain), nil
}
func status(r Record, token string, now time.Time) Status {
	st := Status{Revision: r.Revision, Configured: token != "", Expires: r.Expires, State: "empty", Source: "saved"}
	if r.Revision == 0 && token != "" {
		st.Source = "environment"
	}
	if token != "" {
		st.State = "ready"
		if r.Invalid {
			st.State = "invalid"
		} else if r.Expires != nil {
			if !now.Before(*r.Expires) {
				st.State = "expired"
			} else if r.Expires.Sub(now) <= 7*24*time.Hour {
				st.State = "expiring"
			}
		}
	}
	return st
}

// Status covers every visible source; a retired alias appears only while it still holds a token.
func (m *Manager) Status(ctx context.Context, owner string) (map[string]Status, error) {
	out := map[string]Status{}
	for _, kind := range codesources.Kinds() {
		r, t, err := m.load(ctx, owner, kind)
		if err != nil {
			return nil, err
		}
		st := status(r, t, time.Now())
		if codesources.IsAlias(kind) {
			if !st.Configured {
				continue
			}
			st.Retired = true
		}
		out[kind] = st
	}
	return out, nil
}
func (m *Manager) Save(ctx context.Context, owner, kind string, in Input) (Status, error) {
	r, token, err := m.load(ctx, owner, kind)
	if err != nil {
		return Status{}, err
	}
	if r.Revision != in.Revision {
		return Status{}, conflict
	}
	if in.Clear {
		token = ""
		in.Expires = nil
	} else if in.Token != nil {
		token = strings.TrimSpace(*in.Token)
		if len(token) < 8 || len(token) > 4096 || strings.IndexFunc(token, unicode.IsSpace) >= 0 || strings.IndexFunc(token, unicode.IsControl) >= 0 {
			return Status{}, contract.Fail("INVALID_CREDENTIAL", "Provide a non-empty access token without whitespace.", 400)
		}
	}
	if token == "" && in.Expires != nil {
		return Status{}, contract.Fail("INVALID_CREDENTIAL", "Set a token before an expiry.", 400)
	}
	r.Expires = in.Expires
	r.Updated = time.Now().UTC()
	r.Revision++
	r.Ciphertext = nil
	if in.Clear || in.Token != nil {
		r.Invalid = false
	}
	if token != "" {
		nonce := make([]byte, m.aead.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			return Status{}, unavailable
		}
		r.Ciphertext = m.aead.Seal(nonce, nonce, []byte(token), []byte(r.ID))
	}
	if err = m.store.Save(ctx, r, in.Revision); err != nil {
		return Status{}, err
	}
	return status(r, token, time.Now()), nil
}

// Move re-encrypts a retired alias token under a visible source that has no token,
// then clears the alias, so the plaintext never leaves the service.
func (m *Manager) Move(ctx context.Context, owner, from, to string, revision int64) (Status, error) {
	if !codesources.IsAlias(from) {
		return Status{}, contract.Fail("INVALID_CREDENTIAL_MOVE", "Only a retired code source's token can be moved.", 400)
	}
	if _, ok := codesources.Lookup(to); !ok {
		return Status{}, contract.Fail("INVALID_CREDENTIAL_KIND", "Unknown code source.", 400)
	}
	src, token, err := m.load(ctx, owner, from)
	if err != nil {
		return Status{}, err
	}
	if src.Revision != revision {
		return Status{}, conflict
	}
	if token == "" {
		return Status{}, contract.Fail("INVALID_CREDENTIAL_MOVE", "The retired code source has no token to move.", 400)
	}
	dst, existing, err := m.load(ctx, owner, to)
	if err != nil {
		return Status{}, err
	}
	if existing != "" {
		return Status{}, contract.Fail("CREDENTIAL_TARGET_CONFIGURED", "The target code source already has a token; clear it first to replace it.", 409)
	}
	moved, err := m.Save(ctx, owner, to, Input{Revision: dst.Revision, Token: &token, Expires: src.Expires})
	if err != nil {
		return Status{}, err
	}
	if _, err = m.Save(ctx, owner, from, Input{Revision: src.Revision, Clear: true}); err != nil {
		return Status{}, err
	}
	return moved, nil
}

// Credential providers are isolated; repository owners take precedence.
func (m *Manager) Candidates(ctx context.Context, owner string, provider ...string) ([]Candidate, error) {
	kind := defaultProvider
	if len(provider) > 0 && provider[0] != "" {
		kind = provider[0]
	}
	if !validKind(kind) {
		return nil, contract.Fail("INVALID_CODE_PROVIDER", "Unknown code source.", 400)
	}
	owners := []string{}
	if owner != "" && owner != Global {
		owners = append(owners, owner)
	}
	owners = append(owners, Global)
	out := []Candidate{}
	seen := map[string]bool{}
	for _, who := range owners {
		for _, kind := range []string{kind} {
			r, t, err := m.load(ctx, who, kind)
			if err != nil {
				return nil, err
			}
			if t == "" || (r.Expires != nil && !time.Now().Before(*r.Expires)) || seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, Candidate{t, who, kind, r.Revision})
		}
	}
	// GitHub rejects a revoked token even for public repositories, so anonymous access is tried last.
	if codesources.AllowsAnonymous(kind) {
		out = append(out, Candidate{kind: kind})
	}
	if len(out) == 0 {
		return nil, contract.Fail("GIT_CREDENTIAL_UNAVAILABLE", "Configure an unexpired personal or global code-service credential.", 503)
	}
	return out, nil
}
func (m *Manager) Report(ctx context.Context, c Candidate, invalid bool) {
	if c.Token == "" {
		return
	}
	r, t, err := m.load(ctx, c.owner, c.kind)
	if err != nil || r.Revision != c.revision || t != c.Token || r.Invalid == invalid {
		return
	}
	if r.Revision == 0 {
		m.mu.Lock()
		m.legacyInvalid[c.kind] = invalid
		m.mu.Unlock()
		return
	}
	old := r.Revision
	r.Revision++
	r.Invalid = invalid
	r.Updated = time.Now().UTC()
	_ = m.store.Save(ctx, r, old)
}

type Warning struct {
	Kind    string     `json:"kind"`
	State   string     `json:"state"`
	Expires *time.Time `json:"expires_at,omitempty"`
}

func (m *Manager) Warnings(ctx context.Context) []Warning {
	out := []Warning{}
	values, err := m.Status(ctx, Global)
	if err != nil {
		return []Warning{{Kind: defaultProvider, State: "unavailable"}}
	}
	for _, kind := range codesources.Kinds() {
		v, ok := values[kind]
		if !ok {
			continue
		}
		if v.State == "expired" || v.State == "invalid" || v.State == "expiring" {
			out = append(out, Warning{kind, v.State, v.Expires})
		}
	}
	return out
}

func (m *Manager) Reset(ctx context.Context) error { return m.store.Clear(ctx) }
