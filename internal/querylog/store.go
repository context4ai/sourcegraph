package querylog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/mongo/options"
	"os"
	"sort"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// One bounded document makes append + global retention atomic across instances.
// Bounded fields keep each entry below 12 KiB, including query recovery hints;
// 1000 entries remain below Mongo's 16 MiB document limit.
type MongoStore struct{ Collection *mongo.Collection }

func (s MongoStore) Append(ctx context.Context, entries []Entry) error {
	for attempt := 0; attempt < 5; attempt++ {
		var doc struct {
			ID            string    `bson:"_id"`
			Revision      int64     `bson:"revision"`
			ClearedBefore time.Time `bson:"cleared_before"`
			Entries       []Entry   `bson:"entries"`
		}
		err := s.Collection.FindOne(ctx, bson.M{"_id": "recent"}).Decode(&doc)
		if errors.Is(err, mongo.ErrNoDocuments) {
			_, err = s.Collection.InsertOne(ctx, bson.M{"_id": "recent", "revision": int64(1), "entries": merge(nil, entries)})
			if mongo.IsDuplicateKeyError(err) {
				continue
			}
			return err
		}
		if err != nil {
			return err
		}
		result, err := s.Collection.ReplaceOne(ctx, bson.M{"_id": "recent", "revision": doc.Revision}, bson.M{"_id": "recent", "revision": doc.Revision + 1, "entries": merge(doc.Entries, afterClear(entries, doc.ClearedBefore)), "cleared_before": doc.ClearedBefore})
		if err != nil {
			return err
		}
		if result.MatchedCount == 1 {
			return nil
		}
	}
	return errors.New("query log concurrent update")
}

// IDs make retries safe even when the previous write acknowledgement was lost.
func merge(rows, entries []Entry) []Entry {
	seen := map[string]bool{}
	out := make([]Entry, 0, len(rows)+len(entries))
	for _, e := range append(rows, entries...) {
		if !seen[e.ID] {
			out = append(out, e)
			seen[e.ID] = true
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if len(out) > Limit {
		out = out[:Limit]
	}
	return out
}
func (s MongoStore) Read(ctx context.Context) ([]Entry, error) {
	var doc struct {
		Entries []Entry `bson:"entries"`
	}
	err := s.Collection.FindOne(ctx, bson.M{"_id": "recent"}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return []Entry{}, nil
	}
	return doc.Entries, err
}

// Local development uses the same retention contract, persisted under data_root.
type FileStore struct {
	Path string
	mu   sync.Mutex
}

type fileState struct {
	Entries       []Entry   `json:"entries"`
	ClearedBefore time.Time `json:"cleared_before"`
}

func (s *FileStore) state() (fileState, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return fileState{}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	var out fileState
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		err = json.Unmarshal(raw, &out.Entries)
	} else {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func (s *FileStore) Read(ctx context.Context) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, err := s.state()
	return state.Entries, err
}
func (s *FileStore) Append(ctx context.Context, entries []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := s.state()
	if err != nil {
		return err
	}
	state.Entries = merge(state.Entries, afterClear(entries, state.ClearedBefore))
	return s.write(state)
}
func (s *FileStore) write(state fileState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err = os.WriteFile(s.Path+".tmp", raw, 0600); err != nil {
		return err
	}
	return os.Rename(s.Path+".tmp", s.Path)
}

// Persist the cutoff with the emptied log, so delayed batches (including other
// instances and retries) cannot resurrect entries created before the clear.
func afterClear(entries []Entry, cutoff time.Time) []Entry {
	if cutoff.IsZero() {
		return entries
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Time.After(cutoff) {
			out = append(out, e)
		}
	}
	return out
}
func clearCutoff() time.Time {
	return time.Now().UTC().Truncate(time.Millisecond).Add(time.Millisecond)
}
func (s MongoStore) Clear(ctx context.Context) error {
	cutoff := clearCutoff()
	for attempt := 0; attempt < 5; attempt++ {
		_, err := s.Collection.UpdateOne(ctx, bson.M{"_id": "recent"}, bson.M{"$set": bson.M{"entries": []Entry{}}, "$inc": bson.M{"revision": int64(1)}, "$max": bson.M{"cleared_before": cutoff}}, options.Update().SetUpsert(true))
		if mongo.IsDuplicateKeyError(err) {
			continue
		}
		return err
	}
	return errors.New("query log concurrent clear")
}
func (s *FileStore) Clear(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := s.state()
	if err != nil {
		return err
	}
	if cutoff := clearCutoff(); cutoff.After(state.ClearedBefore) {
		state.ClearedBefore = cutoff
	}
	state.Entries = []Entry{}
	return s.write(state)
}
