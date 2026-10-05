// Package wasmplugin hosts repository-supplied, read-only Wasm enhancements.
// It deliberately knows nothing about a plugin's business input or output schema.
package wasmplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"
)

const Suffix = ".sourcegraph.wasm"

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func ValidName(name string) bool { return validName.MatchString(name) }

type Request struct {
	Name string         `json:"name" jsonschema:"Plugin file name without .sourcegraph.wasm."`
	Args map[string]any `json:"args,omitempty" jsonschema:"Plugin-defined arguments. See its optional metadata; no fixed business schema."`
}
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func Failure(code, message string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"issues": []Issue{{code, message}}})
	return b
}

// Metadata declares the required JSON ABI version. Other fields are advisory;
// business payloads are opaque to the host.
type Metadata struct {
	DefaultEnabled bool            `json:"default_enabled,omitempty"`
	Name           string          `json:"name,omitempty"`
	Title          string          `json:"title,omitempty"`
	Description    string          `json:"description,omitempty"`
	Version        string          `json:"version,omitempty"`
	ABIVersion     int             `json:"abi_version,omitempty"`
	Operations     []string        `json:"operations,omitempty"`
	InputSchema    json.RawMessage `json:"input_schema,omitempty"`
}
type Reader func(context.Context, string) ([]byte, error)
type Config struct {
	Disabled                                                                              bool
	Timeout                                                                               time.Duration
	MemoryMiB, ArtifactBytes, MetadataBytes, FileBytes, ReadBytes, OutputBytes, ReadCalls int
	Concurrency, CacheEntries, IdleInstances                                              int
}

func DefaultConfig() Config {
	return Config{Timeout: 10 * time.Second, MemoryMiB: 256, ArtifactBytes: 32 << 20, MetadataBytes: 256 << 10, FileBytes: 32 << 20, ReadBytes: 128 << 20, OutputBytes: 2 << 20, ReadCalls: 1024, Concurrency: 8, CacheEntries: 16, IdleInstances: 8}
}

// Environment configuration is shared by every Service entry point. Invalid
// overrides fail startup instead of accidentally removing a resource guard.
func FromEnv() (Config, error) {
	c := DefaultConfig()
	c.Disabled = os.Getenv("SOURCEGRAPH_PLUGINS_DISABLED") == "1"
	fields := map[string]*int{"MEMORY_MIB": &c.MemoryMiB, "ARTIFACT_BYTES": &c.ArtifactBytes, "METADATA_BYTES": &c.MetadataBytes, "FILE_BYTES": &c.FileBytes, "READ_BYTES": &c.ReadBytes, "OUTPUT_BYTES": &c.OutputBytes, "READ_CALLS": &c.ReadCalls, "CONCURRENCY": &c.Concurrency, "CACHE_ENTRIES": &c.CacheEntries, "IDLE_INSTANCES": &c.IdleInstances}
	for name, p := range fields {
		if v := os.Getenv("SOURCEGRAPH_PLUGINS_" + name); v != "" {
			n, e := strconv.Atoi(v)
			if e != nil || n < 1 {
				return c, fmt.Errorf("invalid plugin setting %s", name)
			}
			*p = n
		}
	}
	if v := os.Getenv("SOURCEGRAPH_PLUGINS_TIMEOUT"); v != "" {
		d, e := time.ParseDuration(v)
		if e != nil || d <= 0 {
			return c, fmt.Errorf("invalid plugin timeout")
		}
		c.Timeout = d
	}
	if c.MemoryMiB > 4096 {
		return c, fmt.Errorf("plugin memory exceeds Wasm32 address space")
	}
	return c, nil
}
