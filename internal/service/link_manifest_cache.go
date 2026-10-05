package service

import (
	"container/list"
	"encoding/json"
	"io"
	"os"
	"sync"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/indexer"
)

// Published manifests are immutable. Stat also invalidates cached snapshots
// after collection or replacement. Bounds apply to serialized bytes and entries.
type linkManifestCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     list.List
	bytes   int64
}
type cachedLinkManifest struct {
	name     string
	info     os.FileInfo
	artifact *indexer.Artifact
}

func (c *linkManifestCache) load(name string) (*indexer.Artifact, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	remove := func(e *list.Element) {
		v := e.Value.(cachedLinkManifest)
		c.bytes -= v.info.Size()
		delete(c.entries, v.name)
		c.lru.Remove(e)
	}
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > indexer.MaxManifestBytes {
		if e := c.entries[name]; e != nil {
			remove(e)
		}
		return nil, contract.Fail("INDEX_NOT_READY", "Snapshot manifest is unavailable or invalid.", 409)
	}
	if e := c.entries[name]; e != nil {
		v := e.Value.(cachedLinkManifest)
		if os.SameFile(info, v.info) && info.Size() == v.info.Size() && info.ModTime() == v.info.ModTime() {
			c.lru.MoveToFront(e)
			return v.artifact, nil
		}
		remove(e)
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, contract.Fail("INDEX_NOT_READY", "Snapshot manifest is unavailable.", 409)
	}
	data, err := io.ReadAll(io.LimitReader(f, indexer.MaxManifestBytes+1))
	f.Close()
	var a indexer.Artifact
	if err != nil || len(data) > indexer.MaxManifestBytes || json.Unmarshal(data, &a) != nil || a.Links == nil {
		return nil, contract.Fail("INDEX_NOT_READY", "Snapshot manifest is invalid.", 409)
	}
	objects, err := linkObjects(a.Links)
	if err != nil {
		return nil, err
	}
	a.Links.Objects = objects
	if c.entries == nil {
		c.entries = map[string]*list.Element{}
	}
	c.entries[name] = c.lru.PushFront(cachedLinkManifest{name, info, &a})
	c.bytes += info.Size()
	for c.bytes > 64<<20 || c.lru.Len() > 64 {
		remove(c.lru.Back())
	}
	return &a, nil
}
