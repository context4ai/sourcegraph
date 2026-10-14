package codesources

import (
	"fmt"
	"io"
	"os"

	"github.com/context4ai/sourcegraph/internal/contract"
)

// LoadFile reads the optional startup file named by SOURCEGRAPH_CONFIG_FILE:
// {"code_sources": {"git": {"name": "...", "host": "https://...", "token_env": "..."}}}.
func LoadFile(path string) (map[string]Override, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read configuration file: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return nil, fmt.Errorf("read configuration file: %w", err)
	}
	if len(data) > 65536 {
		return nil, fmt.Errorf("configuration file exceeds 64 KiB")
	}
	var file struct {
		CodeSources map[string]Override `json:"code_sources"`
	}
	if err = contract.DecodeObject(data, &file); err != nil {
		return nil, fmt.Errorf("invalid configuration file: %w", err)
	}
	return file.CodeSources, nil
}
