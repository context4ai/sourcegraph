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
	if s.Err() != nil || fields["protocol"] != "https" || fields["host"] != "github.com" {
		return
	}
	token := os.Getenv("SOURCEGRAPH_GIT_TOKEN")
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return
	}
	fmt.Printf("username=oauth2\npassword=%s\n\n", token)
}
