package zoektclient

import "strings"

func quoteQ(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }

// QuoteQuery escapes a token for the pinned Zoekt parser, preserving Unicode.
func QuoteQuery(s string) string { return quoteQ(s) }
