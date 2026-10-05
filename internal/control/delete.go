package control

import (
	"context"
	"github.com/context4ai/sourcegraph/internal/contract"
	"go.mongodb.org/mongo-driver/bson"
)

func (m *Mongo) Delete(ctx context.Context, r Repository) error {
	result, e := m.collection.DeleteOne(ctx, bson.M{"_id": r.ID, "revision": r.Revision, "deleted": true, "enabled": false})
	if e != nil {
		return contract.Fail("CONTROL_STATE_UNAVAILABLE", "Cleanup result is unknown; reload state.", 503)
	}
	if result.DeletedCount != 1 {
		return contract.Fail("REVISION_CONFLICT", "Repository changed during cleanup.", 412)
	}
	return nil
}
