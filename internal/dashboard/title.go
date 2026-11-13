package dashboard

import "strings"

// A short, literal request excerpt, not an AI-generated title. This is only
// called in content-enabled mode. Skip known bootstrap envelopes so they do
// not obscure the user's first request in the inventory.
func requestTitle(content string) string {
	text := strings.TrimSpace(content)
	for _, prefix := range []string{"<environment_context>", "<permissions instructions>", "<user_instructions>", "# AGENTS.md instructions", "<INSTRUCTIONS>"} {
		if strings.HasPrefix(text, prefix) {
			return ""
		}
	}
	if _, request, ok := strings.Cut(text, "## My request:"); ok {
		text = request
	}
	text = strings.Join(strings.Fields(text), " ")
	text = strings.TrimLeft(text, "#>*- ")
	runes := []rune(text)
	if len(runes) > 120 {
		text = string(runes[:119]) + "…"
	}
	return text
}
