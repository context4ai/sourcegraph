package sso

import (
	"context"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

func (s *MongoStore) accessCollection() *mongo.Collection {
	return s.DB.Collection("sourcegraph_access_control", options.Collection().SetReadPreference(readpref.Primary()).SetWriteConcern(writeconcern.New(writeconcern.WMajority(), writeconcern.WTimeout(5*time.Second))))
}

func (s *MongoStore) GetAccess(ctx context.Context) (AccessControl, error) {
	var a AccessControl
	err := s.accessCollection().FindOne(ctx, bson.M{"_id": accessID}).Decode(&a)
	return a, authStoreError(err)
}
func (s *MongoStore) CreateAccess(ctx context.Context, a AccessControl) (bool, error) {
	_, err := s.accessCollection().InsertOne(ctx, a)
	if mongo.IsDuplicateKeyError(err) {
		return false, nil
	}
	return err == nil, authStoreError(err)
}
func (s *MongoStore) ReplaceAccess(ctx context.Context, revision int64, a AccessControl) (bool, error) {
	result, err := s.accessCollection().ReplaceOne(ctx, bson.M{"_id": accessID, "revision": revision}, a)
	if err != nil {
		return false, authStoreError(err)
	}
	return result.MatchedCount == 1, nil
}
func (s *MongoStore) ListUsers(ctx context.Context, q UserQuery, adminIDs []string) ([]User, error) {
	filter := bson.M{}
	if q.After != "" {
		filter["_id"] = bson.M{"$gt": q.After}
	}
	if q.Q != "" {
		pattern := bson.M{"$regex": regexp.QuoteMeta(q.Q), "$options": "i"}
		filter["$or"] = bson.A{bson.M{"_id": pattern}, bson.M{"name": pattern}, bson.M{"email": pattern}}
	}
	if q.Department != "" {
		filter["department"] = q.Department
	}
	if q.Role != "" {
		op := "$in"
		if q.Role == roleUser {
			op = "$nin"
		}
		if adminIDs == nil {
			adminIDs = []string{}
		}
		filter["$and"] = bson.A{bson.M{"_id": bson.M{op: adminIDs}}}
	}
	cursor, err := s.DB.Collection("sourcegraph_users").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(q.Limit)).SetProjection(bson.M{"_id": 1, "name": 1, "email": 1, "department": 1, "created_at": 1, "last_login_at": 1, "enabled": 1}))
	if err != nil {
		return nil, authStoreError(err)
	}
	defer cursor.Close(ctx)
	out := []User{}
	if err = cursor.All(ctx, &out); err != nil {
		return nil, authStoreError(err)
	}
	return out, nil
}
