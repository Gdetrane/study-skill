package core

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Forecasts and Triage (design: "Syllabus, Forecasts and Revisions"). A
// Forecast says when each Milestone ends at the learner's Pace; it is shown
// instead of any count of what is late. When a must-Milestone is forecast to
// end after its deadline, the core offers a Triage: trim Stretch goals, move
// Lessons past the deadline, or raise the Pace, whichever can still finish
// it in time, or else a later date. Nothing changes until the learner
// chooses: a Revision for Lessons and target dates, topic_update for the
// Pace and the Goal's deadline.
//
// The arithmetic is exact and deterministic. Work is counted in minutes and
// the Pace in minutes per week, so each calendar day adds its period's
// minutes per week, and n minutes of work fit once seven times n is reached.
// Days are civil dates: the learner's calendar day is taken from the core's
// clock in its location, and every date is then a midnight in UTC, so a
// daylight saving change never adds or loses a day. Work goes through the
// Syllabus in order, and only Lessons neither done nor skipped count, a
// Lesson in progress fully.

// forecastHorizon bounds how far a Forecast looks ahead: about ten years.
const forecastHorizon = 3653

// Where a Milestone's deadline comes from.
const (
	DeadlineFromTarget = "target" // the Milestone's target date
	DeadlineFromGoal   = "goal"   // the Goal's deadline, for a must-Milestone
)

// Forecast is when each Milestone of a Topic ends at its Pace.
type Forecast struct {
	// Pace describes the Pace the Forecast assumes, such as "10 h/week" or
	// "10 h/week, then 3 h/week from 16 Nov 2026"; empty without a Pace.
	Pace string `json:"pace,omitempty"`
	// NeedsPace is set when no Pace is set, so no dates can be forecast.
	NeedsPace  bool                `json:"needs_pace,omitempty"`
	Milestones []MilestoneForecast `json:"milestones"`
	// Triage is offered when a must-Milestone is forecast to end after its
	// deadline; nothing changes until the learner chooses an option. It is
	// something to consider, never the one recommended action.
	Triage *Triage `json:"triage,omitempty"`
}

// MilestoneForecast is one Milestone's Forecast.
type MilestoneForecast struct {
	Milestone string `json:"milestone"`
	Title     string `json:"title"`
	Priority  string `json:"priority"`
	// Deadline is the date the Milestone should end by, and DeadlineFrom
	// where it comes from: its target date, else the Goal's deadline for a
	// must-Milestone.
	Deadline     string `json:"deadline,omitempty"`
	DeadlineFrom string `json:"deadline_from,omitempty"`
	// RemainingHours is the estimated work left in the Milestone's Lessons
	// that are neither done nor skipped, to a tenth of an hour.
	RemainingHours float64 `json:"remaining_hours"`
	// Ends is the day the Milestone is forecast to end, YYYY-MM-DD; empty
	// when it is done, needs hour estimates, has no Pace to go by, or ends
	// more than ten years from now.
	Ends string `json:"ends,omitempty"`
	Done bool   `json:"done,omitempty"`
	// Unestimated lists the Milestone's Lessons that need an hour estimate
	// before it can be forecast.
	Unestimated []string `json:"unestimated,omitempty"`
	// AfterDeadline is set when the Milestone is forecast to end after its
	// deadline.
	AfterDeadline bool `json:"after_deadline,omitempty"`
	// Text says it in one sentence, such as "At 10 h/week, Core ends 7 Oct
	// 2026."
	Text string `json:"text"`
}

// Triage offers ways to finish a must-Milestone by its deadline. Only
// options that can are offered; when none can, it suggests a later date.
type Triage struct {
	Milestone    string `json:"milestone"`
	Title        string `json:"title"`
	Deadline     string `json:"deadline"`
	DeadlineFrom string `json:"deadline_from"`
	// Ends is when the Milestone is forecast to end at the current Pace;
	// empty when not within the Forecast's ten years.
	Ends string `json:"ends,omitempty"`
	// DeadlinePassed is set when the deadline is before today: choosing a
	// new date comes first.
	DeadlinePassed bool `json:"deadline_passed,omitempty"`
	// RaisePaceTo is the hours a week, from today to the deadline, that
	// finish the Milestone in time; left out above 168, which no Pace can
	// be, or when the deadline is today.
	RaisePaceTo float64 `json:"raise_pace_to,omitempty"`
	// HoursToday is the work, in hours, that finishes the Milestone when
	// its deadline is today; left out above 24.
	HoursToday float64 `json:"hours_today,omitempty"`
	// MoveLessons are the fewest Lessons not started that, moved past the
	// deadline, make the Milestone fit: optional Lessons before it first,
	// then its own from the last. Left out when that is not enough, or
	// would move all the Milestone's own work.
	MoveLessons []string `json:"move_lessons,omitempty"`
	// TrimHours is how many hours of work, Stretch goals for example, do
	// not fit before the deadline at the current Pace; left out when it is
	// more than half the Milestone's own work, which Stretch goals, being
	// optional extras, never are.
	TrimHours float64 `json:"trim_hours,omitempty"`
	// SuggestDeadline is a date the Milestone can end by at the current
	// Pace, offered when the deadline has passed or no option fits.
	SuggestDeadline string `json:"suggest_deadline,omitempty"`
	// Text offers the options in plain words.
	Text string `json:"text"`
}

