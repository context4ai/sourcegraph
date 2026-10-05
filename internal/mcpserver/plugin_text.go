package mcpserver

import (
	"fmt"
	"github.com/context4ai/sourcegraph/internal/service"
	"strings"
)

func appendPluginText(blocks []string, enhanced service.EnhancedRead) []string {
	// Only host-assigned IDs determine proximity. Never inspect business fields.
	count := len(blocks)
	for _, attachment := range enhanced.Attachments {
		if attachment.ItemID == "" {
			blocks = append(blocks, attachment.Text)
			continue
		}
		for i := 0; i < count; i++ {
			if attachment.ItemID == fmt.Sprintf("read-%d", i) {
				blocks[i] = strings.TrimRight(blocks[i], "\n") + "\n\n" + attachment.Text
				break
			}
		}
	}
	if len(enhanced.Issues) > 0 {
		var b strings.Builder
		b.WriteString("Plugin diagnostics (original read retained):\n")
		for i, issue := range enhanced.Issues {
			if b.Len()+len(issue.Code)+len(issue.Message) > 16384 {
				fmt.Fprintf(&b, "Additional diagnostics omitted: %d\n", len(enhanced.Issues)-i)
				break
			}
			fmt.Fprintf(&b, "%s: %s\n", issue.Code, issue.Message)
		}
		blocks = append(blocks, b.String())
	}
	return blocks
}
