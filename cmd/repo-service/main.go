package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/context4ai/sourcegraph/internal/appruntime"
	"github.com/context4ai/sourcegraph/internal/config"
	"github.com/context4ai/sourcegraph/internal/control"
	"github.com/context4ai/sourcegraph/internal/httpserver"
	"github.com/context4ai/sourcegraph/internal/service"
	"github.com/context4ai/sourcegraph/internal/sitesettings"
	"github.com/context4ai/sourcegraph/internal/sqlstore"
	"github.com/context4ai/sourcegraph/internal/sso"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var buildRevision = "development"

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(buildRevision)
		return
	}
	if e := run(); e != nil {
		slog.Error("service stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	root := env("SOURCEGRAPH_DATA_ROOT", "/data")
	if !filepath.IsAbs(root) {
		return errors.New("data root must be absolute")
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return e
	}
	var repos service.Store
	var auth sso.Store
	var settingsDB sitesettings.Store
	driver := env("SOURCEGRAPH_DATABASE", "sqlite")
	dsn := os.Getenv("DATABASE_URL")
	if driver == "mongodb" {
		db, e := control.Connect(ctx, dsn)
		if e != nil {
			return e
		}
		defer db.Close(context.Background())
		repos = db
		auth = &sso.MongoStore{DB: db.AuthDatabase()}
		settingsDB = &sitesettings.MongoStore{DB: db.AuthDatabase()}
	} else {
		if driver == "sqlite" && dsn == "" {
			dsn = filepath.Join(root, "sourcegraph.db")
		}
		db, e := sqlstore.Open(ctx, driver, dsn)
		if e != nil {
			return e
		}
		defer db.Close()
		go func() {
			tick := time.NewTicker(time.Hour)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					if err := db.Prune(ctx); err != nil {
						slog.Warn("expired record cleanup failed")
					}
				}
			}
		}()
		repos = &control.SQL{DB: db}
		auth = &sso.SQLStore{DB: db}
		settingsDB = &sitesettings.SQLStore{DB: db}
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	bin := filepath.Dir(exe)
	backend, e := service.New(repos, root, filepath.Join(bin, "git-credential-sourcegraph"), os.Getenv("SOURCEGRAPH_GIT_TOKEN"))
	if e != nil {
		return e
	}
	defer backend.Close()
	minimum, e := strconv.ParseUint(env("SOURCEGRAPH_MIN_FREE_BYTES", "1073741824"), 10, 64)
	if e != nil {
		return errors.New("invalid free disk threshold")
	}
	backend.MinFree = minimum
	backend.Builder.MinFree = minimum
	if e = backend.InitStorage(); e != nil {
		return e
	}
	index := filepath.Join(root, "index")
	if e = os.MkdirAll(index, 0700); e != nil {
		return e
	}
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	zoekt := exec.CommandContext(workerCtx, filepath.Join(bin, "zoekt-webserver"), "-index", index, "-listen", "127.0.0.1:6070", "-rpc", "-html=false")
	zoekt.Stdout = os.Stdout
	zoekt.Stderr = os.Stderr
	if e = zoekt.Start(); e != nil {
		return e
	}
	engineDone := make(chan error, 1)
	go func() { engineDone <- zoekt.Wait() }()
	defer func() {
		cancelWorker()
		select {
		case <-engineDone:
		case <-time.After(5 * time.Second):
		}
	}()
	origin := env("SOURCEGRAPH_ORIGIN", "http://localhost:8080")
	settings, e := sitesettings.New(ctx, settingsDB, true)
	if e != nil {
		return e
	}
	bootstrap, e := sso.NewCommunity(ctx, root, origin, auth, settings)
	if e != nil {
		return e
	}
	if os.Getenv("SOURCEGRAPH_SEED_DEMO") == "true" {
		for _, name := range []string{"context4ai/context", "context4ai/sourcegraph"} {
			if _, e = backend.RegisterWithOptions(ctx, name, control.Policy{DefaultBranch: true, Count: 1}, 60, "full", nil, "bootstrap:"+name); e != nil {
				var existing control.Repository
				existing, e = repos.Get(ctx, name)
				if e != nil || existing.Name != name {
					return fmt.Errorf("seed repository %s: %w", name, e)
				}
			}
		}
	}
	background := appruntime.Start(workerCtx, backend, settings, slog.Default(), appruntime.Options{})
	defer background.Stop()
	c := config.Defaults()
	c.DataRoot = root
	handler := httpserver.RuntimeWithBootstrap(c, background.Logger, backend, os.Getenv("SOURCEGRAPH_API_TOKEN"), settings, bootstrap)
	server := &http.Server{Addr: env("SOURCEGRAPH_LISTEN", "0.0.0.0:8080"), Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 120 * time.Second, WriteTimeout: 125 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	slog.Info("listening", "address", server.Addr, "origin", origin, "database", driver, "revision", buildRevision)
	select {
	case e = <-done:
		return e
	case e = <-engineDone:
		cancelWorker()
		_ = server.Close()
		return fmt.Errorf("search engine exited: %v", e)
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	e = server.Shutdown(shutdown)
	cancelWorker()
	if e != nil && !strings.Contains(e.Error(), "closed") {
		return e
	}
	return nil
}
