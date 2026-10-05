// Package codecredentials stores encrypted code-service credentials separately
// from public site settings and repository metadata.
package codecredentials

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/context4ai/sourcegraph/internal/contract"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

var missing = errors.New("credential not configured")
var unavailable = contract.Fail("CREDENTIAL_STORAGE_UNAVAILABLE", "Credential storage is unavailable.", 503)
var conflict = contract.Fail("REVISION_CONFLICT", "Credential changed; reload before saving.", 412)

type Store interface {
	Clear(context.Context) error
	Get(context.Context, string) (Record, error)
	Save(context.Context, Record, int64) error
}
type MongoStore struct{ Collection *mongo.Collection }

func (s MongoStore) Get(ctx context.Context, id string) (Record, error) {
	var r Record
	err := s.Collection.FindOne(ctx, bson.M{"_id": id}).Decode(&r)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return r, missing
	}
	if err != nil {
		return r, unavailable
	}
	return r, nil
}
func (s MongoStore) Save(ctx context.Context, r Record, expected int64) error {
	if expected == 0 {
		_, err := s.Collection.InsertOne(ctx, r)
		if mongo.IsDuplicateKeyError(err) {
			return conflict
		}
		if err != nil {
			return unavailable
		}
		return nil
	}
	res, err := s.Collection.ReplaceOne(ctx, bson.M{"_id": r.ID, "revision": expected}, r)
	if err != nil {
		return unavailable
	}
	if res.MatchedCount != 1 {
		return conflict
	}
	return nil
}

type FileStore struct {
	mu   sync.Mutex
	Path string
}

func (s *FileStore) load() (map[string]Record, error) {
	rows := map[string]Record{}
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return rows, nil
	}
	if err != nil || json.Unmarshal(data, &rows) != nil {
		return nil, unavailable
	}
	return rows, nil
}
func (s *FileStore) Get(_ context.Context, id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return Record{}, err
	}
	r, ok := rows[id]
	if !ok {
		return r, missing
	}
	return r, nil
}
func (s *FileStore) Save(_ context.Context, r Record, expected int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return err
	}
	if rows[r.ID].Revision != expected {
		return conflict
	}
	rows[r.ID] = r
	data, err := json.Marshal(rows)
	if err != nil {
		return unavailable
	}
	f, err := os.CreateTemp(filepath.Dir(s.Path), ".credentials-*")
	if err != nil {
		return unavailable
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	if err == nil {
		err = os.Rename(f.Name(), s.Path)
	}
	if err != nil {
		return unavailable
	}
	return nil
}

func (s MongoStore) Clear(ctx context.Context) error {
	_, err := s.Collection.DeleteMany(ctx, bson.M{})
	if err != nil {
		return unavailable
	}
	return nil
}
func (s *FileStore) Clear(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.Path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return unavailable
	}
	return nil
}
