package indexer

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/zoektclient"
	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/index"
)

const workerArgument = "--sourcegraph-internal-index-worker"

type workerHeader struct {
	Directory string
	Target    zoektclient.Target
	Metadata  map[string]string
}
type workerDocument struct {
	Name     string
	Content  []byte
	TooLarge bool
}

// Re-exec keeps the exact same library/version in production, local development
// and Go tests, without requiring an additional binary in the release bundle.
// This private mode reads only a parent-created pipe and never starts the server.
func init() {
	if len(os.Args) == 2 && os.Args[1] == workerArgument {
		if err := indexWorker(os.Stdin); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func indexWorker(input io.Reader) error {
	decoder := gob.NewDecoder(input)
	var header workerHeader
	if err := decoder.Decode(&header); err != nil {
		return err
	}
	t := header.Target
	options := index.Options{IndexDir: header.Directory, ShardPrefixOverride: fmt.Sprintf("snapshot-%d", t.RepositoryID), Parallelism: 2, ShardMax: 104857600, SizeMax: maxFileBytes, TrigramMax: maxFileTrigrams, DisableCTags: true,
		RepositoryDescription: zoekt.Repository{ID: t.RepositoryID, Name: t.EngineName(), Branches: []zoekt.RepositoryBranch{{Name: "snapshot", Version: t.Commit}}, Metadata: header.Metadata}}
	builder, err := index.NewBuilder(options)
	if err != nil {
		return err
	}
	// Only normal EOF means finish. On errors the child exits; it must not flush
	// buffered work after cancellation. The parent owns staging cleanup.
	for {
		var doc workerDocument
		if err = decoder.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		item := index.Document{Name: doc.Name, Content: doc.Content, Branches: []string{"snapshot"}}
		if doc.TooLarge {
			item.SkipReason = index.SkipReasonTooLarge
		}
		if err = builder.Add(item); err != nil {
			return err
		}
	}
	return builder.Finish()
}

func freeSpace(root string, min uint64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(root, &stat); err != nil {
		return err
	}
	if uint64(stat.Bavail)*uint64(stat.Bsize) < min {
		return fmt.Errorf("insufficient index storage")
	}
	return nil
}

// superviseIndex monitors the entire child lifetime, including Finish/shard writes.
// Cancellation kills the process group and Wait reaps it before the stage is removed.
func superviseIndex(ctx context.Context, command *exec.Cmd, check func() error, feed func(context.Context, io.Writer) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	if err = command.Start(); err != nil {
		stdin.Close()
		return err
	}
	done := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
				return
			case <-tick.C:
				if err := check(); err != nil {
					cancel(err)
				}
			}
		}
	}()
	feedErr := feed(ctx, stdin)
	if feedErr != nil {
		cancel(feedErr)
	}
	stdin.Close()
	waitErr := command.Wait()
	close(done)
	<-monitorDone
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if waitErr != nil {
		return fmt.Errorf("index preparation failed: worker exited unsuccessfully")
	}
	return check()
}

func (b Builder) buildIsolated(ctx context.Context, store *gitstore.Store, t zoektclient.Target, entries []gitstore.Entry, metadata map[string]string, opts Options, idx string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, workerArgument)
	command.Env = append(os.Environ(), "GOMAXPROCS=2")
	return superviseIndex(ctx, command, func() error { return freeSpace(b.Root, b.MinFree) }, func(ctx context.Context, w io.Writer) error {
		encoder := gob.NewEncoder(w)
		if err := encoder.Encode(workerHeader{Directory: idx, Target: t, Metadata: metadata}); err != nil {
			return err
		}
		return store.VisitBlobs(ctx, t.Repo, t.Commit, entries, maxFileBytes, func(entry gitstore.Entry, content []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := entry.Path
			if entry.CanonicalPath != "" {
				name = entry.CanonicalPath
			}
			if opts.SkipGenerated && content != nil && Generated(name, content) {
				return nil
			}
			return encoder.Encode(workerDocument{Name: entry.Path, Content: content, TooLarge: entry.Size != nil && *entry.Size > maxFileBytes})
		})
	})
}