// civilDay is t's calendar day, in t's location, as midnight UTC.
func civilDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// parseDay reads a date written YYYY-MM-DD as midnight UTC.
func parseDay(s string) (time.Time, bool) {
	d, err := time.Parse(dateLayout, s)
	return d, err == nil
}

// paceCalendar gives each day its Pace, in minutes per week.
type paceCalendar struct {
	starts  []time.Time // first day of each period; zero for "from the start"
	minutes []int64     // minutes per week of each period
}

func newPaceCalendar(periods []PacePeriod) paceCalendar {
	var pc paceCalendar
	for _, p := range periods {
		var start time.Time
		if p.From != "" {
			start, _ = parseDay(p.From)
		}
		pc.starts = append(pc.starts, start)
		pc.minutes = append(pc.minutes, int64(math.Round(p.HoursPerWeek*60)))
	}
	return pc
}

// on returns the minutes per week planned on day d.
func (pc paceCalendar) on(d time.Time) int64 {
	var m int64
	for i, s := range pc.starts {
		if s.IsZero() || !d.Before(s) {
			m = pc.minutes[i]
		}
	}
	return m
}

// lessonMinutes is a Lesson's estimate in whole minutes; any estimate counts
// as at least one, so a Milestone with work left is never done.
func lessonMinutes(hours float64) int64 {
	return max(1, int64(math.Round(hours*60)))
}

// milestoneWork is the work left in one Milestone.
type milestoneWork struct {
	m           Milestone
	minutes     int64 // remaining and estimated
	unestimated []string
	movable     []SyllabusLesson // not started and estimated, in order
}

// forecastOf computes a Topic's Forecast from its replayed Syllabus and
// progress, its Pace periods and the Goal's deadline, as of now. It is nil
// without a Syllabus.
func forecastOf(st *studyState, pace []PacePeriod, goalDeadline string, now time.Time) *Forecast {
	if st.syllabus == nil {
		return nil
	}
	today := civilDay(now)
	f := &Forecast{Milestones: []MilestoneForecast{}, NeedsPace: len(pace) == 0}
	if !f.NeedsPace {
		f.Pace = paceText(pace, today)
	}
	cal := newPaceCalendar(pace)

	var work []milestoneWork
	for _, m := range st.syllabus.Milestones {
		w := milestoneWork{m: m}
		for _, l := range m.Lessons {
			v := st.lessonView(l)
			if v.Status == LessonDone || v.Status == LessonSkipped {
				continue
			}
			if l.Hours <= 0 {
				w.unestimated = append(w.unestimated, l.ID)
				continue
			}
			w.minutes += lessonMinutes(l.Hours)
			if v.Status == LessonNotStarted {
				w.movable = append(w.movable, l)
			}
		}
		work = append(work, w)
	}

	// Walk the days once, recording the day each cumulative amount of work
	// is reached; a Milestone after one that needs estimates has no date.
	needs := make([]int64, len(work)) // cumulative minutes × 7 through each Milestone
	blockedAt := make([]bool, len(work))
	var cum int64
	blocked := false
	for i, w := range work {
		if len(w.unestimated) > 0 {
			blocked = true
		}
		blockedAt[i] = blocked
		cum += w.minutes
		needs[i] = cum * 7
	}
	ends := make([]time.Time, len(work))
	if !f.NeedsPace {
		var have int64
		next := 0
		for day := 0; day < forecastHorizon && next < len(needs); day++ {
			d := today.AddDate(0, 0, day)
			have += cal.on(d)
			for next < len(needs) && needs[next] <= have {
				ends[next] = d
				next++
			}
		}
	}

	for i, w := range work {
		mf := MilestoneForecast{Milestone: w.m.ID, Title: w.m.Title, Priority: w.m.Priority,
			RemainingHours: roundHours(float64(w.minutes) / 60), Unestimated: w.unestimated}
		if w.minutes > 0 && mf.RemainingHours == 0 {
			mf.RemainingHours = 0.1
		}
		switch {
		case w.m.Target != "":
			mf.Deadline, mf.DeadlineFrom = w.m.Target, DeadlineFromTarget
		case w.m.Priority == PriorityMust && goalDeadline != "":
			mf.Deadline, mf.DeadlineFrom = goalDeadline, DeadlineFromGoal
		}
		done := w.minutes == 0 && len(w.unestimated) == 0
		switch {
		case done:
			mf.Done = true
			mf.Text = fmt.Sprintf("%s is done.", w.m.Title)
		case len(w.unestimated) > 0:
			mf.Text = fmt.Sprintf("%s needs hour estimates for %s before it can be forecast.",
				w.m.Title, lessonTitles(st.syllabus, w.unestimated))
		case blockedAt[i]:
			mf.Text = fmt.Sprintf("%s can be forecast once the Lessons before it have hour estimates.", w.m.Title)
		case f.NeedsPace:
			mf.Text = fmt.Sprintf("%s has %s of work left; set a Pace to see when it ends.",
				w.m.Title, workText(w.minutes))
		case ends[i].IsZero():
			mf.Text = fmt.Sprintf("At this Pace, %s ends more than ten years from now.", w.m.Title)
		default:
			mf.Ends = ends[i].Format(dateLayout)
			mf.Text = fmt.Sprintf("%s, %s ends %s.", paceLead(pace, today, ends[i]), w.m.Title, dayText(ends[i]))
		}
		if mf.Deadline != "" && !mf.Done && !blockedAt[i] && !f.NeedsPace {
			mf.Text = strings.TrimSuffix(mf.Text, ".") + "; " + deadlineText(mf) + "."
		}
		if !done && !blockedAt[i] && !f.NeedsPace && mf.Deadline != "" {
			if deadline, ok := parseDay(mf.Deadline); ok {
				mf.AfterDeadline = ends[i].IsZero() || ends[i].After(deadline)
				if mf.AfterDeadline && w.m.Priority == PriorityMust && f.Triage == nil {
					f.Triage = triage(work, i, needs[i], deadline, today, cal, mf, st.syllabus)
				}
			}
		}
		f.Milestones = append(f.Milestones, mf)
	}
	return f
}

