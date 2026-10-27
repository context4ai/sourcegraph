package service

import (
	"github.com/context4ai/sourcegraph/internal/control"
	"time"
)

// UseGitTimeoutDefault is wired before background workers and HTTP start.
func (s *Service) UseGitTimeoutDefault(read func() int) { s.gitTimeoutDefault = read }

func (s *Service) gitTimeoutMinutes(r control.Repository) int {
	if r.GitTimeoutMinutes != 0 && control.ValidateGitTimeout(r.GitTimeoutMinutes) == nil {
		return r.GitTimeoutMinutes
	}
	if s.gitTimeoutDefault != nil {
		if n := s.gitTimeoutDefault(); n != 0 && control.ValidateGitTimeout(n) == nil {
			return n
		}
	}
	return 15
}

func gitJobBudget(minutes int, kind string) time.Duration {
	budget := time.Duration(minutes) * time.Minute
	if budget < 45*time.Minute {
		budget = 45 * time.Minute
	}
	if kind == "prepare" {
		budget *= 16
	}
	return budget
}
