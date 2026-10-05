package storagecontract

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/service"
	"github.com/context4ai/sourcegraph/internal/sitesettings"
	"github.com/context4ai/sourcegraph/internal/sqlstore"
	"github.com/context4ai/sourcegraph/internal/sso"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type admission interface {
	AdmitUser(context.Context, sso.User, bool) (sso.User, bool, error)
}

func TestBackendContract(t *testing.T) {
	drivers := []string{"sqlite"}
	if os.Getenv("TEST_POSTGRES_URL") != "" {
		drivers = append(drivers, "postgres")
	}
	if os.Getenv("TEST_MONGODB_URL") != "" {
		drivers = append(drivers, "mongodb")
	}
	for _, driver := range drivers {
		t.Run(driver, func(t *testing.T) {
			ctx := context.Background()
			var repos service.Store
			var auth sso.Store
			var settings sitesettings.Store
			if driver == "mongodb" {
				db, e := control.Connect(ctx, os.Getenv("TEST_MONGODB_URL"))
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close(ctx)
				repos = db
				auth = &sso.MongoStore{DB: db.AuthDatabase()}
				settings = &sitesettings.MongoStore{DB: db.AuthDatabase()}
			} else {
				dsn := os.Getenv("TEST_POSTGRES_URL")
				if driver == "sqlite" {
					dsn = filepath.Join(t.TempDir(), "db")
				}
				db, e := sqlstore.Open(ctx, driver, dsn)
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close()
				repos = &control.SQL{DB: db}
				auth = &sso.SQLStore{DB: db}
				settings = &sitesettings.SQLStore{DB: db}
			}
			r := control.Repository{ID: control.ID("context4ai/context"), Name: "context4ai/context", Revision: 1}
			if e := repos.Create(ctx, r); e != nil {
				t.Fatal(e)
			}
			lease := r
			lease.LeaseRevision = 1
			if e := repos.ReplaceLease(ctx, lease, 1); e != nil {
				t.Fatal(e)
			}
			r.Revision = 2
			if e := repos.Replace(ctx, r, 1); e == nil {
				t.Fatal("stale lease accepted")
			}
			got, e := repos.Get(ctx, r.Name)
			if e != nil || got.LeaseRevision != 1 {
				t.Fatal(got, e)
			}
			a, e := repos.NextGeneration(ctx)
			if e != nil {
				t.Fatal(e)
			}
			b, e := repos.NextGeneration(ctx)
			if e != nil || b <= a {
				t.Fatal(a, b, e)
			}
			config, e := sitesettings.New(ctx, settings, true)
			if e != nil {
				t.Fatal(e)
			}
			if config.Snapshot().Access.Values.AllowRegistration {
				t.Fatal("registration open by default")
			}
			if _, e = auth.CreateAccess(ctx, sso.AccessControl{ID: "website", Revision: 1, BootstrapPending: true}); e != nil {
				t.Fatal(e)
			}
			if indexed, ok := auth.(interface{ EnsureIndexes(context.Context) error }); ok {
				if e = indexed.EnsureIndexes(ctx); e != nil {
					t.Fatal(e)
				}
			}
			var wg sync.WaitGroup
			var mu sync.Mutex
			winners := 0
			for _, id := range []string{"google-owner", "github-owner"} {
				wg.Add(1)
				go func(id string) {
					defer wg.Done()
					_, first, e := auth.(admission).AdmitUser(ctx, sso.User{ID: id, Issuer: id, Subject: id, Enabled: true, Created: time.Now()}, false)
					mu.Lock()
					defer mu.Unlock()
					if e == nil && first {
						winners++
					} else if !errors.Is(e, sso.ErrRegistrationClosed) {
						t.Errorf("admission error: %v", e)
					}
				}(id)
			}
			wg.Wait()
			if winners != 1 {
				t.Fatalf("administrators: %d", winners)
			}
			users, e := auth.ListUsers(ctx, sso.UserQuery{Limit: 10}, nil)
			if e != nil || len(users) != 1 {
				t.Fatalf("rejected identity persisted: %d %v", len(users), e)
			}
			if e = auth.PutFlow(ctx, sso.Flow{ID: "once", Binding: "browser", Expires: time.Now().Add(time.Minute)}); e != nil {
				t.Fatal(e)
			}
			if _, e = auth.ConsumeFlow(ctx, "once", "browser", time.Now()); e != nil {
				t.Fatal(e)
			}
			if _, e = auth.ConsumeFlow(ctx, "once", "browser", time.Now()); !errors.Is(e, sso.ErrNotFound) {
				t.Fatal("flow replay", e)
			}
		})
	}
}