// deadlineText names a Milestone's deadline: "its target is 16 Oct 2026",
// or "the deadline is 1 Dec 2026" for the Goal's.
func deadlineText(mf MilestoneForecast) string {
	if mf.DeadlineFrom == DeadlineFromGoal {
		return "the deadline is " + dateText(mf.Deadline)
	}
	return "its target is " + dateText(mf.Deadline)
}

// triage builds the offer for the must-Milestone at index i, whose
// cumulative work through it is need (minutes × 7).
func triage(work []milestoneWork, i int, need int64, deadline, today time.Time, cal paceCalendar,
	mf MilestoneForecast, sy *Syllabus) *Triage {
	t := &Triage{Milestone: mf.Milestone, Title: mf.Title, Deadline: mf.Deadline, DeadlineFrom: mf.DeadlineFrom, Ends: mf.Ends}
	which := "the target date of " + mf.Title
	if mf.DeadlineFrom == DeadlineFromGoal {
		which = "the deadline"
	}
	later := ""
	if mf.Ends != "" {
		t.SuggestDeadline = mf.Ends
		later = fmt.Sprintf(", such as %s, when it is forecast to end", dateText(mf.Ends))
	}
	if deadline.Before(today) {
		t.DeadlinePassed = true
		t.Text = fmt.Sprintf("%s, %s, has passed: choose a new date%s.", sentenceCase(which), dateText(mf.Deadline), later)
		return t
	}

	var have int64
	days := 0
	for d := today; !d.After(deadline); d = d.AddDate(0, 0, 1) {
		have += cal.on(d)
		days++
	}
	over := need - have // minutes × 7 that do not fit
	trim := (over + 6) / 7
	own := work[i].minutes
	var options []string

	// The Pace that fits all the work by the deadline: need is minutes × 7,
	// so need / days is minutes per week.
	if days == 1 {
		if h := roundUpHalf(float64(need) / 7 / 60); h <= 24 {
			t.HoursToday = h
			options = append(options, fmt.Sprintf("work about %s on it today", hoursText(h)))
		}
	} else if perWeek := roundUpHalf(float64(need) / float64(days) / 60); perWeek <= maxHoursPerWeek {
		t.RaisePaceTo = perWeek
		verb := "raise"
		if float64(cal.on(today)) >= perWeek*60 {
			verb = "set" // the current Pace is as high, but a later period is not
		}
		options = append(options, fmt.Sprintf("%s the Pace to about %s/week until then", verb, hoursText(perWeek)))
	}

	// The fewest Lessons not started that fit it in time if moved past the
	// deadline: optional Lessons before it first, then its own from the last.
	var candidates []SyllabusLesson
	for _, w := range work[:i] {
		if w.m.Priority != PriorityMust {
			candidates = append(candidates, w.movable...)
		}
	}
	ownMovable := work[i].movable
	for k := len(ownMovable) - 1; k >= 0; k-- {
		candidates = append(candidates, ownMovable[k])
	}
	var chosen []SyllabusLesson
	var freed int64
	for _, l := range candidates {
		if freed >= trim {
			break
		}
		chosen = append(chosen, l)
		freed += lessonMinutes(l.Hours)
	}
	if freed >= trim {
		// Drop the Lessons not needed once the later ones are chosen.
		kept := chosen[:0:0]
		for k, l := range chosen {
			rest := freed - lessonMinutes(l.Hours)
			if rest >= trim {
				freed = rest
				continue
			}
			kept = append(kept, chosen[k])
		}
		var movedOwn int64
		for _, l := range kept {
			if _, m, ok := sy.find(l.ID); ok && m.ID == mf.Milestone {
				movedOwn += lessonMinutes(l.Hours)
			}
		}
		if movedOwn < own {
			for _, l := range kept {
				t.MoveLessons = append(t.MoveLessons, l.ID)
			}
			options = append(options, fmt.Sprintf("move %s past the deadline", lessonTitles(sy, t.MoveLessons)))
		}
	}

	// Stretch goals are optional extras within Lessons, so trimming is
	// offered only up to half the Milestone's own work.
	if trim*2 <= own {
		t.TrimHours = roundUpHours(float64(trim) / 60)
		options = append(options, fmt.Sprintf("trim about %s of Stretch goals", hoursText(t.TrimHours)))
	}

	if len(options) == 0 {
		t.Text = fmt.Sprintf("No change to the Pace or the Lessons can finish %s by %s: choose a later date%s.",
			mf.Title, dateText(mf.Deadline), later)
		return t
	}
	t.Text = fmt.Sprintf("To finish %s by %s: %s.", mf.Title, dateText(mf.Deadline), joinOptions(options))
	return t
}

