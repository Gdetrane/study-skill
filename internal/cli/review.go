package cli

import (
	"bytes"
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
			"Recall: Enter shows the answer, and you can type your answer first to compare; a line with just\n" +
			"f flags the Card, s skips it and q stops. Then 1 again, 2 hard, 3 good, 4 easy. At a new Card's\n" +
			"first Review, k keeps it, e edits it and d drops it, after asking. Ctrl-C stops; every Review\n" +
			"already made is kept. Without a terminal, each answer is read from a line of standard input.\n\n" +
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
			s := &reviewSession{c: c, topic: topic, in: newKeyInput(a.stdin), out: a.out}
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

// keyInput reads the learner's answers. Reading waits on a context, so a
// signal stops the session even while it waits.
type keyInput interface {
	// begin starts a new prompt: in a terminal, whatever was typed before
	// it is discarded, so keys typed ahead never answer a question the
	// learner has not seen.
	begin()
	// key returns the next key, lower-cased; '\n' for Enter. io.EOF ends
	// the session.
	key(ctx context.Context) (rune, error)
	// line reads a line of text, such as a new prompt.
	line(ctx context.Context) (string, error)
}

func newKeyInput(r io.Reader) keyInput {
	if r == nil {
		r = strings.NewReader("")
	}
	src := startReading(r)
	if f, ok := r.(*os.File); ok && term.IsTerminal(f.Fd()) {
		return &terminalInput{f: f, src: src}
	}
	return &lineInput{src: src}
}

// byteSource reads its input in the background, so readers can wait for
// the next byte and for a context at once.
type byteSource struct {
	chunks  chan []byte
	err     error // set before chunks is closed
	pending []byte
}

func startReading(r io.Reader) *byteSource {
	src := &byteSource{chunks: make(chan []byte)}
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				src.chunks <- bytes.Clone(buf[:n])
			}
			if err != nil {
				src.err = err
				close(src.chunks)
				return
			}
		}
	}()
	return src
}

