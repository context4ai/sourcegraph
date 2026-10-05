package sso

import (
	"context"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const apiKeyCollection = "sourcegraph_api_keys"

func (s *MongoStore) ensureAPIKeyIndexes(ctx context.Context) error {
	_, err := s.DB.Collection(apiKeyCollection).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "_id", Value: 1}}, Options: options.Index().SetName("api_key_owner")},
		{Keys: bson.D{{Key: "key_hash", Value: 1}}, Options: options.Index().SetName("api_key_digest").SetUnique(true)},
	})
	return authStoreError(err)
}
func (s *MongoStore) CreateAPIKey(ctx context.Context, key APIKey) error {
	_, err := s.DB.Collection(apiKeyCollection).InsertOne(ctx, key)
	return authStoreError(err)
}
func (s *MongoStore) ListAPIKeys(ctx context.Context, owner, after string, limit int) ([]APIKey, error) {
	filter := bson.M{"user_id": owner}
	if after != "" {
		filter["_id"] = bson.M{"$gt": after}
	}
	cursor, err := s.DB.Collection(apiKeyCollection).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, authStoreError(err)
	}
	defer cursor.Close(ctx)
	var records []APIKey
	err = cursor.All(ctx, &records)
	return records, authStoreError(err)
}
func (s *MongoStore) DeleteAPIKey(ctx context.Context, owner, id string) (bool, error) {
	result, err := s.DB.Collection(apiKeyCollection).DeleteOne(ctx, bson.M{"_id": id, "user_id": owner})
	if err != nil {
		return false, authStoreError(err)
	}
	return result.DeletedCount > 0, nil
}

func (s *MongoStore) FindAPIKey(ctx context.Context, hash string) (APIKey, error) {
	var key APIKey
	err := s.DB.Collection(apiKeyCollection).FindOne(ctx, bson.M{"key_hash": hash}).Decode(&key)
	return key, authStoreError(err)
}
