package dashboard

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRequestTitle(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"  ## Build\n a browser \t for agents ", "Build a browser for agents"},
		{"# Files pasted by the user:\nfile.txt\n## My request:\nFix the session list", "Fix the session list"},
		{"<environment_context>private context</environment_context>", ""},
		{"# AGENTS.md instructions for project\nmetadata", ""},
		{"<INSTRUCTIONS>bootstrap</INSTRUCTIONS>", ""},
		{" \n\t", ""},
		{strings.Repeat("界", 121), strings.Repeat("界", 119) + "…"},
	} {
		got := requestTitle(tc.input)
		if got != tc.want || !utf8.ValidString(got) {
			t.Fatalf("requestTitle(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestInventoryTitleCaptureAndReplacement(t *testing.T) {
	for _, capture := range []bool{true, false} {
		s := NewStore(capture)
		e := sample("conversation")
		e.Content = "<environment_context>bootstrap</environment_context>"
		s.Apply(e)
		e.Content, e.Inherited = "Inherited request", true
		s.Apply(e)
		e.Content, e.Inherited, e.ContentCaptured = "Uncaptured request", false, false
		s.Apply(e)
		e.Content, e.Kind, e.ContentCaptured = "Assistant reply", "message.assistant", true
		s.Apply(e)
		if s.Snapshot().Sessions[0].Title != "" {
			t.Fatal("used context, inherited, uncaptured, or assistant content as title")
		}
		e.Kind, e.Content = "message.user", "The real request"
		s.Apply(e)
		e.Content = "A later request"
		s.Apply(e)
		want := ""
		if capture {
			want = "The real request"
		}
		if s.Snapshot().Sessions[0].Title != want {
			t.Fatal("title changed or content opt-in was ignored")
		}
		e.Source.Generation++
		e.Kind, e.Content = "source.ready", ""
		s.Apply(e)
		if s.Snapshot().Sessions[0].Title != "" {
			t.Fatal("source replacement retained the previous title")
		}
	}
}
