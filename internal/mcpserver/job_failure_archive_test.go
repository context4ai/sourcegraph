package mcpserver

import (
	"github.com/context4ai/sourcegraph/internal/control"
	"testing"
	"time"
)

func TestArchivedFailureIsNotReportedAsLiveTask(t *testing.T) {
	r := control.Repository{Name: "org/repo", JobFailures: []control.JobFailure{{ID: "old", Code: "FETCH_FAILED", Time: time.Now()}}}
	out := compactAvailability(r, "old")
	if _, ok := out["job_failure"]; !ok {
		t.Fatal("archived diagnosis unavailable")
	}
	if out["job_lookup"].(map[string]string)["state"] != "archived_failure" {
		t.Fatal("archive mistaken for live task")
	}
	if _, ok := compactAvailability(r, "")["job_failure"]; ok {
		t.Fatal("default availability expanded with archive")
	}
	if compactAvailability(r, "unknown")["job_lookup"].(map[string]string)["state"] != "not_found" {
		t.Fatal("missing task mistaken for success")
	}
}
