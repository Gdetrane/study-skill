package core

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Forecasts and Triage (design: "Syllabus, Forecasts and Revisions"). A
// Forecast says when each Milestone ends at the learner's Pace; it is shown
// instead of any count of what is late. When a must-Milestone is forecast to
// end after its deadline, the core offers a Triage: trim Stretch goals, move
// Lessons past the deadline, or raise the Pace. Nothing changes until the
// learner chooses: a Revision for the first two, a Pace change for the last.
//
// The arithmetic is exact and deterministic. Work is counted in minutes and
// the Pace in minutes per week, so each calendar day adds its period's
// minutes per week, and n minutes of work fit once seven times n is reached.
// Days are calendar days where the learner is (the location of the core's
// clock), starting today; work goes through the Syllabus in order, and only
// Lessons neither done nor skipped count, a Lesson in progress fully.

// forecastHorizon bounds how far a Forecast looks ahead: about ten years.
const forecastHorizon = 3653

// Forecast is when each Milestone of a Topic ends at its Pace.
type Forecast struct {
	// Pace describes the Pace the Forecast assumes, such as "10 h/week" or
	// "10 h/week, then 3 h/week from 16 Nov 2026"; empty without a Pace.
	Pace string `json:"pace,omitempty"`
	// NeedsPace is set when no Pace is set, so no dates can be forecast.
	NeedsPace  bool                `json:"needs_pace,omitempty"`
	Milestones []MilestoneForecast `json:"milestones"`
	// Triage is offered when a must-Milestone is forecast to end after its
	// deadline; nothing changes until the learner chooses an option.
	Triage *Triage `json:"triage,omitempty"`
}

// MilestoneForecast is one Milestone's Forecast.
type MilestoneForecast struct {
	Milestone string `json:"milestone"`
	Title     string `json:"title"`
	Priority  string `json:"priority"`
	// Deadline is the date the Milestone should end by: its target date,
	// else the Goal's deadline for a must-Milestone.
	Deadline string `json:"deadline,omitempty"`
	// RemainingHours is the estimated work left in the Milestone's Lessons
	// that are neither done nor skipped.
	RemainingHours float64 `json:"remaining_hours"`
	// Ends is the day the Milestone is forecast to end, YYYY-MM-DD; empty
	// when it is done, needs hour estimates, or has no Pace to go by.
	Ends string `json:"ends,omitempty"`
	Done bool   `json:"done,omitempty"`
	// Unestimated lists the Milestone's Lessons that need an hour estimate
	// before it can be forecast.
	Unestimated []string `json:"unestimated,omitempty"`
	// AfterDeadline is set when the Milestone is forecast to end after its
	// deadline.
	AfterDeadline bool `json:"after_deadline,omitempty"`
	// Text says it in one sentence, such as "At 10 h/week, Core ends 16 Oct
	// 2026."
	Text string `json:"text"`
}

// Triage offers ways to finish a must-Milestone by its deadline.
type Triage struct {
	Milestone string `json:"milestone"`
	Title     string `json:"title"`
	Deadline  string `json:"deadline"`
	// Ends is when the Milestone is forecast to end at the current Pace;
	// empty when not within the Forecast's ten years.
	Ends string `json:"ends,omitempty"`
	// DeadlinePassed is set when the deadline is before today: choosing a
	// new date comes first.
	DeadlinePassed bool `json:"deadline_passed,omitempty"`
	// TrimHours is how many hours of work, Stretch goals for example, do
	// not fit before the deadline at the current Pace.
	TrimHours float64 `json:"trim_hours,omitempty"`
	// RaisePaceTo is the hours a week, from today to the deadline, that
	// finish the Milestone in time.
	RaisePaceTo float64 `json:"raise_pace_to,omitempty"`
	// MoveLessons are Lessons not started that, moved past the deadline,
	// make the Milestone fit: optional Lessons before it first, then its
	// own from the last. Empty when moving them would not be enough.
	MoveLessons []string `json:"move_lessons,omitempty"`
	// Text offers the options in plain words.
	Text string `json:"text"`
}

// paceCalendar gives each day its Pace, in minutes per week.
type paceCalendar struct {
	starts  []time.Time // first day of each period; zero for "from the start"
	minutes []int64     // minutes per week of each period
}

