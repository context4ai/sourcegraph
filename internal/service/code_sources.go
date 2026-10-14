package service

import (
	"context"
	"log/slog"

	"github.com/context4ai/sourcegraph/internal/codesources"
	"github.com/context4ai/sourcegraph/internal/control"
)

func (s *Service) eachRepository(ctx context.Context, visit func(control.Repository) error) error {
	after := ""
	for {
		page, err := s.DB.List(ctx, after, 500)
		if err != nil {
			return err
		}
		for _, r := range page {
			if err = visit(r); err != nil {
				return err
			}
		}
		if len(page) < 500 {
			return nil
		}
		after = page[len(page)-1].ID
	}
}

func sourceID(r control.Repository) string {
	provider := r.CodeProvider
	if provider == "" {
		provider = defaultCodeProvider
	}
	if source, ok := codesources.Resolve(provider); ok {
		return source.ID
	}
	return provider
}

// PinCodeHosts gives repositories registered before hosts were recorded their
// current host, so later configuration changes cannot move them. Repositories
// whose pinned host differs from the configured one keep the pinned host.
func (s *Service) PinCodeHosts(ctx context.Context, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	err := s.eachRepository(ctx, func(r control.Repository) error {
		provider := r.CodeProvider
		if provider == "" {
			provider = defaultCodeProvider
		}
		source, _ := codesources.Resolve(provider)
		if r.CodeHost == "" && source.Host != "" {
			s.mu.Lock()
			_, err := control.Mutate(ctx, s.DB, r.Name, func(v *control.Repository) error {
				if v.CodeHost == "" {
					v.CodeHost = source.Host
				}
				return nil
			})
			s.mu.Unlock()
			if err != nil && ctx.Err() == nil {
				log.Warn("cannot record repository code host", "repo", r.Name, "error", err.Error())
			}
		} else if r.CodeHost != "" && r.CodeHost != source.Host {
			log.Warn("code source host changed; repository keeps its registered host", "repo", r.Name, "registered", r.CodeHost, "configured", source.Host)
		}
		return ctx.Err()
	})
	if err != nil && ctx.Err() == nil {
		log.Warn("cannot record repository code hosts", "error", err.Error())
	}
}

// CodeSourceInUse reports whether any repository record, deleted or not, uses the source.
func (s *Service) CodeSourceInUse(ctx context.Context, id string) (bool, error) {
	used := false
	err := s.eachRepository(ctx, func(r control.Repository) error {
		if sourceID(r) == id {
			used = true
		}
		return nil
	})
	return used, err
}