// next returns the next byte of input.
func (src *byteSource) next(ctx context.Context) (byte, error) {
	if len(src.pending) == 0 {
		select {
		case chunk, ok := <-src.chunks:
			if !ok {
				return 0, src.err
			}
			src.pending = chunk
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	b := src.pending[0]
	src.pending = src.pending[1:]
	return b, nil
}

// discard drops the input read but not used yet.
func (src *byteSource) discard() {
	src.pending = nil
	for {
		select {
		case _, ok := <-src.chunks:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// readLine reads up to the end of a line, at Enter whichever byte the
// terminal sends for it.
func (src *byteSource) readLine(ctx context.Context) (string, error) {
	var b strings.Builder
	for {
		c, err := src.next(ctx)
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

// lineInput reads answers from lines, as a script or a pipe gives them.
// It never discards input: a script answers before it sees the questions.
type lineInput struct{ src *byteSource }

func (in *lineInput) begin() {}

func (in *lineInput) key(ctx context.Context) (rune, error) {
	line, err := in.src.readLine(ctx)
	if err != nil {
		return 0, err
	}
	if line == "" {
		return '\n', nil
	}
	return unicode.ToLower([]rune(line)[0]), nil
}

func (in *lineInput) line(ctx context.Context) (string, error) { return in.src.readLine(ctx) }

// terminalInput reads single keys with the terminal in raw mode, only while
// waiting for a key, so output and lines typed are never affected.
type terminalInput struct {
	f   *os.File
	src *byteSource
}

func (in *terminalInput) begin() {
	flushInput(in.f.Fd())
	in.src.discard()
}

func (in *terminalInput) key(ctx context.Context) (rune, error) {
	state, err := term.MakeRaw(in.f.Fd())
	if err != nil {
		return 0, err
	}
	defer func() { _ = term.Restore(in.f.Fd(), state) }()
	for {
		b, err := in.src.next(ctx)
		if err != nil {
			return 0, err
		}
		switch {
		case b == '\r' || b == '\n':
			return '\n', nil
		case b == 3 || b == 4: // Ctrl-C, Ctrl-D
			return 'q', nil
		case b == 0x1b:
			in.skipEscape()
		case b >= 0x20 && b < 0x7f:
			return unicode.ToLower(rune(b)), nil
		}
	}
}

// skipEscape drops the rest of an escape sequence, such as an arrow key's
// "\x1b[A", which arrives in one piece.
func (in *terminalInput) skipEscape() {
	p := in.src.pending
	switch {
	case len(p) > 0 && p[0] == '[':
		i := 1
		for i < len(p) && (p[i] < 0x40 || p[i] > 0x7e) {
			i++
		}
		in.src.pending = p[min(i+1, len(p)):]
	case len(p) > 1 && p[0] == 'O':
		in.src.pending = p[2:]
	}
}

func (in *terminalInput) line(ctx context.Context) (string, error) { return in.src.readLine(ctx) }

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

// ask starts a new prompt: it discards what was typed before, then shows
// the prompt, so only keys typed after it appears answer it.
func (s *reviewSession) ask(format string, args ...any) {
	s.in.begin()
	s.say(format, args...)
}

// stopped reports whether err ends the session without failing it: the
// learner stopped, the input ended, or a signal arrived. What was recorded
// so far stays.
func stopped(err error) bool {
	return errors.Is(err, errStop) || errors.Is(err, io.EOF) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
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
	if due.Paused {
		s.say("\n%s", pausedText(s.topic))
		return nil
	}
	if len(due.Cards) == 0 {
		s.say("\nNothing to review now.")
		return nil
	}
	for _, card := range due.Cards {
		err := s.review(ctx, card)
		if stopped(err) {
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

// review takes the learner through one Card. Nothing is recorded until the
// learner rates it or confirms a drop, so stray keys can never lose work.
func (s *reviewSession) review(ctx context.Context, card core.Card) error {
	header := styleLabel.Render(cardLabel(card))
	if card.Draft {
		header += styleWarn.Render(" · new")
	}
	s.say("\n%s", header)
	s.say("  %s", renderCardText(card.Prompt, "  "))

	// Recall: a whole line, so an answer typed to compare never acts as keys.
	typed := ""
	for revealed := false; !revealed; {
		s.ask("%s", styleDim.Render("  Recall the answer, then press Enter to show it (you can type it first) · f flag · s skip · q stop"))
		line, err := s.in.line(ctx)
		if err != nil {
			return err
		}
		switch strings.ToLower(line) {
		case "f":
			if err := s.flag(ctx, card); err != nil {
				return err
			}
		case "s":
			return nil
		case "q":
			return errStop
		default:
			typed, revealed = line, true
		}
	}
	if typed != "" {
		s.say("  %s %s", styleDim.Render("you:"), renderCardText(typed, "       "))
	}
	s.say("  %s %s", styleDim.Render("→"), renderCardText(card.Answer, "    "))

	spec := core.ReviewSpec{Card: card.ID}
	if card.Draft {
		decided, err := s.decide(ctx, card, &spec)
		if err != nil || !decided {
			return err
		}
	}

	s.ask("%s", styleDim.Render("  How well did you recall it? 1 again · 2 hard · 3 good · 4 easy · f flag · q stop"))
	for spec.Rating == "" {
		k, err := s.in.key(ctx)
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
			s.ask("%s", styleDim.Render("  How well did you recall it? 1 again · 2 hard · 3 good · 4 easy · q stop"))
		case 'q':
			return errStop
		}
	}
	res, err := s.c.RecordReview(ctx, s.topic, spec)
	if err != nil {
		return err
	}
	s.rated++
	if spec.Rating == core.RatingAgain {
		s.say("  %s", styleWarn.Render("Again. It comes back next time."))
		return nil
	}
	s.say("  %s %s", styleOK.Render(sentenceCase(spec.Rating)+"."), styleDim.Render("Next "+dueIn(res.Card.Due, s.c.Now())+"."))
	return nil
}

// decide asks what to do with a new Card at its first Review. It returns
// false when the Card was dropped, which needs no rating.
func (s *reviewSession) decide(ctx context.Context, card core.Card, spec *core.ReviewSpec) (bool, error) {
	ask := func() { s.ask("%s", styleDim.Render("  New Card: k keep · e edit · d drop · f flag · q stop")) }
	ask()
	for {
		k, err := s.in.key(ctx)
		if err != nil {
			return false, err
		}
		switch k {
		case 'k':
			spec.Draft = core.DraftKeep
			return true, nil
		case 'e':
			saved, err := s.edit(ctx, card, spec)
			if err != nil {
				return false, err
			}
			if saved {
				return true, nil
			}
			ask()
		case 'd':
			s.ask("  Drop this new Card for good? y/N")
			k, err := s.in.key(ctx)
			if err != nil {
				return false, err
			}
			if k != 'y' {
				s.say("  Kept for now.")
				ask()
				continue
			}
			spec.Draft = core.DraftDrop
			if _, err := s.c.RecordReview(ctx, s.topic, *spec); err != nil {
				return false, err
			}
			s.dropped++
			s.say("  Dropped.")
			return false, nil
		case 'f':
			if err := s.flag(ctx, card); err != nil {
				return false, err
			}
			ask()
		case 'q':
			return false, errStop
		}
	}
}

// edit asks for a draft's new prompt and answer, an empty line keeping
// either, checks them, shows the result and saves it into spec only once the
// learner confirms. It returns false when nothing is to be saved.
func (s *reviewSession) edit(ctx context.Context, card core.Card, spec *core.ReviewSpec) (bool, error) {
	for {
		s.ask("  New prompt (Enter keeps it):")
		prompt, err := s.in.line(ctx)
		if err != nil {
			return false, err
		}
		s.ask("  New answer (Enter keeps it):")
		answer, err := s.in.line(ctx)
		if err != nil {
			return false, err
		}
		if prompt == "" {
			prompt = card.Prompt
		}
		if answer == "" {
			answer = card.Answer
		}
		if prompt == card.Prompt && answer == card.Answer {
			s.say("  Nothing changed.")
			return false, nil
		}
		draft, err := core.CheckCardDraft(core.CardDraft{Prompt: prompt, Answer: answer})
		if err != nil {
			s.say("  %s", styleWarn.Render(sentence(err.Error())+" Try again."))
			continue
		}
		s.say("  %s %s", styleDim.Render("prompt:"), renderCardText(draft.Prompt, "          "))
		s.say("  %s %s", styleDim.Render("answer:"), renderCardText(draft.Answer, "          "))
		s.ask("  Save this edit? y/N")
		k, err := s.in.key(ctx)
		if err != nil {
			return false, err
		}
		if k != 'y' {
			s.say("  Not saved.")
			return false, nil
		}
		spec.Draft, spec.Prompt, spec.Answer = core.DraftEdit, draft.Prompt, draft.Answer
		return true, nil
	}
}

// flag records that the learner thinks a Card is wrong or unclear.
func (s *reviewSession) flag(ctx context.Context, card core.Card) error {
	s.ask("  What is wrong with it? (Enter to skip)")
	note, err := s.in.line(ctx)
	if err != nil {
		return err
	}
	if _, err := s.c.FlagCard(ctx, s.topic, card.ID, note, false); err != nil {
		return err
	}
	s.say("  %s", styleWarn.Render("Flagged: your agent will fix it, or edit it with study card edit."))
	return nil
}

// dueIn describes when a Card is next due, in calendar days on this
// computer's clock.
func dueIn(due, now time.Time) string {
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	y, m, d = due.In(now.Location()).Date()
	days := int(time.Date(y, m, d, 0, 0, 0, 0, now.Location()).Sub(today).Hours()/24 + 0.5)
	switch {
	case days <= 0:
		return "later today"
	case days == 1:
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
