package httpserver

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/service"
)

// Called after the runtime's authentication, authorization and body limits.
func repositoryMaintenance(r *http.Request, s *service.Service, decode func(any) bool, method func(string) bool) (any, error, int) {
	name := r.URL.Query().Get("repo")
	if r.URL.Path == Prefix+"/api/admin/v1/repo/commit-options" {
		if !method("GET") {
			return nil, nil, 200
		}
		result, err := s.CommitOptions(r.Context(), name, r.URL.Query().Get("branch"))
		return result, err, 200
	}
	if !method("POST") {
		return nil, nil, 200
	}
	var q service.IndexSelection
	if r.URL.Path == Prefix+"/api/admin/v1/repo/index/preview" || r.URL.Path == Prefix+"/api/admin/v1/repo/index" {
		if !decode(&q) {
			return nil, nil, 200
		}
	} else {
		var confirmation struct {
			Preview string `json:"preview"`
		}
		if !decode(&confirmation) {
			return nil, nil, 200
		}
		q.Preview = confirmation.Preview
	}
	if r.URL.Path == Prefix+"/api/admin/v1/repo/index/preview" {
		result, err := s.PreviewIndex(r.Context(), name, service.IndexSelection{Branch: q.Branch, Commit: q.Commit})
		return result, err, 200
	}
	action := "cleanup"
	if strings.Contains(r.URL.Path, "/delete") {
		action = "delete"
	}
	if strings.HasSuffix(r.URL.Path, "/preview") {
		result, err := s.PreviewMaintenance(r.Context(), name, action)
		return result, err, 200
	}
	revision, err := strconv.ParseInt(strings.Trim(r.Header.Get("If-Match"), `"`), 10, 64)
	if err != nil {
		return nil, contract.Fail("PRECONDITION_REQUIRED", "If-Match revision is required.", 428), 200
	}
	if r.URL.Path == Prefix+"/api/admin/v1/repo/index" {
		result, err := s.CreateIndex(r.Context(), name, service.IndexSelection{Branch: q.Branch, Commit: q.Commit, Preview: q.Preview}, revision, r.Header.Get("Idempotency-Key"))
		return result, err, 202
	}
	result, err := s.Maintenance(r.Context(), name, action, q.Preview, revision)
	return result, err, 200
}
