package dashboard

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shbhmrzd/agentview/internal/observe"
)

func TestAgentOverviewPrivacyBoundariesAndReplacement(t *testing.T) {
	for _, capture := range []bool{true, false} {
		s := NewStore(capture)
		e := sample("child")
		e.ObservationPhase = "history"
		e.Inherited, e.Content = true, "inherited request"
		s.Apply(e)
		e.Inherited, e.Content = false, "actual request"
		s.Apply(e)
		e.Content = "later request"
		s.Apply(e)
		e.Kind, e.Content = "message.assistant", "early reply"
		s.Apply(e)
		e.Content = strings.Repeat("界", 1000)
		s.Apply(e)
		id := sourceID("codex", "child")
		tree, ok := s.AgentTree(id)
		if !ok || len(tree.Sessions) != 1 {
			t.Fatal("missing tree")
		}
		overview := tree.Sessions[0].Overview
		if capture {
			if overview.FirstRequest.Content != "actual request" || !overview.LatestReply.Clipped || len(overview.LatestReply.Content) > maxAgentPreviewBytes || !utf8.ValidString(overview.LatestReply.Content) {
				t.Fatalf("incorrect excerpts: %+v", overview)
			}
			overview.FirstRequest.Content = "external mutation"
			again, _ := s.AgentTree(id)
			if again.Sessions[0].Overview.FirstRequest.Content != "actual request" {
				t.Fatal("caller mutated the store")
			}
		} else if overview.FirstRequest != nil || overview.LatestReply != nil {
			t.Fatal("metadata-only tree leaked content")
		}
		inventory, _ := json.Marshal(s.Snapshot())
		if strings.Contains(string(inventory), "actual request") != capture || strings.Contains(string(inventory), "first_request") || strings.Contains(string(inventory), "later request") {
			t.Fatal("inventory must contain only an opt-in title, not full excerpts")
		}
		e.Source.Generation++
		e.Kind, e.Content = "source.ready", ""
		s.Apply(e)
		tree, _ = s.AgentTree(id)
		if tree.Sessions[0].Overview.FirstRequest != nil || tree.Sessions[0].Overview.LatestReply != nil {
			t.Fatal("replacement retained old excerpts")
		}
	}
}

func TestAgentTreeFamilyIsolationAndBounds(t *testing.T) {
	s := NewStore(true)
	add := func(path, agent, parent, group string) {
		e := sample(path)
		e.Agent, e.GroupSessionID = agent, group
		e.AgentContext = observe.AgentContext{ParentThreadID: parent}
		s.Apply(e)
	}
	add("root", "codex", "", "group")
	add("child", "codex", "root", "")
	add("grandchild", "codex", "child", "")
	add("related", "codex", "missing", "group")
	add("unrelated", "codex", "", "different")
	add("other-provider", "claude", "root", "group")
	tree, _ := s.AgentTree(sourceID("codex", "child"))
	if len(tree.Sessions) != 4 || tree.Limited {
		t.Fatalf("family scope: %+v", tree)
	}
	if _, ok := s.AgentTree("unknown"); ok {
		t.Fatal("unknown source accepted")
	}
	for i := 0; i < maxTreeAgents+5; i++ {
		add(fmt.Sprintf("many-%d", i), "codex", "root", "")
	}
	tree, _ = s.AgentTree(sourceID("codex", "root"))
	if len(tree.Sessions) != maxTreeAgents || !tree.Limited {
		t.Fatal("tree response is not bounded")
	}
	add("root", "codex", "grandchild", "group")
	if _, ok := s.AgentTree(sourceID("codex", "root")); !ok {
		t.Fatal("cycle prevented inspection")
	}
}

func TestAgentTreeIncludesOrphanSiblingsAndDuplicateSources(t *testing.T) {
	s := NewStore(false)
	for _, spec := range []struct{ path, native, group, parent string }{
		{"first", "child", "", "missing-root"},
		{"sibling", "sibling", "", "missing-root"},
		{"copy", "child", "", ""},
		{"group-only", "member", "missing-root", ""},
		{"unrelated", "other", "", ""},
	} {
		e := sample(spec.path)
		e.SessionID, e.GroupSessionID = spec.native, spec.group
		e.AgentContext.ParentThreadID = spec.parent
		s.Apply(e)
	}
	for _, path := range []string{"first", "sibling", "copy", "group-only"} {
		tree, ok := s.AgentTree(sourceID("codex", path))
		if !ok || tree.Limited || len(tree.Sessions) != 4 {
			t.Fatalf("asymmetric or incomplete family from %s: %+v", path, tree)
		}
	}
}
