package mcpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReadReadableWirePreservesRawContent(t *testing.T) {
	raw := "第一行\r\n\"quoted\" \\ path\n🙂 tail"
	file := gitstore.File{Path: "src/中文.go", Commit: strings.Repeat("a", 40), BlobOID: strings.Repeat("b", 40), Kind: "file", Encoding: "utf-8", Content: raw, ReturnedStartLine: ptr(7), ReturnedEndLine: ptr(9), NextStartLine: ptr(10), HasMore: ptr(true)}
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	register(server, "read", "test", func(context.Context, readInput) (any, error) { return file, nil })
	session, ctx := connectTest(t, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: map[string]any{"repo": "org/repo", "revision": file.Commit, "path": file.Path, "start_line": 7, "end_line": 9}})
	if err != nil || result.IsError {
		t.Fatalf("read: %v %#v", err, result)
	}
	if result.StructuredContent != nil {
		t.Fatal("unexpected structured output")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"org/repo", file.Commit, file.Path, "7: 第一行\r\n", "8: \"quoted\" \\ path\n", "9: 🙂 tail\n", "NextStartLine: 10"} {
		if !strings.Contains(text, want) {
			t.Errorf("read view missing %q", want)
		}
	}
	if strings.Contains(text, "10: ") {
		t.Fatal("invented trailing line")
	}
}

func TestReadViewEmptyLFSAndLimits(t *testing.T) {
	empty := readText("org/repo", gitstore.File{Path: "empty", HasMore: ptr(false)})
	if strings.Contains(empty, "1: ") || !strings.Contains(empty, "No text lines") {
		t.Fatal(empty)
	}
	lfs := readText("org/repo", gitstore.File{Path: "asset", IsLFSPointer: true, Content: "pointer\n", ReturnedStartLine: ptr(1), ReturnedEndLine: ptr(1), HasMore: ptr(false)})
	if !strings.Contains(lfs, "LFS pointer") || strings.Contains(lfs, "2: ") {
		t.Fatal(lfs)
	}
	file := gitstore.File{Content: strings.Repeat("🙂", readTextLimit), ReturnedStartLine: ptr(1), ReturnedEndLine: ptr(1)}
	text := readText("org/repo", file)
	if len(text) > readTextLimit || !strings.Contains(text, "TextTruncated: true") || strings.Contains(text, "1: ") {
		t.Fatal("display budget must omit complete lines")
	}
}

func TestRecoveryHintsByConnection(t *testing.T) {
	for _, canPrepare := range []bool{false, true} {
		for _, code := range []string{"INDEX_NOT_READY", "CONTENT_NOT_READY", "SCOPE_NOT_READY", "BRANCH_NOT_OBSERVED", "REVISION_NOT_ELIGIBLE"} {
			gotCode, msg := publicError(contract.Fail(code, "Prepare now.", 409), canPrepare)
			if gotCode != code {
				t.Fatal(gotCode)
			}
			if canPrepare != strings.Contains(msg, "use prepare with") {
				t.Errorf("canPrepare=%v %s: %s", canPrepare, code, msg)
			}
			if !canPrepare && !strings.Contains(msg, "read-only") {
				t.Fatal(msg)
			}
		}
	}
}

func TestReadManyWirePartialAndHints(t *testing.T) {
	original := service.ReadManyResult{Repository: "org/repo", Commit: strings.Repeat("c", 40), ReturnedBytes: 3, ReturnedLines: 1, Items: []service.ReadManyItem{
		{Request: contract.ReadItemRequest{Path: "ok", StartLine: 1, EndLine: 3}, File: &gitstore.File{Path: "ok", Commit: strings.Repeat("c", 40), Content: "ok\n", ReturnedStartLine: ptr(1), ReturnedEndLine: ptr(1), NextStartLine: ptr(2), HasMore: ptr(true), Truncated: true}, Error: &service.ReadManyError{Code: "READ_BUDGET_EXCEEDED", Message: "Continue from NextStartLine."}},
		{Request: contract.ReadItemRequest{Path: "missing", StartLine: 1, EndLine: 2}, Error: &service.ReadManyError{Code: "CONTENT_NOT_READY", Message: "Prepare group first."}},
		{Request: contract.ReadItemRequest{Path: "timed-out", StartLine: 1, EndLine: 2}, Error: &service.ReadManyError{Code: "READ_TIMEOUT", Message: "Batch deadline reached."}},
	}}
	calls := 0
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	register(server, "read_many", "test", func(context.Context, readManyInput) (any, error) { calls++; return original, nil })
	session, ctx := connectTest(t, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	args := map[string]any{"repo": "org/repo", "files": []map[string]any{{"path": "ok", "start_line": 1, "end_line": 3}, {"path": "missing", "start_line": 1, "end_line": 2}, {"path": "timed-out", "start_line": 1, "end_line": 2}}}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_many", Arguments: args})
	if err != nil || result.IsError {
		t.Fatalf("mixed result: %v %#v", err, result)
	}
	if result.StructuredContent != nil || len(result.Content) != 3 {
		t.Fatal("expected three content-only item blocks")
	}
	if !strings.Contains(result.Content[1].(*mcp.TextContent).Text, "read-only") {
		t.Fatal("missing recovery hint")
	}

	if original.Items[1].Error.Message != "Prepare group first." {
		t.Fatal("mutated Service result")
	}
	text := ""
	for _, block := range result.Content {
		text += block.(*mcp.TextContent).Text
	}
	if !strings.Contains(text, "1: ok\n") || !strings.Contains(text, "CONTENT_NOT_READY") || !strings.Contains(text, "NextStartLine: 2") || !strings.Contains(text, "READ_TIMEOUT") {
		t.Fatal(text)
	}
	// Invalid batch size is rejected by the actual SDK before invoking a read.
	args["files"] = []any{}
	bad, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_many", Arguments: args})
	if err == nil && (bad == nil || !bad.IsError) {
		t.Fatal("empty batch accepted")
	}
	if calls != 1 {
		t.Fatal("invalid batch reached handler")
	}
}
