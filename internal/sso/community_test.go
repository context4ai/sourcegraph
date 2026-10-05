package sso

import (
	"context"
	"errors"
	"github.com/context4ai/sourcegraph/internal/sitesettings"
	"github.com/context4ai/sourcegraph/internal/sqlstore"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSQLAdmissionAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "auth.db")
	db, e := sqlstore.Open(ctx, "sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	s := &SQLStore{DB: db}
	if e = initializeAccess(ctx, s, Config{}, ""); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners []string
	for _, id := range []string{"google:one", "github:two"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, first, e := s.AdmitUser(ctx, User{ID: id, Issuer: strings.Split(id, ":")[0], Subject: id, Enabled: true, Created: time.Now()}, false)
			mu.Lock()
			defer mu.Unlock()
			if e == nil && first {
				winners = append(winners, id)
			} else if !errors.Is(e, ErrRegistrationClosed) {
				t.Errorf("unexpected admission: first=%v err=%v", first, e)
			}
		}(id)
	}
	wg.Wait()
	if len(winners) != 1 {
		t.Fatalf("winners: %v", winners)
	}
	users, e := s.ListUsers(ctx, UserQuery{Limit: 100}, nil)
	if e != nil || len(users) != 1 {
		t.Fatalf("rejected user persisted: %+v %v", users, e)
	}
	db.Close()
	db, e = sqlstore.Open(ctx, "sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s.DB = db
	if _, first, e := s.AdmitUser(ctx, User{ID: winners[0], Enabled: true}, false); e != nil || first {
		t.Fatalf("owner login after restart: %v %v", first, e)
	}
	if _, _, e = s.AdmitUser(ctx, User{ID: "other", Enabled: true}, false); !errors.Is(e, ErrRegistrationClosed) {
		t.Fatal(e)
	}
	f := Flow{ID: "flow", Binding: "browser", Expires: time.Now().Add(time.Minute), Provider: "github"}
	if e = s.PutFlow(ctx, f); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ConsumeFlow(ctx, f.ID, "wrong", time.Now()); !errors.Is(e, ErrNotFound) {
		t.Fatal("wrong binding accepted")
	}
	if _, e = s.ConsumeFlow(ctx, f.ID, f.Binding, time.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ConsumeFlow(ctx, f.ID, f.Binding, time.Now()); !errors.Is(e, ErrNotFound) {
		t.Fatal("flow replay accepted")
	}
}
func TestClosedSessionAndSafeReturn(t *testing.T) {
	ctx := context.Background()
	db, e := sqlstore.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := &SQLStore{DB: db}
	_ = initializeAccess(ctx, s, Config{}, "")
	_, _, _ = s.AdmitUser(ctx, User{ID: "owner", Enabled: true}, false)
	_, _, _ = s.AdmitUser(ctx, User{ID: "reader", Enabled: true}, true)
	_ = s.PutSession(ctx, Session{ID: digest("cookie"), UserID: "reader", Expires: time.Now().Add(time.Hour)})
	settings, e := sitesettings.New(ctx, &sitesettings.SQLStore{DB: db}, true)
	if e != nil {
		t.Fatal(e)
	}
	h := &Handler{store: s, Settings: settings}
	r := httptest.NewRequest("GET", "http://localhost/sourcegraph/", nil)
	r.Header.Set("Cookie", sessionCookie+"=cookie")
	if h.ValidSession(r) {
		t.Fatal("closed site accepted old non-admin session")
	}
	for _, bad := range []string{"//evil.test", "https://evil.test", "/sourcegraph/auth/google/login", "/sourcegraph/../outside", "/%5cevil.test", "/context/%00"} {
		if safeReturn(bad) != "" {
			t.Fatalf("unsafe redirect: %s", bad)
		}
	}
	for _, good := range []string{"/", "/context/", "/context/guide", "/sourcegraph/", "/sourcegraph/code?repo=context4ai/context"} {
		if safeReturn(good) != good {
			t.Fatalf("safe redirect rejected: %s", good)
		}
	}
	w := httptest.NewRecorder()
	h.registrationClosed(w, r, "/context/")
	if w.Code != 403 || !strings.Contains(w.Body.String(), "5;url=/context/") || !strings.Contains(w.Body.String(), sitesettings.DefaultNoticeEN) {
		t.Fatalf("denial: %s", w.Body.String())
	}
}

func TestUnconfiguredProviderDiscovery(t *testing.T) {
	h := &Handler{Providers: map[string]*Handler{}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", Prefix+"providers", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"providers":[]`) {
		t.Fatalf("unconfigured discovery: %d %s", w.Code, w.Body.String())
	}
}
