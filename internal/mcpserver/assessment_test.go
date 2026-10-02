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
	placement := map[string]any{"topic": "c", "kind": "placement", "request": "placement-1",
		"summary": "Knows variables; pointers need work", "minutes": 12, "level": "beginner",
		"items": []any{
			map[string]any{"area": "variables", "outcome": "correct"},
			map[string]any{"area": "pointers", "outcome": "incorrect", "note": "confused * and &"},
			map[string]any{"area": "goroutines", "outcome": "not_reached"},
		}}
	decode(t, call(t, session, "assessment_record", placement), &placed)
	a := placed.Assessment
	if !placed.Changed || placed.Level == nil || placed.Level.Level != "beginner" || placed.Next != nil ||
		a.Minutes == nil || *a.Minutes != 12 || strings.Join(a.Weak, ",") != "pointers" ||
		strings.Join(a.Confirm, ",") != "goroutines" {
		t.Fatalf("assessment_record = %+v", placed)
	}
	if msg := toolError(t, session, "assessment_record", map[string]any{"topic": "c", "kind": "placement",
		"summary": "s", "minutes": 16, "items": []any{map[string]any{"area": "a", "outcome": "correct"}}}); !strings.Contains(msg, "time box") {
		t.Errorf("minutes over the time box: %s", msg)
	}

	var status core.Status
	decode(t, call(t, session, "status", map[string]any{}), &status)
	if l := status.Topics[0].Level; l == nil || l.Level != "beginner" || l.Source != core.LevelFromAssessment {
		t.Errorf("status Level = %+v", l)
	}
	var updated core.TopicUpdate
	decode(t, call(t, session, "topic_update", map[string]any{"topic": "c", "level": "advanced", "approach": "project"}), &updated)
	if l := updated.Topic.Level; !updated.Changed || l == nil || l.Level != "advanced" || l.Source != core.LevelFromLearner ||
		updated.Topic.Approach != core.ApproachProject {
		t.Errorf("topic_update level and approach = %+v", updated)
	}
	for field, value := range map[string]string{"level": "guru", "approach": "lectures"} {
		if msg := toolError(t, session, "topic_update", map[string]any{"topic": "c", field: value}); !strings.Contains(msg, "invalid_argument") {
			t.Errorf("an unknown %s: %s", field, msg)
		}
	}
	decode(t, call(t, session, "status", map[string]any{}), &status)
	if status.Topics[0].Approach != core.ApproachProject {
		t.Errorf("status Approach = %q", status.Topics[0].Approach)
	}
	// A retry with the request id records nothing, even after the learner
	// chose a Level.
	var retried core.AssessmentRecorded
	decode(t, call(t, session, "assessment_record", placement), &retried)
	if retried.Changed || retried.Assessment.ID != a.ID || retried.Level.Level != "advanced" {
		t.Errorf("a retried assessment_record = %+v", retried)
	}

	var first core.RevisionProposal
	decode(t, call(t, session, "revision_propose", map[string]any{"topic": "c", "summary": "A first Syllabus",
		"syllabus": firstSyllabus}), &first)
	call(t, session, "revision_apply", map[string]any{"topic": "c", "revision": first.Revision, "learner_said": "yes"})
	var hint core.HintRecorded
	args := map[string]any{"topic": "c", "lesson": "answer", "kind": "explanation", "requested_by": "agent", "request": "h1"}
	decode(t, call(t, session, "hint_record", args), &hint)
	var retry core.HintRecorded
	decode(t, call(t, session, "hint_record", args), &retry)
	if !hint.Changed || retry.Changed || retry.Hint.ID != hint.Hint.ID || hint.Hint.RequestedBy != core.HintByAgent {
		t.Errorf("hint_record = %+v, then %+v", hint, retry)
	}
	if msg := toolError(t, session, "hint_record", map[string]any{"topic": "c", "lesson": "answer",
		"requested_by": "teacher"}); !strings.Contains(msg, "invalid_argument") {
		t.Errorf("an unknown asker: %s", msg)
	}

	var sig core.Signals
	decode(t, call(t, session, "signals", map[string]any{"topic": "c"}), &sig)
	if len(sig.Assessments) != 1 || sig.Totals.Hints != 1 || sig.Totals.HintsRequested != 0 || sig.Level == nil ||
		sig.Level.Level != "advanced" {
		t.Errorf("signals = %+v", sig)
	}
	// Weak results at the end of a Milestone propose a Revision.
	var end core.AssessmentRecorded
	decode(t, call(t, session, "assessment_record", map[string]any{"topic": "c", "kind": "milestone",
		"milestone": "basics", "summary": "Pointers need work",
		"items": []any{map[string]any{"area": "pointers", "outcome": "partly"}}}), &end)
	if end.Next == nil || end.Next.Code != core.NextProposeRevision {
		t.Errorf("next after weak results = %+v", end.Next)
	}
	if msg := toolError(t, session, "assessment_record", map[string]any{"topic": "c", "kind": "milestone",
		"milestone": "later", "summary": "s", "items": []any{map[string]any{"area": "a", "outcome": "correct"}}}); !strings.Contains(msg, "not_found") {
		t.Errorf("an unknown Milestone: %s", msg)
	}
}
