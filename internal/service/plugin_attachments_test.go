package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/wasmplugin"
)

func TestAttachmentProtocolIsolation(t *testing.T) {
	out := EnhancedRead{}
	out.acceptAttachments(json.RawMessage(`{"attachments":[{"item_id":"read-2","text":"correct"},{"item_id":"read-0","text":"wrong scope"},{"item_id":null,"text":"bad"},{"item_id":42,"text":"bad"},{"text":null},{"text":"batch"},{"item_id":"read-2","text":"second"},{"text":""}]}`), []map[string]any{{"item_id": "read-2"}})
	if len(out.Attachments) != 3 || len(out.Issues) != 4 || out.Attachments[0].ItemID != "read-2" || out.Attachments[1].ItemID != "" || out.Attachments[2].Text != "second" {
		t.Fatalf("%+v", out)
	}
	for _, raw := range []string{`42`, `null`, `{"attachments":null}`, `{"attachments":{}}`, `{"sections":[]}`, `{"issues":[]}`} {
		out := EnhancedRead{}
		out.acceptAttachments(json.RawMessage(raw), nil)
		if len(out.Attachments) != 0 || len(out.Issues) == 0 {
			t.Fatalf("accepted legacy/invalid result %s", raw)
		}
	}
}
func TestAttachmentBudgetDoesNotClipOrReplaceBody(t *testing.T) {
	out := EnhancedRead{Result: "original"}
	raw, _ := json.Marshal(map[string]any{"attachments": []PluginAttachment{{Text: strings.Repeat("x", pluginAttachmentBudget)}, {Text: "link"}}})
	out.acceptAttachments(raw, nil)
	if out.Result != "original" || len(out.Attachments) != 1 || len(out.Attachments[0].Text) != pluginAttachmentBudget || len(out.Issues) != 1 {
		t.Fatalf("body/budget %+v", out.Issues)
	}
	out.acceptAttachments(json.RawMessage(`{"attachments":[{"text":"next invocation"}]}`), nil)
	if len(out.Attachments) != 1 || len(out.Issues) != 2 {
		t.Fatal("budget reset between invocations")
	}
}
func TestRealWasmAttachmentsKeepOriginalRequestIDs(t *testing.T) {
	artifact := defaultPluginFixture(t, true, "read", "read_many")
	s, _, _, sha := readManyFixture(t, map[string]string{"a/fixture.sourcegraph.wasm": artifact, "b/fixture.sourcegraph.wasm": artifact, "a/doc.md": "first\nsecond\n", "b/doc.md": "third\n"})
	setupPlugins(t, s)
	items := []contract.ReadItemRequest{{Path: "missing", StartLine: 1, EndLine: 1}, {Path: "a/doc.md", StartLine: 1, EndLine: 1}, {Path: "b/doc.md", StartLine: 1, EndLine: 1}, {Path: "a/doc.md", StartLine: 2, EndLine: 2}, {Path: "a/doc.md", StartLine: 1, EndLine: 1}}
	out, err := s.ReadManyWithPlugins(context.Background(), "org/repo", sha, items, []wasmplugin.Request{{Name: "fixture", Args: map[string]any{"action": "attachments"}}})
	if err != nil || len(out.Issues) != 0 || len(out.Attachments) != 4 {
		t.Fatalf("%+v %v", out, err)
	}
	// Root a executes first; root b still receives its original ID.
	for i, id := range []string{"read-1", "read-3", "read-4", "read-2"} {
		if out.Attachments[i].ItemID != id {
			t.Fatalf("%+v", out.Attachments)
		}
	}
	batch := out.Result.(ReadManyResult)
	if batch.Items[0].Error == nil || batch.Items[3].File.Content != "second\n" {
		t.Fatal("body changed")
	}
}

func TestSymlinkReadsUseCanonicalPluginScope(t *testing.T) {
	artifact := defaultPluginFixture(t, true, "read", "read_many")
	s, db, dir, sha := readManyFixture(t, map[string]string{"a/fixture.sourcegraph.wasm": artifact, "a/doc.md": "inside\n", "outside.md": "outside\n"})
	// Add two links without a checkout: a/exit leaves the plugin root, entrance
	// enters it. Plugin selection must use the resolved file, not either alias.
	oldTree := readManyGit(t, dir, "", "rev-parse", sha+"^{tree}")
	aTree := readManyGit(t, dir, "", "rev-parse", sha+":a")
	rows := readManyGit(t, dir, "", "ls-tree", aTree) + "\n"
	blob := readManyGit(t, dir, "../outside.md", "hash-object", "-w", "--stdin")
	newA := readManyGit(t, dir, rows+"120000 blob "+blob+"\texit\n", "mktree")
	top := readManyGit(t, dir, "", "ls-tree", oldTree)
	top = strings.Replace(top, aTree, newA, 1)
	entrance := readManyGit(t, dir, "a/doc.md", "hash-object", "-w", "--stdin")
	tree := readManyGit(t, dir, top+"\n120000 blob "+entrance+"\tentrance\n", "mktree")
	next := readManyGit(t, dir, "", "commit-tree", tree, "-p", sha, "-m", "links")
	db.repo.Versions[0].Commit = next
	db.repo.Heads["main"] = next
	setupPlugins(t, s)
	requests := []contract.ReadItemRequest{{Path: "a/exit", StartLine: 1, EndLine: 1}, {Path: "entrance", StartLine: 1, EndLine: 1}, {Path: "a/doc.md", StartLine: 1, EndLine: 1}}
	out, err := s.ReadManyWithPlugins(context.Background(), "org/repo", next, requests, []wasmplugin.Request{{Name: "fixture", Args: map[string]any{"action": "attachments"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Attachments) != 2 || out.Attachments[0].ItemID != "read-1" || out.Attachments[1].ItemID != "read-2" {
		t.Fatalf("plugin crossed alias scope: %+v", out)
	}
	batch := out.Result.(ReadManyResult)
	if batch.Items[0].File.Path != "outside.md" || batch.Items[0].File.Content != "outside\n" {
		t.Fatal(batch)
	}
}
