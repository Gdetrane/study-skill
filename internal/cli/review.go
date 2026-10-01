package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// reviewCommand is study review: Reviews in the terminal, without an agent.
// The learner sees each Card's prompt, recalls the answer, reveals it and
// rates their own recall.
func (a *app) reviewCommand() *cobra.Command {
	var q core.DueQuery
	cmd := &cobra.Command{
		Use:   "review [topic]",
		Short: "Review your due Cards in the terminal",
		Long: "Review the Cards due now, in the terminal, without an agent: recall the answer, show it, and rate\n" +
			"how well you recalled it. The session is sized to your Energy and to today's cap on new Cards.\n\n" +
			"Keys: Enter shows the answer; 1 again, 2 hard, 3 good, 4 easy; at a new Card's first Review\n" +
			"k keeps it, e edits it and d drops it; f flags a Card as wrong or unclear; s skips it; q stops.\n" +
			"Without a terminal, each key is read from a line of standard input.\n\n" +
			"For scripts, use study card due and study card review, which take --json.",
		Example: "  study review\n  study review c --energy fumes",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usageError{fmt.Errorf("%q takes at most one Topic", cmd.CommandPath())}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.json {
				return a.fail(usageError{errors.New("study review is interactive: for scripts, use study card due " +
					"and study card review, which take --json")})
			}
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			topic, why, err := a.reviewTopic(cmd.Context(), c, args)
			if err != nil {
				return a.fail(err)
			}
			due, err := c.DueCardsOf(cmd.Context(), topic, q)
			if err != nil {
				return a.fail(err)
			}
			in := newKeyInput(a.stdin)
			defer in.close()
			s := &reviewSession{c: c, topic: topic, in: in, out: a.out}
			if err := s.run(cmd.Context(), due, why); err != nil {
				return a.fail(err)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&q.Limit, "limit", 0, fmt.Sprintf("most Cards to review, up to %d", core.MaxDueLimit))
	cmd.Flags().StringVar(&q.Energy, "energy", "", "full, half or fumes; sizes the session")
	energyCompletion(cmd)
	return cmd
}

// reviewTopic picks the Topic to review: the one named, else the Active
// topic, with the reason it was chosen.
func (a *app) reviewTopic(ctx context.Context, c *core.Core, args []string) (topic, why string, err error) {
	if len(args) == 1 {
		return args[0], "", nil
	}
	status, err := c.Status(ctx)
	if err != nil {
		return "", "", err
	}
	if t := status.ActiveTopic; t != nil {
		return t.ID, t.Reason, nil
	}
	return "", "", usageError{errors.New("name the Topic to review: study review <topic>")}
}

// keyInput reads the learner's answers: single keys in a terminal, and in
// a script one line per key.
type keyInput interface {
	// key returns the next key, lower-cased; '\n' for Enter. io.EOF ends
	// the session.
	key() (rune, error)
	// line reads a line of text, such as a new prompt.
	line() (string, error)
	close()
}

func newKeyInput(r io.Reader) keyInput {
	if f, ok := r.(*os.File); ok && term.IsTerminal(f.Fd()) {
		return &terminalInput{f: f, r: bufio.NewReader(f)}
	}
	if r == nil {
		r = strings.NewReader("")
	}
	return &lineInput{r: bufio.NewReader(r)}
}

// lineInput reads keys from lines, as a script or a pipe gives them.
type lineInput struct{ r *bufio.Reader }

func (in *lineInput) key() (rune, error) {
	line, err := in.r.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return 0, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return '\n', nil
	}
	return unicode.ToLower([]rune(line)[0]), nil
}

func (in *lineInput) line() (string, error) {
	line, err := in.r.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (in *lineInput) close() {}

// terminalInput reads single keys with the terminal in raw mode, only while
// waiting for a key, so output is never affected.
type terminalInput struct {
	f *os.File
	r *bufio.Reader
}

func (in *terminalInput) key() (rune, error) {
	state, err := term.MakeRaw(in.f.Fd())
	if err != nil {
		return 0, err
	}
	defer func() { _ = term.Restore(in.f.Fd(), state) }()
	for {
		b, err := in.r.ReadByte()
		if err != nil {
			return 0, err
		}
		switch b {
		case '\r', '\n', ' ':
			return '\n', nil
		case 3, 4: // Ctrl-C, Ctrl-D
			return 'q', nil
		}
		if b < 0x80 {
			return unicode.ToLower(rune(b)), nil
		}
	}
}

// line reads a line in the terminal's normal mode, which echoes and edits
// it; a line ends at Enter, whichever byte the terminal sends for it.
func (in *terminalInput) line() (string, error) {
	var b strings.Builder
	for {
		c, err := in.r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && b.Len() > 0 {
				break
			}
			return "", err
		}
		if c == '\n' || c == '\r' {
			break
		}
		b.WriteByte(c)
	}
	return strings.TrimSpace(b.String()), nil
}

func (in *terminalInput) close() {}

// errStop ends a Review session at the learner's request.
var errStop = errors.New("stopped")

type reviewSession struct {
	c     *core.Core
	topic string
	in    keyInput
	out   io.Writer
	// rated and dropped count what the session recorded.
	rated, dropped int
}

func (s *reviewSession) say(format string, args ...any) {
	fmt.Fprintf(s.out, format+"\n", args...)
}

func (s *reviewSession) run(ctx context.Context, due core.DueCards, why string) error {
	intro := "Reviewing " + styleAccent.Render(s.topic)
	if why != "" {
		intro += styleDim.Render(" (" + why + ")")
	}
	if due.Energy != "" {
		intro += styleDim.Render(", sized to " + due.Energy + " Energy")
	}
	s.say("%s", intro)
	if len(due.Cards) == 0 {
		s.say("\nNothing to review now.")
		return nil
	}
	for i, card := range due.Cards {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := s.review(ctx, card, i+1, len(due.Cards))
		if errors.Is(err, errStop) || errors.Is(err, io.EOF) {
			s.say("\nStopped. %s", s.saved())
			return nil
		}
		if err != nil {
			return err
		}
	}
	s.say("\nThat's all for now. %s", s.saved())
	return nil
}

