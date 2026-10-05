package gitstore

import (
	"context"

	"strings"

	"github.com/context4ai/sourcegraph/internal/contract"
)

// Inspect only advertised refs; default selection never downloads file objects.
func (s *Store) discoverDefaultBranch(ctx context.Context, dir, remote string, options FetchOptions) (string, error) {
	args := append(fetchConfiguration(options), "ls-remote", "--symref", remote, "HEAD", "refs/heads/master", "refs/heads/main")
	command := s.command(ctx, dir, args...)
	command.Env = append(command.Env, "SOURCEGRAPH_GIT_TOKEN="+options.Token)
	output := &limitWriter{limit: 64 << 10}
	diagnostic := &prefixTrace{limit: 64 << 10}
	command.Stdout, command.Stderr = output, diagnostic
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", remoteFailure(diagnostic.String(), "GIT_FETCH_FAILED", "Cannot inspect the remote default branch.")
	}
	return parseDefaultBranch(output.String())
}

func parseDefaultBranch(advertisement string) (string, error) {
	head := ""
	available := map[string]bool{}
	for _, row := range strings.Split(advertisement, "\n") {
		fields := strings.Fields(row)
		if len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			head = strings.TrimPrefix(fields[1], "refs/heads/")
		}
		if len(fields) == 2 && fullSHA.MatchString(fields[0]) {
			name := strings.TrimPrefix(fields[1], "refs/heads/")
			if name == "main" || name == "master" {
				available[name] = true
			}
		}
	}
	if available[head] {
		return head, nil
	}
	if len(available) == 1 {
		for name := range available {
			return name, nil
		}
	}
	return "", contract.Fail("DEFAULT_BRANCH_UNDETERMINED", "Cannot uniquely identify master/main. Uncheck the default branch and select an explicit branch.", 409)
}
