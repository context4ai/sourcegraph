// Package secretstore persists application secrets and their AES-GCM key in
// the control database. It removes host-disk dependencies; it is not a defense
// against an attacker who can read the whole database.
package secretstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

var ErrMissing = errors.New("secret not configured")
var ErrExists = errors.New("secret already configured")
var unavailable = errors.New("persistent secret storage unavailable")

type record struct {
	ID   string `bson:"_id"`
	Data []byte `bson:"data"`
}
type backend interface {
	get(context.Context, string) ([]byte, error)
	insert(context.Context, string, []byte) error
}
type mongoBackend struct{ c *mongo.Collection }

func (m mongoBackend) get(ctx context.Context, id string) ([]byte, error) {
	var r record
	e := m.c.FindOne(ctx, bson.M{"_id": id}).Decode(&r)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return nil, ErrMissing
	}
	if e != nil {
		return nil, unavailable
	}
	return r.Data, nil
}
func (m mongoBackend) insert(ctx context.Context, id string, data []byte) error {
	_, e := m.c.InsertOne(ctx, record{id, data})
	if mongo.IsDuplicateKeyError(e) {
		return ErrExists
	}
	if e != nil {
		return unavailable
	}
	return nil
}

type Store struct {
	backend backend
	key     []byte
	aead    cipher.AEAD
}

type KeySeed func() ([]byte, error)

func New(ctx context.Context, db *mongo.Database, seed ...KeySeed) (*Store, error) {
	return open(ctx, mongoBackend{db.Collection("sourcegraph_secrets")}, seed...)
}
func open(ctx context.Context, b backend, seed ...KeySeed) (*Store, error) {
	key, e := b.get(ctx, "encryption-key-v1")
	if errors.Is(e, ErrMissing) {
		if len(seed) > 0 && seed[0] != nil {
			key, e = seed[0]()
			if e != nil {
				return nil, unavailable
			}
		}
		if len(key) == 0 {
			key = make([]byte, 32)
			if _, e = rand.Read(key); e != nil {
				return nil, unavailable
			}
		}
		if len(key) != 32 {
			return nil, unavailable
		}
		if e = b.insert(ctx, "encryption-key-v1", key); e != nil && !errors.Is(e, ErrExists) {
			return nil, e
		}
		key, e = b.get(ctx, "encryption-key-v1")
	}
	if e != nil {
		return nil, e
	}
	if len(key) != 32 {
		return nil, unavailable
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, unavailable
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, unavailable
	}
	return &Store{b, key, aead}, nil
}
func (s *Store) Key() []byte { return append([]byte(nil), s.key...) }
func (s *Store) Get(ctx context.Context, id string) ([]byte, error) {
	data, e := s.backend.get(ctx, "value:"+id)
	if e != nil {
		return nil, e
	}
	n := s.aead.NonceSize()
	if len(data) < n {
		return nil, unavailable
	}
	plain, e := s.aead.Open(nil, data[:n], data[n:], []byte(id))
	if e != nil {
		return nil, unavailable
	}
	return plain, nil
}

// Insert never replaces a winner from another process.
func (s *Store) Insert(ctx context.Context, id string, plain []byte) error {
	nonce := make([]byte, s.aead.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		return unavailable
	}
	return s.backend.insert(ctx, "value:"+id, s.aead.Seal(nonce, nonce, plain, []byte(id)))
}

// Ensure returns the already persisted value, importing the supplied value only
// for a missing record. An empty value creates a random service credential.
func (s *Store) Ensure(ctx context.Context, id string, seed []byte) ([]byte, error) {
	v, e := s.Get(ctx, id)
	if !errors.Is(e, ErrMissing) {
		return v, e
	}
	if len(seed) == 0 {
		seed = make([]byte, 48)
		if _, e = rand.Read(seed); e != nil {
			return nil, unavailable
		}
		seed = []byte(base64.RawURLEncoding.EncodeToString(seed))
	}
	if e = s.Insert(ctx, id, seed); e != nil && !errors.Is(e, ErrExists) {
		return nil, e
	}
	return s.Get(ctx, id)
}
