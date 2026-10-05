package sso

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// UserInfo is supplementary profile data, never an independent login identity.
// Its subject must match the verified ID token before any department is used.
func (h *Handler) userInfoDepartment(ctx context.Context, accessToken, subject string) string {
	if h.userInfoURL == "" || accessToken == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.userInfoURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := h.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return ""
	}
	var claims struct {
		Subject    string          `json:"sub"`
		Department json.RawMessage `json:"department"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Subject != subject || subject == "" {
		return ""
	}
	return verifiedDepartment(claims.Department)
}