// paceText describes the Pace from today on.
func paceText(periods []PacePeriod, today time.Time) string {
	var parts []string
	for i, p := range periods {
		// A period that ends before today no longer matters.
		if i+1 < len(periods) {
			if next, ok := parseDay(periods[i+1].From); ok && !next.After(today) {
				continue
			}
		}
		text := perWeekText(p.HoursPerWeek)
		if from, ok := parseDay(p.From); ok && from.After(today) {
			text += " from " + dayText(from)
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, ", then ")
}

// workText is an amount of work for people: "about 3.5 h", or "a few
// minutes" when it rounds to nothing.
func workText(minutes int64) string {
	if h := roundHours(float64(minutes) / 60); h > 0 {
		return "about " + hoursText(h)
	}
	return "a few minutes"
}

// perWeekText is a Pace period's hours a week, such as "10 h/week" or "a
// break (0 h/week)".
func perWeekText(h float64) string {
	if h == 0 {
		return "a break (0 h/week)"
	}
	return hoursText(h) + "/week"
}

// paceLead opens a Forecast sentence: "At 10 h/week" when one Pace holds
// from today to the end, else "At your Pace".
func paceLead(periods []PacePeriod, today, end time.Time) string {
	cal := newPaceCalendar(periods)
	m := cal.on(today)
	for d := today; !d.After(end); d = d.AddDate(0, 0, 1) {
		if cal.on(d) != m {
			return "At your Pace"
		}
	}
	return "At " + hoursText(float64(m)/60) + "/week"
}

func dayText(d time.Time) string { return d.Format("2 Jan 2006") }

func dateText(s string) string {
	d, ok := parseDay(s)
	if !ok {
		return s
	}
	return dayText(d)
}

func sentenceCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// lessonTitles names Lessons by title, quoted: "Pointers" and "Structs".
func lessonTitles(sy *Syllabus, ids []string) string {
	var names []string
	for _, id := range ids {
		title := id
		if l, ok := sy.lesson(id); ok {
			title = l.Title
		}
		names = append(names, fmt.Sprintf("%q", title))
	}
	return joinAnd(names)
}

func joinAnd(items []string) string { return join(items, "and") }

// joinOptions joins clauses offered as alternatives: "a, b, or c".
func joinOptions(items []string) string {
	if len(items) < 2 {
		return join(items, "or")
	}
	return strings.Join(items[:len(items)-1], ", ") + ", or " + items[len(items)-1]
}

func join(items []string, word string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + word + " " + items[len(items)-1]
}

// roundHours rounds to a tenth of an hour, for showing estimates.
func roundHours(h float64) float64 { return math.Round(h*10) / 10 }

// roundUpHours rounds up to a tenth of an hour, so what to trim is never
// understated.
func roundUpHours(h float64) float64 { return math.Ceil(h*10-1e-9) / 10 }

// roundUpHalf rounds up to half an hour, for a Pace.
func roundUpHalf(h float64) float64 { return math.Ceil(h*2-1e-9) / 2 }
