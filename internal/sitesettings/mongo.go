package sitesettings

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

// MongoStore shares the existing authenticated MongoDB client/database. There
// are exactly two section IDs, protected by MongoDB's built-in unique _id index.
// No additional index or collection-wide migration is required.
type MongoStore struct{ DB *mongo.Database }

func (m *MongoStore) collection() *mongo.Collection {
	return m.DB.Collection(Collection, options.Collection().SetReadPreference(readpref.Primary()).SetWriteConcern(writeconcern.New(writeconcern.WMajority(), writeconcern.WTimeout(5*time.Second))))
}
func (m *MongoStore) Ensure(ctx context.Context, r Record) error {
	if m.DB == nil || !validSection(r.Name) || r.Revision != 1 {
		return ErrUnavailable
	}
	_, err := m.collection().UpdateOne(ctx, bson.M{"_id": r.Name}, bson.M{"$setOnInsert": r}, options.Update().SetUpsert(true))
	// A concurrent bootstrap that wins the unique _id race is read in Load.
	if mongo.IsDuplicateKeyError(err) {
		return nil
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
func (m *MongoStore) Load(ctx context.Context) ([]Record, error) {
	if m.DB == nil {
		return nil, ErrUnavailable
	}
	cursor, err := m.collection().Find(ctx, bson.M{"_id": bson.M{"$in": []string{RepositoryDefaultsName, AccessName}}}, options.Find().SetLimit(2))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer cursor.Close(ctx)
	var out []Record
	if err = cursor.All(ctx, &out); err != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}
func (m *MongoStore) Replace(ctx context.Context, r Record, expected int64) error {
	if m.DB == nil || !validSection(r.Name) || expected < 1 || r.Revision != expected+1 {
		return ErrUnavailable
	}
	result, err := m.collection().ReplaceOne(ctx, bson.M{"_id": r.Name, "revision": expected}, r)
	if err != nil {
		return ErrUnavailable
	}
	if result.MatchedCount != 1 {
		return ErrConflict
	}
	return nil
}
