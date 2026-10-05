package service

import (
	"context"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/contract"
)

func TestPluginEmptyCatalogWithUnrelatedSpecialPaths(t *testing.T) {
	s, _, _, sha := readManyFixture(t, map[string]string{"fixtures/back\\slash.txt": "fixture\n", "doc.md": "normal\n"})
	setupPlugins(t, s)
	catalog, err := s.DiscoverPlugins(context.Background(), "org/repo", sha)
	if err != nil || len(catalog.Plugins) != 0 || len(catalog.Issues) != 0 {
		t.Fatalf("empty repository discovery: %+v %v", catalog, err)
	}
}
func TestPluginDiscoveryFailureReportsScopeAndReason(t *testing.T) {
	for _, err := range []error{contract.Fail("UNSUPPORTED_PATH_ENCODING", "private detail", 422), context.DeadlineExceeded} {
		issue := pluginDiscoveryIssue("", err)
		if issue.Code != "PLUGIN_DISCOVERY_FAILED" || !strings.Contains(issue.Message, "scope . (") || strings.Contains(issue.Message, "private detail") {
			t.Fatal(issue)
		}
	}
}
