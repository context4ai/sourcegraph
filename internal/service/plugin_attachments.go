package service

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"

	"github.com/context4ai/sourcegraph/internal/gitstore"
	"github.com/context4ai/sourcegraph/internal/wasmplugin"
)

// IDs are assigned in original request order, before scope filtering.
type pluginReadFile struct {
	gitstore.File
	ItemID string
}
type PluginAttachment struct {
	ItemID string `json:"item_id,omitempty"`
	Text   string `json:"text"`
}

// Separate from the body budget. Whole attachments are accepted or omitted;
// links and other business text are never clipped halfway through.
const pluginAttachmentBudget = 2 << 20

func (out *EnhancedRead) acceptAttachments(raw json.RawMessage, files []map[string]any) {
	issue := func(message string) {
		out.Issues = append(out.Issues, wasmplugin.Issue{Code: "PLUGIN_PROTOCOL_ERROR", Message: message})
	}
	var envelope map[string]json.RawMessage
	if !utf8.Valid(raw) || json.Unmarshal(raw, &envelope) != nil || envelope == nil {
		issue("Expected an attachments object.")
		return
	}
	data, ok := envelope["attachments"]
	if !ok {
		issue("Missing attachments array (ABI v2 required).")
		return
	}
	var entries []json.RawMessage
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) || json.Unmarshal(data, &entries) != nil {
		issue("attachments must be an array.")
		return
	}
	allowed := map[string]bool{}
	for _, file := range files {
		id, _ := file["item_id"].(string)
		allowed[id] = true
	}
	used := 0
	for _, a := range out.Attachments {
		used += len(a.Text)
	}
	for _, entry := range entries {
		var fields map[string]json.RawMessage
		if json.Unmarshal(entry, &fields) != nil || fields == nil {
			issue("Each attachment must be an object.")
			continue
		}
		var text string
		value, ok := fields["text"]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &text) != nil {
			issue("Attachment text must be a string.")
			continue
		}
		id := ""
		if value, present := fields["item_id"]; present {
			if json.Unmarshal(value, &id) != nil || !allowed[id] || id == "" {
				issue("Attachment item_id does not identify a file in this invocation.")
				continue
			}
		}
		if text == "" {
			continue
		}
		if len(text) > pluginAttachmentBudget-used {
			out.Issues = append(out.Issues, wasmplugin.Issue{Code: "PLUGIN_OUTPUT_TOO_LARGE", Message: "Supplement omitted: attachment budget exceeded; original body retained."})
			continue
		}
		used += len(text)
		out.Attachments = append(out.Attachments, PluginAttachment{ItemID: id, Text: text})
	}
}
