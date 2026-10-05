package sso

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"time"
)

// MongoStore uses only Sourcegraph-owned collections and the existing client.
type MongoStore struct{ DB *mongo.Database }

func (s *MongoStore) EnsureIndexes(ctx context.Context) error {
	for _, name := range []string{"sourcegraph_sessions", "sourcegraph_login_flows"} {
		_, err := s.DB.Collection(name).Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: options.Index().SetName("auth_expiry").SetExpireAfterSeconds(0)})
		if err != nil {
			return errors.New("SSO expiry index initialization failed")
		}
	}
	_, err := s.DB.Collection("sourcegraph_users").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "issuer", Value: 1}, {Key: "subject", Value: 1}}, Options: options.Index().SetName("auth_identity").SetUnique(true)})
	if err != nil {
		return errors.New("SSO identity index initialization failed")
	}
	return s.ensureAPIKeyIndexes(ctx)
}
func authStoreError(e error) error {
	if errors.Is(e, mongo.ErrNoDocuments) {
		return ErrNotFound
	}
	if e != nil {
		return errors.New("authentication storage unavailable")
	}
	return nil
}
func (s *MongoStore) PutFlow(ctx context.Context, f Flow) error {
	_, e := s.DB.Collection("sourcegraph_login_flows").InsertOne(ctx, f)
	return authStoreError(e)
}
func (s *MongoStore) ConsumeFlow(ctx context.Context, id, binding string, now time.Time) (Flow, error) {
	var f Flow
	e := s.DB.Collection("sourcegraph_login_flows").FindOneAndDelete(ctx, bson.M{"_id": id, "binding": binding, "expires_at": bson.M{"$gt": now}}).Decode(&f)
	return f, authStoreError(e)
}
func (s *MongoStore) UpsertUser(ctx context.Context, u User) (User, error) {
	var out User
	filter := bson.M{"_id": u.ID}
	update := bson.M{"$set": bson.M{"name": u.Name, "email": u.Email, "picture": u.Picture, "last_login_at": u.LastLogin}, "$setOnInsert": bson.M{"issuer": u.Issuer, "subject": u.Subject, "created_at": u.Created, "enabled": true}}
	if u.Department != "" {
		update["$set"].(bson.M)["department"] = u.Department
	}
	e := s.DB.Collection("sourcegraph_users").FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&out)
	// Concurrent first login can lose the unique-index upsert race; retry as an update.
	if mongo.IsDuplicateKeyError(e) {
		e = s.DB.Collection("sourcegraph_users").FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&out)
	}
	return out, authStoreError(e)
}
func (s *MongoStore) GetUser(ctx context.Context, id string) (User, error) {
	var u User
	e := s.DB.Collection("sourcegraph_users").FindOne(ctx, bson.M{"_id": id}).Decode(&u)
	return u, authStoreError(e)
}
func (s *MongoStore) PutSession(ctx context.Context, v Session) error {
	_, e := s.DB.Collection("sourcegraph_sessions").InsertOne(ctx, v)
	return authStoreError(e)
}
func (s *MongoStore) GetSession(ctx context.Context, id string) (Session, error) {
	var v Session
	e := s.DB.Collection("sourcegraph_sessions").FindOne(ctx, bson.M{"_id": id}).Decode(&v)
	return v, authStoreError(e)
}
func (s *MongoStore) DeleteSession(ctx context.Context, id string) error {
	_, e := s.DB.Collection("sourcegraph_sessions").DeleteOne(ctx, bson.M{"_id": id})
	return authStoreError(e)
}

// Authentication admission spans profile and role authority, requiring MongoDB
// transactions (a replica set, including a single-node replica set, or Atlas).
func (s *MongoStore) AdmitUser(ctx context.Context, u User, allow bool) (User, bool, error) {
	session, e := s.DB.Client().StartSession()
	if e != nil {
		return u, false, e
	}
	defer session.EndSession(ctx)
	first := false
	_, e = session.WithTransaction(ctx, func(sc mongo.SessionContext) (any, error) {
		first = false
		a, e := readAccess(sc, s)
		if e != nil {
			return nil, e
		}
		old, e := s.GetUser(sc, u.ID)
		if e == nil && !old.Enabled {
			return nil, ErrRegistrationClosed
		}
		if e != nil && !errors.Is(e, ErrNotFound) {
			return nil, e
		}
		if a.BootstrapPending {
			a.BootstrapPending = false
			a.AdminIDs = []string{u.ID}
			a.FounderID = u.ID
			a.Updated = time.Now().UTC()
			a.Events = []RoleEvent{{Actor: u.ID, Target: u.ID, Role: roleAdmin, At: a.Updated}}
			rev := a.Revision
			a.Revision++
			result, e := s.accessCollection().ReplaceOne(sc, bson.M{"_id": accessID, "revision": rev}, a)
			if e != nil {
				return nil, e
			}
			if result.MatchedCount != 1 {
				return nil, errors.New("concurrent administrator admission; retry login")
			}
			first = true
		} else if a.role(u.ID) != roleAdmin && !allow {
			return nil, ErrRegistrationClosed
		}
		u, e = s.UpsertUser(sc, u)
		return nil, e
	})
	return u, first, e
}
