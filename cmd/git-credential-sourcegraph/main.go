// git-credential-sourcegraph supplies a host-scoped token over Git's private pipe.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] != "get" {
		return
	}
	s := bufio.NewScanner(io.LimitReader(os.Stdin, 8193))
	fields := map[string]string{}
	for s.Scan() {
		if s.Text() == "" {
			break
		}
		k, v, ok := strings.Cut(s.Text(), "=")
		if !ok {
			return
		}
		fields[k] = v
	}
	host := os.Getenv("SOURCEGRAPH_GIT_HOST")
	if s.Err() != nil || fields["protocol"] != "https" || host == "" || fields["host"] != host {
		return
	}
	token := os.Getenv("SOURCEGRAPH_GIT_TOKEN")
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return
	}
	username := os.Getenv("SOURCEGRAPH_GIT_USERNAME")
	if username == "" || strings.ContainsAny(username, "\r\n\x00") {
		username = "oauth2"
	}
	fmt.Printf("username=%s\npassword=%s\n\n", username, token)
}
