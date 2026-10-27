// Package sitesettings owns non-secret, runtime website configuration.
// HTTP authorization belongs to the host; settings never grant management rights.
package sitesettings

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/context4ai/sourcegraph/internal/codesources"
	"github.com/context4ai/sourcegraph/internal/contract"
)

const (
	RepositoryDefaultsName = "repository_defaults"
	AccessName             = "access"
	CodeSourceName         = "code_source"
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
	// AllowUserRepositories lets any signed-in user register repositories and
	// manage those they create. Absent in older records, which keeps it off.
	AllowUserRepositories bool `json:"allow_user_repositories"`
}

// CodeSource holds website values for the editable code source; empty fields inherit defaults.
type CodeSource struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Username string `json:"username"`
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
type CodeSourceSection struct {
	Metadata
	Values CodeSource `json:"values"`
}
type Snapshot struct {
	RepositoryDefaults RepositorySection `json:"repository_defaults"`
	Access             AccessSection     `json:"access"`
	CodeSource         CodeSourceSection `json:"code_source"`
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
	return section == RepositoryDefaultsName || section == AccessName || section == CodeSourceName
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
			PublicRead            *bool  `json:"public_read"`
			AllowRegistration     bool   `json:"allow_registration"`
			RegistrationNoticeZH  string `json:"registration_notice_zh"`
			RegistrationNoticeEN  string `json:"registration_notice_en"`
			AllowUserRepositories bool   `json:"allow_user_repositories"`
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
		return map[string]any{"public_read": *input.PublicRead, "allow_registration": input.AllowRegistration, "registration_notice_zh": input.RegistrationNoticeZH, "registration_notice_en": input.RegistrationNoticeEN, "allow_user_repositories": input.AllowUserRepositories}, nil
	case CodeSourceName:
		var v CodeSource
		if contract.DecodeObject(data, &v) != nil {
			return nil, contract.Fail("INVALID_SETTINGS", "Provide name, host and username as strings.", 400)
		}
		if err := codesources.ValidateSettings(codesources.Override{Name: v.Name, Host: v.Host, Username: v.Username}); err != nil {
			return nil, err
		}
		host, _ := codesources.NormalizeHost(v.Host)
		return map[string]any{"name": v.Name, "host": host, "username": v.Username}, nil
	default:
		return nil, contract.Fail("SETTINGS_SECTION_NOT_FOUND", "Unknown settings section.", 404)
	}
}

func assign(out *Snapshot, section string, meta Metadata, values map[string]any) {
	switch section {
	case RepositoryDefaultsName:
		out.RepositoryDefaults = RepositorySection{Metadata: meta, Values: RepositoryDefaults{values["retention_mode"].(string), values["retention_days"].(int), values["retention_count"].(int)}}
	case AccessName:
		out.Access = AccessSection{Metadata: meta, Values: Access{PublicRead: values["public_read"].(bool), AllowRegistration: values["allow_registration"].(bool), RegistrationNoticeZH: values["registration_notice_zh"].(string), RegistrationNoticeEN: values["registration_notice_en"].(string), AllowUserRepositories: values["allow_user_repositories"].(bool)}}
	case CodeSourceName:
		out.CodeSource = CodeSourceSection{Metadata: meta, Values: CodeSource{values["name"].(string), values["host"].(string), values["username"].(string)}}
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
		assign(&out, r.Name, Metadata{r.Revision, r.UpdatedAt, r.UpdatedBy}, values)
	}
	if len(seen) != 3 {
		return out, errors.New("site settings sections are incomplete")
	}
	return out, nil
}

const DefaultNoticeZH = "当前站点为演示，关闭其他用户注册，请访问 GitHub 项目了解更多。"
const DefaultNoticeEN = "This site is a demo. Registration is closed to other users. Visit the GitHub project to learn more."
