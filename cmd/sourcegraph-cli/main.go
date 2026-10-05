// sourcegraph-cli is a bounded HTTP client; it never clones or installs skills.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ",") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

func main() { os.Exit(run()) }
func run() int {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: sourcegraph-cli repositories|availability|resolve|search|read|list|diff|prepare [flags]")
		return 2
	}
	fs := flag.NewFlagSet("sourcegraph-cli", flag.ContinueOnError)
	repo := fs.String("repo", "", "canonical repository")
	revision := fs.String("revision", "", "full SHA or configured branch")
	pattern := fs.String("e", "", "search pattern, like rg -e; otherwise the first argument")
	fixed := fs.Bool("F", false, "search: match the pattern as literal text, like rg -F")
	ignoreCase := fs.Bool("i", false, "search: ignore case, like rg -i")
	filesOnly := fs.Bool("l", false, "search: print only matched paths, like rg -l")
	var globs repeated
	fs.Var(&globs, "g", "search: include or !exclude files by glob, like rg -g; repeatable")
	path := fs.String("path", "", "relative Git path")
	start := fs.Int("start-line", 1, "first line")
	end := fs.Int("end-line", 100, "last line")
	first := fs.Int("first", 100, "page size")
	after := fs.String("after", "", "page cursor")
	base := fs.String("base", "", "base SHA")
	head := fs.String("head", "", "head SHA")
	key := fs.String("key", "", "stable idempotency key for prepare")
	kind := fs.String("kind", "index", "index or sync")
	if fs.Parse(os.Args[2:]) != nil {
		return 2
	}
	endpoint := os.Getenv("SOURCEGRAPH_URL")
	if endpoint == "" {
		endpoint = "https://context4ai.org/sourcegraph"
	}
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		fmt.Fprintln(os.Stderr, "SOURCEGRAPH_URL must be a credential-free HTTPS URL")
		return 2
	}
	token := os.Getenv("SOURCEGRAPH_API_TOKEN")
	if token != "" && len(token) < 32 {
		fmt.Fprintln(os.Stderr, "SOURCEGRAPH_API_TOKEN must contain at least 32 characters when supplied")
		return 2
	}
	route := ""
	method := "POST"
	var body any
	switch os.Args[1] {
	case "repositories":
		method = "GET"
		route = "/v1/repos"
	case "availability":
		method = "GET"
		route = "/v1/repo/availability"
	case "resolve":
		route = "/v1/repo/resolve"
		body = map[string]any{"Revision": *revision}
	case "search":
		route = "/api/search"
		// Like rg: search [flags] PATTERN [PATH...]; flags must precede PATTERN.
		args := fs.Args()
		if *pattern == "" && len(args) > 0 {
			*pattern, args = args[0], args[1:]
		}
		if *pattern == "" {
			fmt.Fprintln(os.Stderr, "Usage: sourcegraph-cli search -repo R [-revision REV] [-F] [-i] [-l] [-g GLOB]... PATTERN [PATH...]")
			return 2
		}
		output := "content"
		if *filesOnly {
			output = "files"
		}
		search := map[string]any{"Revision": *revision, "Pattern": *pattern, "FixedStrings": *fixed, "IgnoreCase": *ignoreCase, "Output": output}
		if len(globs) > 0 {
			search["Glob"] = []string(globs)
		}
		if len(args) > 0 {
			search["Paths"] = args
		}
		body = search
	case "read":
		route = "/v1/repo/read"
		body = map[string]any{"Revision": *revision, "Path": *path, "StartLine": *start, "EndLine": *end}
	case "list":
		route = "/v1/repo/list"
		body = map[string]any{"Revision": *revision, "Path": *path, "First": *first, "After": *after}
	case "diff":
		route = "/v1/repo/diff"
		body = map[string]any{"Base": *base, "Head": *head, "First": *first, "After": *after}
	case "prepare":
		route = "/api/admin/v1/repo/prepare"
		body = map[string]any{"kind": *kind, "commit": *revision}
	default:
		fmt.Fprintln(os.Stderr, "Unknown operation")
		return 2
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + route
	params := url.Values{}
	if *repo != "" {
		params.Set("repo", *repo)
	}
	if os.Args[1] == "repositories" && *after != "" {
		params.Set("after", *after)
	}
	u.RawQuery = params.Encode()
	data, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(data))
	if e != nil {
		return 2
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	if *key != "" {
		req.Header.Set("Idempotency-Key", *key)
	}
	client := &http.Client{Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("redirects disabled") }}
	res, e := client.Do(req)
	if e != nil {
		fmt.Fprintln(os.Stderr, "Request failed or exceeded its deadline; delivery may be unknown. Inspect availability before retrying preparation.")
		return 1
	}
	defer res.Body.Close()
	data, e = io.ReadAll(io.LimitReader(res.Body, (3<<20)+1))
	if e != nil || len(data) > 3<<20 || !json.Valid(data) {
		fmt.Fprintln(os.Stderr, "Invalid or oversized response")
		return 1
	}
	os.Stdout.Write(data)
	if res.StatusCode == 202 {
		return 3
	}
	if res.StatusCode == 409 && bytes.Contains(data, []byte("INDEX_NOT_READY")) {
		return 3
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return 1
	}
	return 0
}
