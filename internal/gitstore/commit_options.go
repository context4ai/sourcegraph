package gitstore

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
)

// CommitOption is display metadata from an immutable commit, never executable text.
type CommitOption struct {
	Commit        string    `json:"commit"`
	Subject       string    `json:"subject"`
	CommitterTime time.Time `json:"committer_time"`
}

// RecentCommits follows first-parent history, matching the service retention policy.
func (s *Store) RecentCommits(ctx context.Context, repo, head string, count int) ([]CommitOption, error) {
	if count < 1 || count > 20 {
		return nil, contract.Fail("INVALID_LIMIT", "Choose at most 20 recent commits.", 400)
	}
	p, err := s.fixed(ctx, repo, head)
	if err != nil {
		return nil, err
	}
	data, err := s.run(ctx, p, 1<<20, "log", "--first-parent", "--format=%H%x00%ct%x00%s", "--max-count="+strconv.Itoa(count), head, "--")
	if err != nil {
		return nil, err
	}
	result := []CommitOption{}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		fields := strings.SplitN(line, "\x00", 3)
		if len(fields) != 3 || !fullSHA.MatchString(fields[0]) {
			return nil, contract.Fail("INVALID_GIT_RESULT", "Invalid commit metadata.", 500)
		}
		seconds, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, err
		}
		result = append(result, CommitOption{fields[0], fields[2], time.Unix(seconds, 0).UTC()})
	}
	return result, nil
}

func (s *Store) IsAncestor(ctx context.Context, repo, commit, head string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	p, err := s.fixed(ctx, repo, commit)
	if err != nil {
		return false, err
	}
	if !fullSHA.MatchString(head) {
		return false, contract.Fail("INVALID_REVISION", "Use a full SHA.", 400)
	}
	cmd := s.command(ctx, p, "merge-base", "--is-ancestor", commit, head)
	err = cmd.Run()
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err == nil {
		return true, nil
	}
	if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1 {
		return false, nil
	}
	return false, contract.Fail("GIT_OPERATION_FAILED", "Cannot determine branch ancestry.", 503)
}
