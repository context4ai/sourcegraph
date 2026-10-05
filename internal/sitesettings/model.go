// Package sitesettings owns non-secret, runtime website configuration.
// HTTP authorization belongs to the host; settings never grant management rights.
package sitesettings

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
)

const (
	RepositoryDefaultsName = "repository_defaults"
	AccessName             = "access"
	Collection             = "sourcegraph_site_settings"
	// JSON consumers must be able to round-trip revisions exactly.
	maxRevision int64 = 9007199254740991
)

var (
	ErrConflict    = errors.New("site settings revision conflict")
	ErrUnavailable = errors.New("site settings storage unavailable")
)

type RepositoryDefaults struct {
	RetentionMode  string `json:"retention_mode"`
	RetentionDays  int    `json:"retention_days"`
	RetentionCount int    `json:"retention_count"`
}
type Access struct {
	PublicRead           bool   `json:"public_read"`
	AllowRegistration    bool   `json:"allow_registration"`
	RegistrationNoticeZH string `json:"registration_notice_zh"`
	RegistrationNoticeEN string `json:"registration_notice_en"`
}
type Metadata struct {
	Revision  int64     `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}
type RepositorySection struct {
	Metadata
	Values RepositoryDefaults `json:"values"`
}
type AccessSection struct {
	Metadata
	Values Access `json:"values"`
}
type Snapshot struct {
	RepositoryDefaults RepositorySection `json:"repository_defaults"`
	Access             AccessSection     `json:"access"`
}

// Record is one CAS-protected section. Values contain validated JSON scalars only.
// Mongo stores them as an object so operators can inspect the actual settings.
type Record struct {
	Name      string         `json:"-" bson:"_id"`
	Revision  int64          `json:"revision" bson:"revision"`
	Values    map[string]any `json:"values" bson:"values"`
	UpdatedAt time.Time      `json:"updated_at" bson:"updated_at"`
	UpdatedBy string         `json:"updated_by" bson:"updated_by"`
}

type Store interface {
	// Ensure inserts only absent records. Existing settings always win over seeds.
	Ensure(context.Context, Record) error
	Load(context.Context) ([]Record, error)
	Replace(context.Context, Record, int64) error
}

func validSection(section string) bool {
	return section == RepositoryDefaultsName || section == AccessName
}

func decodeValues(section string, data []byte) (map[string]any, error) {
	switch section {
	case RepositoryDefaultsName:
		var v RepositoryDefaults
		if contract.DecodeObject(data, &v) != nil || (v.RetentionMode != "days" && v.RetentionMode != "count") || v.RetentionDays < 1 || v.RetentionDays > 365 || v.RetentionCount < 1 || v.RetentionCount > 4096 {
			return nil, contract.Fail("INVALID_SETTINGS", "Use retention_mode days or count, retention_days 1..365 and retention_count 1..4096; all fields are required.", 400)
		}
		return map[string]any{"retention_mode": v.RetentionMode, "retention_days": v.RetentionDays, "retention_count": v.RetentionCount}, nil
	case AccessName:
		var input struct {
			PublicRead           *bool  `json:"public_read"`
			AllowRegistration    bool   `json:"allow_registration"`
			RegistrationNoticeZH string `json:"registration_notice_zh"`
			RegistrationNoticeEN string `json:"registration_notice_en"`
		}
		if contract.DecodeObject(data, &input) != nil || input.PublicRead == nil || len(input.RegistrationNoticeZH) > 4096 || len(input.RegistrationNoticeEN) > 4096 {
			return nil, contract.Fail("INVALID_SETTINGS", "Provide public_read, allow_registration and notices of at most 4096 bytes.", 400)
		}
		if input.RegistrationNoticeZH == "" {
			input.RegistrationNoticeZH = DefaultNoticeZH
		}
		if input.RegistrationNoticeEN == "" {
			input.RegistrationNoticeEN = DefaultNoticeEN
		}
		return map[string]any{"public_read": *input.PublicRead, "allow_registration": input.AllowRegistration, "registration_notice_zh": input.RegistrationNoticeZH, "registration_notice_en": input.RegistrationNoticeEN}, nil
	default:
		return nil, contract.Fail("SETTINGS_SECTION_NOT_FOUND", "Unknown settings section.", 404)
	}
}

func snapshot(records []Record) (Snapshot, error) {
	var out Snapshot
	seen := map[string]bool{}
	for _, r := range records {
		if !validSection(r.Name) || seen[r.Name] || r.Revision < 1 || r.Revision > maxRevision || r.UpdatedAt.IsZero() || r.UpdatedBy == "" {
			return out, errors.New("invalid site settings record")
		}
		seen[r.Name] = true
		raw, err := json.Marshal(r.Values)
		if err != nil {
			return out, errors.New("invalid site settings values")
		}
		values, err := decodeValues(r.Name, raw)
		if err != nil {
			return out, errors.New("invalid persisted site settings")
		}
		meta := Metadata{r.Revision, r.UpdatedAt, r.UpdatedBy}
		if r.Name == RepositoryDefaultsName {
			out.RepositoryDefaults = RepositorySection{Metadata: meta, Values: RepositoryDefaults{values["retention_mode"].(string), values["retention_days"].(int), values["retention_count"].(int)}}
		} else {
			out.Access = AccessSection{Metadata: meta, Values: Access{PublicRead: values["public_read"].(bool), AllowRegistration: values["allow_registration"].(bool), RegistrationNoticeZH: values["registration_notice_zh"].(string), RegistrationNoticeEN: values["registration_notice_en"].(string)}}
		}
	}
	if len(seen) != 2 {
		return out, errors.New("site settings sections are incomplete")
	}
	return out, nil
}

const DefaultNoticeZH = "当前站点为演示，关闭其他用户注册，请访问 GitHub 项目了解更多。"
const DefaultNoticeEN = "This site is a demo. Registration is closed to other users. Visit the GitHub project to learn more."
