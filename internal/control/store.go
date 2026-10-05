package control

import (
	"context"
	"errors"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

// Repository, policy, audit and pending work share one Mongo document. A single
// CAS persists them together without assuming cross-document transactions.
type Store interface {
	Get(context.Context, string) (Repository, error)
	List(context.Context, string, int) ([]Repository, error)
	Create(context.Context, Repository) error
	Replace(context.Context, Repository, int64) error
	ReplaceLease(context.Context, Repository, int64) error
}
type Mongo struct {
	client     *mongo.Client
	collection *mongo.Collection
}

func Connect(ctx context.Context, uri string) (*Mongo, error) {
	if uri == "" {
		return nil, errors.New("MongoDB URI is required")
	}
	type result struct {
		m *Mongo
		e error
	}
	ch := make(chan result)
	go func() {
		o := options.Client().ApplyURI(uri).SetConnectTimeout(2 * time.Second).SetServerSelectionTimeout(3 * time.Second).SetSocketTimeout(5 * time.Second).SetMaxPoolSize(8).SetMinPoolSize(0).SetRetryWrites(false).SetReadPreference(readpref.Primary()).SetWriteConcern(writeconcern.New(writeconcern.WMajority(), writeconcern.WTimeout(5*time.Second)))
		c, e := mongo.Connect(ctx, o)
		if e == nil {
			e = c.Ping(ctx, readpref.Primary())
		}
		if e != nil {
			if c != nil {
				cc, cancel := context.WithTimeout(context.Background(), time.Second)
				c.Disconnect(cc)
				cancel()
			}
			select {
			case ch <- result{e: errors.New("MongoDB connection failed")}:
			case <-ctx.Done():
			}
			return
		}
		select {
		case ch <- result{m: &Mongo{c, c.Database("c4a").Collection("sourcegraph_repositories")}}:
		case <-ctx.Done():
			cc, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c.Disconnect(cc)
		}
	}()
	select {
	case r := <-ch:
		return r.m, r.e
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (m *Mongo) Close(ctx context.Context) error { return m.client.Disconnect(ctx) }
func (m *Mongo) Get(ctx context.Context, name string) (Repository, error) {
	var r Repository
	e := m.collection.FindOne(ctx, bson.M{"_id": ID(name)}).Decode(&r)
	if errors.Is(e, mongo.ErrNoDocuments) {
		return r, contract.Fail("REPOSITORY_NOT_FOUND", "Repository is not registered.", 404)
	}
	if e != nil {
		return r, contract.Fail("CONTROL_STATE_UNAVAILABLE", "Repository state is unavailable.", 503)
	}
	return r, nil
}
func (m *Mongo) List(ctx context.Context, after string, limit int) ([]Repository, error) {
	if limit < 1 || limit > 1000 {
		return nil, contract.Fail("INVALID_LIMIT", "Invalid catalog limit.", 400)
	}
	c, e := m.collection.Find(ctx, bson.M{"_id": bson.M{"$gt": after}}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if e != nil {
		return nil, contract.Fail("CONTROL_STATE_UNAVAILABLE", "Catalog is unavailable.", 503)
	}
	defer c.Close(ctx)
	out := []Repository{}
	if e = c.All(ctx, &out); e != nil {
		return nil, contract.Fail("CONTROL_STATE_UNAVAILABLE", "Catalog is unavailable.", 503)
	}
	return out, nil
}
func (m *Mongo) Create(ctx context.Context, r Repository) error {
	if r.ID != ID(r.Name) || r.Revision != 1 {
		return errors.New("invalid repository identity")
	}
	_, e := m.collection.InsertOne(ctx, r)
	if mongo.IsDuplicateKeyError(e) {
		return contract.Fail("REPOSITORY_EXISTS", "Repository is already registered.", 409)
	}
	if e != nil {
		return contract.Fail("CONTROL_STATE_UNAVAILABLE", "Repository registration failed; retry with the same key.", 503)
	}
	return nil
}
func (m *Mongo) Replace(ctx context.Context, r Repository, expected int64) error {
	if r.ID != ID(r.Name) || r.Revision != expected+1 {
		return errors.New("invalid repository revision")
	}
	return m.replace(ctx, r, expected, r.LeaseRevision)
}
func (m *Mongo) ReplaceLease(ctx context.Context, r Repository, expected int64) error {
	if r.Revision != expected || r.LeaseRevision < 1 {
		return errors.New("invalid lease revision")
	}
	return m.replace(ctx, r, expected, r.LeaseRevision-1)
}
func (m *Mongo) replace(ctx context.Context, r Repository, expected, lease int64) error {
	filter := bson.M{"_id": r.ID, "revision": expected, "lease_revision": lease}
	if lease == 0 {
		delete(filter, "lease_revision")
		filter["$or"] = []bson.M{{"lease_revision": 0}, {"lease_revision": bson.M{"$exists": false}}}
	}
	result, e := m.collection.ReplaceOne(ctx, filter, r)
	if e != nil {
		return contract.Fail("CONTROL_STATE_UNAVAILABLE", "Repository update result is unknown; reload its state.", 503)
	}
	if result.MatchedCount != 1 {
		return contract.Fail("REVISION_CONFLICT", "Repository changed; reload and retry.", 412)
	}
	return nil
}
func Mutate(ctx context.Context, s Store, name string, fn func(*Repository) error) (Repository, error) {
	for attempt := 0; attempt < 5; attempt++ {
		r, e := s.Get(ctx, name)
		if e != nil {
			return r, e
		}
		old := r.Revision
		if e = fn(&r); e != nil {
			return r, e
		}
		r.Revision++
		r.Updated = time.Now().UTC()
		e = s.Replace(ctx, r, old)
		if e == nil {
			return r, nil
		}
		var ce *contract.Error
		if !errors.As(e, &ce) || ce.Code != "REVISION_CONFLICT" {
			return r, e
		}
	}
	return Repository{}, contract.Fail("REVISION_CONFLICT", "Repository is busy; retry later.", 412)
}

// AuthDatabase shares the existing bounded primary connection; auth uses distinct collections.
func (m *Mongo) AuthDatabase() *mongo.Database { return m.client.Database("c4a") }

// RenewLease has its own CAS counter so heartbeats cannot invalidate a user
// preview. Both replacement paths check it to prevent stale lease overwrite.
func RenewLease(ctx context.Context, s Store, name string, job Job, now time.Time) (time.Time, error) {
	for attempt := 0; attempt < 5; attempt++ {
		r, err := s.Get(ctx, name)
		if err != nil {
			return time.Time{}, err
		}
		if err = r.Renew(job, "preparing", now); err != nil {
			return time.Time{}, err
		}
		r.LeaseRevision++
		err = s.ReplaceLease(ctx, r, r.Revision)
		if err == nil {
			return now.Add(LeaseDuration), nil
		}
		var ce *contract.Error
		if !errors.As(err, &ce) || ce.Code != "REVISION_CONFLICT" {
			return time.Time{}, err
		}
	}
	return time.Time{}, contract.Fail("REVISION_CONFLICT", "Repository is busy; retry later.", 412)
}
