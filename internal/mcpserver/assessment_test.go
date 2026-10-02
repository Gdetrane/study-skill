package mcpserver_test

import (
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

func TestAssessmentsLevelHintsAndSignals(t *testing.T) {
	session := connect(t, t.TempDir())
	call(t, session, "topic_create", map[string]any{"title": "C", "id": "c"})

	var placed core.AssessmentRecorded
	decode(t, call(t, session, "assessment_record", map[string]any{"topic": "c", "kind": "placement",
		"summary": "Knows variables; pointers need work", "minutes": 12, "level": "beginner",
		"items": []any{
			map[string]any{"area": "variables", "outcome": "correct"},
			map[string]any{"area": "pointers", "outcome": "incorrect", "note": "confused * and &"},
			map[string]any{"area": "goroutines", "outcome": "not_reached"},
		}}), &placed)
	a := placed.Assessment
	if !placed.Changed || placed.Level == nil || placed.Level.Level != "beginner" ||
		strings.Join(a.Weak, ",") != "pointers" || strings.Join(a.Confirm, ",") != "goroutines" {
		t.Fatalf("assessment_record = %+v", placed)
	}

	var status core.Status
	decode(t, call(t, session, "status", map[string]any{}), &status)
	if l := status.Topics[0].Level; l == nil || l.Level != "beginner" || l.Source != core.LevelFromAssessment {
		t.Errorf("status Level = %+v", l)
	}
	var updated core.TopicUpdate
	decode(t, call(t, session, "topic_update", map[string]any{"topic": "c", "level": "advanced"}), &updated)
	if l := updated.Topic.Level; !updated.Changed || l == nil || l.Level != "advanced" || l.Source != core.LevelFromLearner {
		t.Errorf("topic_update level = %+v", updated)
	}
	if msg := toolError(t, session, "topic_update", map[string]any{"topic": "c", "level": "guru"}); !strings.Contains(msg, "invalid_argument") {
		t.Errorf("an unknown Level: %s", msg)
	}

	var first core.RevisionProposal
	decode(t, call(t, session, "revision_propose", map[string]any{"topic": "c", "summary": "A first Syllabus",
		"syllabus": firstSyllabus}), &first)
	call(t, session, "revision_apply", map[string]any{"topic": "c", "revision": first.Revision, "learner_said": "yes"})
	var hint core.HintRecorded
	args := map[string]any{"topic": "c", "lesson": "answer", "kind": "explanation", "request": "h1"}
	decode(t, call(t, session, "hint_record", args), &hint)
	var retry core.HintRecorded
	decode(t, call(t, session, "hint_record", args), &retry)
	if !hint.Changed || retry.Changed || retry.Hint.ID != hint.Hint.ID {
		t.Errorf("hint_record = %+v, then %+v", hint, retry)
	}

	var sig core.Signals
	decode(t, call(t, session, "signals", map[string]any{"topic": "c"}), &sig)
	if len(sig.Assessments) != 1 || sig.Totals.Hints != 1 || sig.Level == nil || sig.Level.Level != "advanced" {
		t.Errorf("signals = %+v", sig)
	}
	if msg := toolError(t, session, "assessment_record", map[string]any{"topic": "c", "kind": "milestone",
		"milestone": "later", "summary": "s", "items": []any{map[string]any{"area": "a", "outcome": "correct"}}}); !strings.Contains(msg, "not_found") {
		t.Errorf("an unknown Milestone: %s", msg)
	}
}