func newPaceCalendar(periods []PacePeriod, loc *time.Location) paceCalendar {
	var pc paceCalendar
	for _, p := range periods {
		var start time.Time
		if p.From != "" {
			start, _ = time.ParseInLocation(dateLayout, p.From, loc)
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

// localDay is midnight of t's calendar day in t's location.
func localDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// forecastOf computes a Topic's Forecast from its replayed Syllabus and
// progress, its Pace periods and the Goal's deadline, as of now. It is nil
// without a Syllabus.
func forecastOf(st *studyState, pace []PacePeriod, goalDeadline string, now time.Time) *Forecast {
	if st.syllabus == nil {
		return nil
	}
	today := localDay(now)
	loc := now.Location()
	f := &Forecast{Milestones: []MilestoneForecast{}, NeedsPace: len(pace) == 0}
	if !f.NeedsPace {
		f.Pace = paceText(pace, today)
	}
	cal := newPaceCalendar(pace, loc)

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
			w.minutes += int64(math.Round(l.Hours * 60))
			if v.Status == LessonNotStarted {
				w.movable = append(w.movable, l)
			}
		}
		work = append(work, w)
	}

	// Walk the days once, recording the day each cumulative amount of work
	// is reached; a Milestone after one that needs estimates has no date.
	var needs []int64 // cumulative minutes × 7 through each Milestone
	var cum int64
	blocked := false
	blockedAt := make([]bool, len(work))
	for i, w := range work {
		if len(w.unestimated) > 0 {
			blocked = true
		}
		blockedAt[i] = blocked
		cum += w.minutes
		needs = append(needs, cum*7)
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

	var first *Triage
	for i, w := range work {
		mf := MilestoneForecast{Milestone: w.m.ID, Title: w.m.Title, Priority: w.m.Priority,
			RemainingHours: float64(w.minutes) / 60, Unestimated: w.unestimated}
		mf.Deadline = w.m.Target
		if mf.Deadline == "" && w.m.Priority == PriorityMust {
			mf.Deadline = goalDeadline
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
			mf.Text = fmt.Sprintf("%s has about %s of work left; set a Pace to see when it ends.",
				w.m.Title, hoursText(roundHours(mf.RemainingHours)))
		case ends[i].IsZero():
			mf.Text = fmt.Sprintf("At this Pace, %s ends more than ten years from now.", w.m.Title)
		default:
			mf.Ends = ends[i].Format(dateLayout)
			mf.Text = fmt.Sprintf("%s, %s ends %s.", paceLead(pace, today, ends[i]), w.m.Title, dayText(ends[i]))
			if mf.Deadline != "" {
				mf.Text = strings.TrimSuffix(mf.Text, ".") + fmt.Sprintf("; its target is %s.", dateText(mf.Deadline, loc))
			}
		}
		if !done && !blockedAt[i] && !f.NeedsPace && mf.Deadline != "" {
			deadline, _ := time.ParseInLocation(dateLayout, mf.Deadline, loc)
			mf.AfterDeadline = ends[i].IsZero() || ends[i].After(deadline)
			if mf.AfterDeadline && w.m.Priority == PriorityMust && first == nil {
				first = triage(work, i, needs[i], deadline, today, cal, mf, st.syllabus)
			}
		}
		f.Milestones = append(f.Milestones, mf)
	}
	f.Triage = first
	return f
}

// milestoneWork is the work left in one Milestone.
type milestoneWork struct {
	m           Milestone
	minutes     int64 // remaining and estimated
	unestimated []string
	movable     []SyllabusLesson // not started and estimated, in order
}

// triage builds the offer for the must-Milestone at index i, whose
// cumulative work through it is need (minutes × 7).
func triage(work []milestoneWork, i int, need int64, deadline, today time.Time, cal paceCalendar,
	mf MilestoneForecast, sy *Syllabus) *Triage {
	t := &Triage{Milestone: mf.Milestone, Title: mf.Title, Deadline: mf.Deadline, Ends: mf.Ends}
	loc := today.Location()
	if deadline.Before(today) {
		t.DeadlinePassed = true
		t.Text = fmt.Sprintf("The target date of %s, %s, has passed: choose a new date with a Revision, "+
			"or move some of its Lessons past it.", mf.Title, dateText(mf.Deadline, loc))
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
	t.TrimHours = roundUpHours(float64(trim) / 60)
	// Hours a week, from today to the deadline, that fit all the work.
	perWeek := float64(need) / float64(days) / 60 // need is minutes × 7, so this is per week
	t.RaisePaceTo = roundUpHalf(perWeek)

	// Lessons to move: not started, from the Milestones before it that are
	// optional, then its own from the last.
	var candidates []SyllabusLesson
	for _, w := range work[:i] {
		if w.m.Priority != PriorityMust {
			candidates = append(candidates, w.movable...)
		}
	}
	own := work[i].movable
	for k := len(own) - 1; k >= 0; k-- {
		candidates = append(candidates, own[k])
	}
	var freed int64
	for _, l := range candidates {
		if freed >= trim {
			break
		}
		t.MoveLessons = append(t.MoveLessons, l.ID)
		freed += int64(math.Round(l.Hours * 60))
	}
	if freed < trim {
		t.MoveLessons = nil
	}

	options := []string{fmt.Sprintf("raise the Pace to about %s/week until then", hoursText(t.RaisePaceTo))}
	if len(t.MoveLessons) > 0 {
		options = append(options, fmt.Sprintf("move %s past the deadline", lessonTitles(sy, t.MoveLessons)))
	}
	options = append(options, fmt.Sprintf("trim about %s of Stretch goals", hoursText(t.TrimHours)))
	t.Text = fmt.Sprintf("To finish %s by %s: %s.", mf.Title, dateText(mf.Deadline, loc), joinOptions(options))
	return t
}

// paceText describes the Pace from today on.
func paceText(periods []PacePeriod, today time.Time) string {
	sorted := append([]PacePeriod(nil), periods...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].From < sorted[j].From })
	var parts []string
	for i, p := range sorted {
		from, _ := time.ParseInLocation(dateLayout, p.From, today.Location())
		// A period that ends before today no longer matters.
		if i+1 < len(sorted) {
			next, _ := time.ParseInLocation(dateLayout, sorted[i+1].From, today.Location())
			if !next.After(today) {
				continue
			}
		}
		text := hoursText(p.HoursPerWeek) + "/week"
		if p.From != "" && from.After(today) {
			text += " from " + dayText(from)
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, ", then ")
}

// paceLead opens a Forecast sentence: "At 10 h/week" when one Pace holds
// from today to the end, else "At your Pace".
func paceLead(periods []PacePeriod, today, end time.Time) string {
	cal := newPaceCalendar(periods, today.Location())
	m := cal.on(today)
	for d := today; !d.After(end); d = d.AddDate(0, 0, 1) {
		if cal.on(d) != m {
			return "At your Pace"
		}
	}
	return "At " + hoursText(float64(m)/60) + "/week"
}

func dayText(d time.Time) string { return d.Format("2 Jan 2006") }

func dateText(s string, loc *time.Location) string {
	d, err := time.ParseInLocation(dateLayout, s, loc)
	if err != nil {
		return s
	}
	return dayText(d)
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
