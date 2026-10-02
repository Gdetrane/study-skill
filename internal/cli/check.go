package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// checkCommand is study check: the only way to run a Lesson's Check. It runs
// commands the agent wrote, so the agent runs it from its own shell, where
// its sandbox applies; the MCP server never runs a Check (ADR-0009).
func (a *app) checkCommand() *cobra.Command {
	var topic string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "check <lesson>",
		Short: "Run a Lesson's Check on the current work and record the Attempt",
		Long: "Run each criterion of a Lesson's Check, from the YAML header of lessons/<lesson>.md, in the Lesson's\n" +
			"practice folder, and record the Attempt in the Topic's History. A criterion passes when its command exits\n" +
			"with 0. The exit code is 0 whenever the Attempt was recorded; its outcome is in the output.",
		Example: `  study check pointers --topic c
  study check pointers --json        # inside the Topic's folder`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			if topic == "" {
				if topic, err = topicFromFolder(cmd, c); err != nil {
					return a.fail(err)
				}
			}
			attempt, err := c.RunCheck(cmd.Context(), topic, args[0], core.CheckOptions{Timeout: timeout})
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: attempt})
			}
			return writeAttempt(a.out, attempt)
		},
	}
	cmd.Flags().StringVar(&topic, "topic", "", "the Topic's id; defaults to the Topic whose folder you are in")
	cmd.Flags().DurationVar(&timeout, "timeout", core.DefaultCheckTimeout, "longest each criterion may run")
	return cmd
}

// topicFromFolder names the Topic whose folder the command runs in. Writes
// never fall back to the most recent Topic: outside a Topic's folder, the
// Topic must be named.
func topicFromFolder(cmd *cobra.Command, c *core.Core) (string, error) {
	status, err := c.Status(cmd.Context())
	if err != nil {
		return "", err
	}
	if t := status.ActiveTopic; t != nil && t.ChosenBy == core.ChosenByFolder {
		return t.ID, nil
	}
	return "", usageError{fmt.Errorf("name the Topic with --topic, or run the command inside the Topic's folder")}
}

func writeAttempt(w io.Writer, at core.Attempt) error {
	var b strings.Builder
	style := styleOK
	switch at.Outcome {
	case core.OutcomeFailed:
		style = styleWarn
	case core.OutcomeErrored:
		style = styleFail
	}
	fmt.Fprintf(&b, "Check of %s: %s\n", styleAccent.Render(at.Lesson), style.Render(at.Outcome))
	ids := make([]string, len(at.Criteria))
	for i, c := range at.Criteria {
		ids[i] = c.ID
	}
	width := widest(ids) + 2
	for _, c := range at.Criteria {
		line := c.Outcome
		switch {
		case c.Reason != "":
			line += ": " + c.Reason
		case c.Outcome == core.OutcomeFailed:
			line += fmt.Sprintf(" (exit %d)", c.ExitCode)
		}
		fmt.Fprintf(&b, "  %s%s\n", styleLabel.Render(pad(c.ID, width)), line)
		if c.Outcome != core.OutcomePassed && strings.TrimSpace(c.Output) != "" {
			for _, l := range strings.Split(strings.TrimRight(c.Output, "\n"), "\n") {
				fmt.Fprintf(&b, "    %s\n", styleDim.Render(l))
			}
		}
	}
	if at.Reason != "" {
		fmt.Fprintf(&b, "%s\n", styleWarn.Render(sentence(at.Reason)))
	}
	fmt.Fprintf(&b, "%s\n", styleDim.Render("Recorded as Attempt "+at.ID+"."))
	_, err := io.WriteString(w, b.String())
	return err
}
