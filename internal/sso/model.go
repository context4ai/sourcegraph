package sso

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"time"
)

var ErrNotFound = errors.New("authentication record unavailable")

type Config struct {
	Enabled             bool     `json:"enabled"`
	AllowAllUsersManage bool     `json:"allow_all_users_manage"` // Deprecated: parsed only, never grants a role.
	Origin              string   `json:"origin"`
	ClientID            string   `json:"client_id"`
	SigningAlgorithm    string   `json:"signing_algorithm"`
	ReaderSubjects      []string `json:"reader_subjects"` // Deprecated: all enabled SSO users can read.
	AdminSubjects       []string `json:"admin_subjects"`  // One-time seed only when the role authority is absent.
}

func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	u, e := url.Parse(c.Origin)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" {
		return errors.New("SSO origin must be an explicit origin without path or credentials")
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return errors.New("SSO origin must use HTTPS (except loopback development)")
	}
	if c.ClientID == "" || (c.SigningAlgorithm != "RS256" && c.SigningAlgorithm != "HS256" && c.SigningAlgorithm != "auto") {
		return errors.New("SSO requires client_id and RS256, HS256 or auto signing_algorithm")
	}
	for _, subjects := range [][]string{c.ReaderSubjects, c.AdminSubjects} {
		for _, sub := range subjects {
			if sub == "" || sub == "*" {
				return errors.New("SSO subject lists require explicit non-empty subjects")
			}
		}
	}
	return nil
}

type User struct {
	ID         string    `bson:"_id" json:"id"`
	Issuer     string    `bson:"issuer" json:"issuer"`
	Subject    string    `bson:"subject" json:"subject"`
	Name       string    `bson:"name" json:"name"`
	Email      string    `bson:"email" json:"email,omitempty"`
	Picture    string    `bson:"picture" json:"picture,omitempty"`
	Department string    `bson:"department" json:"department"`
	Role       string    `bson:"-" json:"role"`
	Enabled    bool      `bson:"enabled" json:"-"`
	Created    time.Time `bson:"created_at" json:"created_at"`
	LastLogin  time.Time `bson:"last_login_at" json:"last_login_at"`
}
type Session struct {
	ID      string    `bson:"_id"`
	UserID  string    `bson:"user_id"`
	CSRF    string    `bson:"csrf"`
	Expires time.Time `bson:"expires_at"`
}
type Flow struct {
	Provider string    `bson:"provider"`
	ID       string    `bson:"_id"`
	Binding  string    `bson:"binding"`
	Nonce    string    `bson:"nonce"`
	ReturnTo string    `bson:"return_to"`
	Expires  time.Time `bson:"expires_at"`
}
type Store interface {
	AccessStore
	PutFlow(context.Context, Flow) error
	ConsumeFlow(context.Context, string, string, time.Time) (Flow, error)
	UpsertUser(context.Context, User) (User, error)
	GetUser(context.Context, string) (User, error)
	PutSession(context.Context, Session) error
	GetSession(context.Context, string) (Session, error)
	DeleteSession(context.Context, string) error
}

func digest(v string) string { b := sha256.Sum256([]byte(v)); return hex.EncodeToString(b[:]) }