func (s *reviewSession) saved() string {
	var parts []string
	if s.rated > 0 {
		parts = append(parts, fmt.Sprintf("%d %s recorded", s.rated, plural(s.rated, "Review", "Reviews")))
	}
	if s.dropped > 0 {
		parts = append(parts, fmt.Sprintf("%d new %s dropped", s.dropped, plural(s.dropped, "Card", "Cards")))
	}
	if len(parts) == 0 {
		return "Nothing was recorded."
	}
	return sentenceCase(strings.Join(parts, ", ")) + "."
}

// review takes the learner through one Card.
func (s *reviewSession) review(ctx context.Context, card core.Card, n, of int) error {
	header := fmt.Sprintf("\n%s %s", styleLabel.Render(fmt.Sprintf("%d/%d", n, of)), styleLabel.Render(cardLabel(card)))
	if card.Draft {
		header += styleWarn.Render(" · new")
	}
	s.say("%s", header)
	s.say("  %s", card.Prompt)

	// Recall, then show the answer.
	for revealed := false; !revealed; {
		s.say("%s", styleDim.Render("  Enter shows the answer · f flag · s skip · q stop"))
		k, err := s.in.key()
		if err != nil {
			return err
		}
		switch k {
		case '\n':
			revealed = true
		case 'f':
			if err := s.flag(ctx, card); err != nil {
				return err
			}
		case 's':
			return nil
		case 'q':
			return errStop
		}
	}
	s.say("  %s %s", styleDim.Render("→"), card.Answer)

	spec := core.ReviewSpec{Card: card.ID}
	if card.Draft {
		decided := false
		for !decided {
			s.say("%s", styleDim.Render("  New Card: k keep · e edit · d drop · f flag · q stop"))
			k, err := s.in.key()
			if err != nil {
				return err
			}
			switch k {
			case 'k':
				spec.Draft, decided = core.DraftKeep, true
			case 'e':
				if err := s.edit(card, &spec); err != nil {
					return err
				}
				decided = true
			case 'd':
				spec.Draft = core.DraftDrop
				if _, err := s.c.RecordReview(ctx, s.topic, spec); err != nil {
					return err
				}
				s.dropped++
				s.say("  Dropped.")
				return nil
			case 'f':
				if err := s.flag(ctx, card); err != nil {
					return err
				}
			case 'q':
				return errStop
			}
		}
	}

	for spec.Rating == "" {
		s.say("%s", styleDim.Render("  How well did you recall it? 1 again · 2 hard · 3 good · 4 easy · f flag · q stop"))
		k, err := s.in.key()
		if err != nil {
			return err
		}
		switch k {
		case '1':
			spec.Rating = core.RatingAgain
		case '2':
			spec.Rating = core.RatingHard
		case '3':
			spec.Rating = core.RatingGood
		case '4':
			spec.Rating = core.RatingEasy
		case 'f':
			if err := s.flag(ctx, card); err != nil {
				return err
			}
		case 'q':
			return errStop
		}
	}
	res, err := s.c.RecordReview(ctx, s.topic, spec)
	if err != nil {
		return err
	}
	s.rated++
	s.say("  %s %s", styleOK.Render(sentenceCase(spec.Rating)+"."), styleDim.Render("Next "+dueIn(res.Card.Due, s.c.Now())+"."))
	return nil
}

// edit asks for a draft's new prompt and answer; an empty line keeps one.
// Keeping both is the same as keeping the Card.
func (s *reviewSession) edit(card core.Card, spec *core.ReviewSpec) error {
	s.say("  New prompt (Enter keeps it):")
	prompt, err := s.in.line()
	if err != nil {
		return err
	}
	s.say("  New answer (Enter keeps it):")
	answer, err := s.in.line()
	if err != nil {
		return err
	}
	if prompt == "" {
		prompt = card.Prompt
	}
	if answer == "" {
		answer = card.Answer
	}
	if prompt == card.Prompt && answer == card.Answer {
		spec.Draft = core.DraftKeep
		return nil
	}
	spec.Draft, spec.Prompt, spec.Answer = core.DraftEdit, prompt, answer
	return nil
}

// flag records that the learner thinks a Card is wrong or unclear.
func (s *reviewSession) flag(ctx context.Context, card core.Card) error {
	s.say("  What is wrong with it? (Enter to skip)")
	note, err := s.in.line()
	if err != nil {
		return err
	}
	if _, err := s.c.FlagCard(ctx, s.topic, card.ID, note, false); err != nil {
		return err
	}
	s.say("  %s", styleWarn.Render("Flagged: your agent will fix it, or edit it with study card edit."))
	return nil
}

// dueIn describes when a Card is next due: in minutes or hours the same
// day, as a new Card's first steps are, otherwise in days.
func dueIn(due, now time.Time) string {
	d := due.Sub(now)
	switch {
	case d <= time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("in %d %s", int(d.Minutes()+0.5), plural(int(d.Minutes()+0.5), "minute", "minutes"))
	case d < 24*time.Hour:
		return fmt.Sprintf("in %d %s", int(d.Hours()+0.5), plural(int(d.Hours()+0.5), "hour", "hours"))
	}
	days := int(d.Hours()/24 + 0.5)
	if days == 1 {
		return "tomorrow"
	}
	return fmt.Sprintf("in %d days", days)
}

func sentenceCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
