package control

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"time"
)

// Telemetry is replace-by-key, so retries never add to a counter twice. TTL only
// applies to observations, never policies, receipts, leases or active jobs.
func (m *Mongo) PrepareTelemetry(ctx context.Context) error {
	_, e := m.client.Database("c4a").Collection("sourcegraph_usage").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: options.Index().SetName("observations_expire").SetExpireAfterSeconds(0)})
	if e == nil {
		_, e = m.client.Database("c4a").Collection("sourcegraph_usage").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "observed_at", Value: -1}, {Key: "_id", Value: 1}}, Options: options.Index().SetName("recent_observations")})
	}
	return e
}

type Observation struct {
	InstanceID string    `bson:"_id" json:"instance_id"`
	Payload    bson.M    `bson:"payload" json:"payload"`
	ObservedAt time.Time `bson:"observed_at" json:"observed_at"`
}

// Reads are dashboard-only; the query path never waits for the telemetry store.
func (m *Mongo) RecentObservations(ctx context.Context, since time.Time, limit int) ([]Observation, error) {
	if limit < 1 || limit > 101 {
		return nil, errors.New("invalid observation limit")
	}
	c, e := m.client.Database("c4a").Collection("sourcegraph_usage").Find(ctx, bson.M{"observed_at": bson.M{"$gte": since}, "payload.schema": 2}, options.Find().SetSort(bson.D{{Key: "observed_at", Value: -1}, {Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if e != nil {
		return nil, errors.New("instance observations unavailable")
	}
	defer c.Close(ctx)
	rows := []Observation{}
	if e = c.All(ctx, &rows); e != nil {
		return nil, errors.New("instance observations unavailable")
	}
	return rows, nil
}
func (m *Mongo) WriteObservation(ctx context.Context, id string, payload any, at time.Time) error {
	_, e := m.client.Database("c4a").Collection("sourcegraph_usage").ReplaceOne(ctx, bson.M{"_id": id}, bson.M{"_id": id, "payload": payload, "observed_at": at, "expires_at": at.Add(30 * 24 * time.Hour)}, options.Replace().SetUpsert(true))
	return e
}
