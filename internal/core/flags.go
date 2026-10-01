package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

const eventFlagDismissed = "flag.dismissed"

// flagDismissedData is the payload of a flag.dismissed Event.
type flagDismissedData struct {
	Flag string `json:"flag"`
	Kind string `json:"kind"`
}

// FlagDismissal is the result of DismissFlag.
type FlagDismissal struct {
	Topic string `json:"topic"`
	Flag  Flag   `json:"flag"`
	// Changed is false when the flag was already dismissed, so nothing was
	// recorded.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

var flagIDPattern = regexp.MustCompile(`^[0-9a-f]{10}$`)

// DismissFlag records that the learner has seen a flag and accepted it, so
// status stops showing it. It records the decision and never changes
// content: a conflict stays as it is in the files. Only flags that do not go
// away by themselves can be dismissed.
func (c *Core) DismissFlag(ctx context.Context, topicID, flagID string, dryRun bool) (FlagDismissal, error) {
	if !flagIDPattern.MatchString(flagID) {
		return FlagDismissal{}, invalidf("%q is not a flag id: study status lists each flag with its id", flagID)
	}
	result := FlagDismissal{Topic: topicID, DryRun: dryRun}
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		if kind, ok := s.dismissed[flagID]; ok {
			result.Flag = Flag{ID: flagID, Kind: kind, Message: "this flag was already dismissed"}
			return nil, nil
		}
		for _, f := range c.topicFlags(view.root, s) {
			if f.ID != flagID {
				continue
			}
			if !dismissible(f.Kind) {
				return nil, &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf(
					"a flag of kind %s cannot be dismissed: it goes away by itself once Lamplight next records %s, "+
						"or once the content is restored", f.Kind, orTheTopic(f.Item))}
			}
			result.Flag = f
			return &change{Type: eventFlagDismissed, Data: flagDismissedData{Flag: f.ID, Kind: f.Kind}}, nil
		}
		return nil, &Error{Code: CodeNotFound,
			Message: fmt.Sprintf("Topic %s has no flag %s: study status lists its flags", topicID, flagID)}
	}, dryRun)
	if err != nil {
		return FlagDismissal{}, err
	}
	result.Changed = ev != nil
	return result, nil
}

func orTheTopic(item string) string {
	if item == "" {
		return "the Topic"
	}
	return item
}

func replayFlagDismissed(s *replayed, ev event) error {
	var d flagDismissedData
	if err := json.Unmarshal(ev.Data, &d); err != nil || d.Flag == "" {
		return errors.New("its payload names no flag")
	}
	s.dismissed[d.Flag] = d.Kind
	return nil
}
