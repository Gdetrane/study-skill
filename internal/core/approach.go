package core

import (
	"encoding/json"
	"fmt"
)

// The Approach: how a Topic's Lessons relate to each other. It lives in
// topic.toml as approach, set by an approach.set Event through
// topic_update, and is chosen with the learner when the Topic is created.

const eventApproachSet = "approach.set"

// Approaches.
const (
	// ApproachConcepts: standalone concepts, each with its own exercises.
	ApproachConcepts = "concepts"
	// ApproachProject: one project built step by step, Lesson by Lesson.
	ApproachProject = "project"
	// ApproachChallenges: a run of challenges of growing difficulty.
	ApproachChallenges = "challenges"
)

func init() {
	eventKinds[eventApproachSet] = eventKind{apply: applyApproachSet, replay: replayApproachSet}
}

// approachSetData is the payload of an approach.set Event.
type approachSetData struct {
	Approach string `json:"approach"`
}

// checkApproach validates an Approach.
func checkApproach(approach string) (string, error) {
	switch approach {
	case ApproachConcepts, ApproachProject, ApproachChallenges:
		return approach, nil
	}
	return "", invalidf("an Approach is concepts, project or challenges, not %q", clip(approach, 40))
}

// approachOf reads the Approach from topic.toml's settings: empty when
// unset, and a problem when the value is not an Approach.
func approachOf(settings topicSettings) (approach, problem string) {
	v, ok := settings.extra["approach"]
	if !ok {
		return "", ""
	}
	if s, ok := v.(string); ok {
		if _, err := checkApproach(s); err == nil {
			return s, ""
		}
		return "", fmt.Sprintf("approach in %s must be concepts, project or challenges, not %q", topicFile, clip(s, 40))
	}
	return "", fmt.Sprintf("approach in %s must be text: concepts, project or challenges", topicFile)
}

// addApproach fills a Topic's Approach for status, and reports one a hand
// edit broke.
func addApproach(topic *Topic, settings topicSettings) {
	approach, problem := approachOf(settings)
	topic.Approach = approach
	if problem != "" {
		topic.SettingsProblems = append(topic.SettingsProblems, problem)
	}
}

// planApproach plans setting the Approach. One present in topic.toml but
// unreadable is rewritten even when it reads as the one asked for.
func planApproach(topicID, approach string) plan {
	return func(_ *replayed, view *topicView) (*change, error) {
		settings, err := viewSettings(view, topicID)
		if err != nil {
			return nil, err
		}
		if current, problem := approachOf(settings); current == approach && problem == "" {
			return nil, nil
		}
		return &change{Type: eventApproachSet, Data: approachSetData{Approach: approach}, Items: []string{topicFile}}, nil
	}
}

func applyApproachSet(ev event, item string, current []byte, exists bool) ([]byte, bool, error) {
	var d approachSetData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, unreadable(ev)
	}
	return editSettings(ev, item, current, exists, func(extra map[string]any) error {
		extra["approach"] = d.Approach
		return nil
	})
}

func replayApproachSet(_ *replayed, ev event) error {
	var d approachSetData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	_, err := checkApproach(d.Approach)
	return err
}
