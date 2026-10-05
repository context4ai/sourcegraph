package control

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
	"math"
)

func (m *Mongo) NextGeneration(ctx context.Context) (uint32, error) {
	var v struct {
		Value int64 `bson:"value"`
	}
	e := m.client.Database("c4a").Collection("sourcegraph_counters").FindOneAndUpdate(ctx, bson.M{"_id": "generation"}, bson.M{"$inc": bson.M{"value": int64(1)}}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&v)
	if e != nil || v.Value < 1 || v.Value > math.MaxUint32 {
		return 0, errors.New("generation allocation unavailable")
	}
	return uint32(v.Value), nil
}
