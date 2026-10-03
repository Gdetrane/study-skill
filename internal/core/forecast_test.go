package core

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// Forecasts are checked by hand-worked dates. Each day adds its Pace's
// minutes per week, and n minutes of work fit once seven times n is reached,
// counting today: at 10 h/week (600 minutes), 10 h of work needs 4200, which
// seven days give, so it ends on the seventh day.

// thursday is 1 Oct 2026, mid-morning, in UTC.
var thursday = time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)

// forecastSyllabus has a must-Milestone "core" (a1, a2: 5 h each) and an
// if_time Milestone "extra" (b1: 7 h).
func forecastSyllabus() *studyState {
	st := newStudyState()
	st.syllabus = &Syllabus{Milestones: []Milestone{
		{ID: "core", Title: "Core", Priority: PriorityMust, Lessons: []SyllabusLesson{
			{ID: "a1", Title: "A one", Hours: 5}, {ID: "a2", Title: "A two", Hours: 5}}},
		{ID: "extra", Title: "Extra", Priority: PriorityIfTime, Lessons: []SyllabusLesson{
			{ID: "b1", Title: "B one", Hours: 7}}},
	}}
	return &st
}

func milestoneEnds(f *Forecast) []string {
	var out []string
	for _, m := range f.Milestones {
		out = append(out, m.Ends)
	}
	return out
}

func TestForecastAtOnePace(t *testing.T) {
	f := forecastOf(forecastSyllabus(), []PacePeriod{{HoursPerWeek: 10}}, "", thursday)
	if got := milestoneEnds(f); !slices.Equal(got, []string{"2026-10-07", "2026-10-12"}) {
		t.Fatalf("ends = %v", got)
	}
	if f.Pace != "10 h/week" || f.Milestones[0].Text != "At 10 h/week, Core ends 7 Oct 2026." {
		t.Errorf("pace %q, text %q", f.Pace, f.Milestones[0].Text)
	}
	if f.Milestones[1].RemainingHours != 7 || f.Triage != nil || f.NeedsPace {
		t.Errorf("forecast = %+v", f)
	}
}

func TestForecastCountsOnlyWorkLeft(t *testing.T) {
	st := forecastSyllabus()
	st.lessons["a1"] = &lessonState{completed: &lessonCompletedData{}}
	st.syllabus.Milestones[1].Lessons[0].Skipped = true
	st.lessons["a2"] = &lessonState{phase: PhasePracticing} // in progress: counted in full
	f := forecastOf(st, []PacePeriod{{HoursPerWeek: 10}}, "", thursday)
	core, extra := f.Milestones[0], f.Milestones[1]
	if core.RemainingHours != 5 || core.Ends != "2026-10-04" {
		t.Errorf("core = %+v, want 5 h ending 4 Oct (2100 needs 3.5 days, so the fourth)", core)
	}
	if !extra.Done || extra.Ends != "" || extra.Text != "Extra is done." {
		t.Errorf("extra = %+v, want done: its only Lesson is skipped", extra)
	}
}

func TestForecastAcrossPacePeriods(t *testing.T) {
	// 10 h/week from 1 to 4 Oct gives 2400; then 3 h/week (180 a day) needs
	// 1800 more, ten days from 5 Oct: 14 Oct.
	pace := []PacePeriod{{HoursPerWeek: 10}, {From: "2026-10-05", HoursPerWeek: 3}}
	f := forecastOf(forecastSyllabus(), pace, "", thursday)
	if f.Milestones[0].Ends != "2026-10-14" || f.Milestones[0].Text != "At your Pace, Core ends 14 Oct 2026." {
		t.Errorf("core = %+v", f.Milestones[0])
	}
	if f.Pace != "10 h/week, then 3 h/week from 5 Oct 2026" {
		t.Errorf("pace = %q", f.Pace)
	}
	// A period that ended before today is no longer described.
	later := forecastOf(forecastSyllabus(), pace, "", thursday.AddDate(0, 0, 10))
	if later.Pace != "3 h/week" {
		t.Errorf("pace after the change = %q", later.Pace)
	}
}

func TestForecastWithAPaceThatStartsLater(t *testing.T) {
	// Nothing before 10 Oct, then 10 h/week: Core ends on the seventh day.
	f := forecastOf(forecastSyllabus(), []PacePeriod{{From: "2026-10-10", HoursPerWeek: 10}}, "", thursday)
	if f.Milestones[0].Ends != "2026-10-16" {
		t.Errorf("core ends %s, want 16 Oct", f.Milestones[0].Ends)
	}
}

func TestForecastNeedsEstimatesAndAPace(t *testing.T) {
	st := forecastSyllabus()
	st.syllabus.Milestones[0].Lessons[1].Hours = 0
	f := forecastOf(st, []PacePeriod{{HoursPerWeek: 10}}, "", thursday)
	core, extra := f.Milestones[0], f.Milestones[1]
	if core.Ends != "" || !slices.Equal(core.Unestimated, []string{"a2"}) ||
		core.Text != `Core needs hour estimates for "A two" before it can be forecast.` {
		t.Errorf("core = %+v", core)
	}
	if extra.Ends != "" || extra.Text != "Extra can be forecast once the Lessons before it have hour estimates." {
		t.Errorf("extra = %+v", extra)
	}

	none := forecastOf(forecastSyllabus(), nil, "", thursday)
	if !none.NeedsPace || none.Pace != "" || none.Milestones[0].Ends != "" ||
		none.Milestones[0].Text != "Core has about 10 h of work left; set a Pace to see when it ends." {
		t.Errorf("without a Pace = %+v", none)
	}
	if forecastOf(&studyState{}, nil, "", thursday) != nil {
		t.Error("a Topic without a Syllabus has a Forecast")
	}
}

func TestForecastBeyondTheHorizon(t *testing.T) {
	f := forecastOf(forecastSyllabus(), []PacePeriod{{HoursPerWeek: 0}}, "", thursday)
	if f.Milestones[0].Ends != "" || f.Milestones[0].Text != "At this Pace, Core ends more than ten years from now." {
		t.Errorf("core = %+v", f.Milestones[0])
	}
}

// Today is the learner's calendar day, wherever they are: the same instant
// is still 1 Oct in Honolulu but already 2 Oct in Kiritimati.
func TestForecastUsesTheLearnersCalendarDay(t *testing.T) {
	instant := time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC)
	honolulu := instant.In(time.FixedZone("HST", -10*3600)) // 1 Oct, 23:30
	kiritimati := instant.In(time.FixedZone("LINT", 14*3600))
	pace := []PacePeriod{{HoursPerWeek: 10}}
	if got := forecastOf(forecastSyllabus(), pace, "", honolulu).Milestones[0].Ends; got != "2026-10-07" {
		t.Errorf("in Honolulu Core ends %s, want 7 Oct", got)
	}
	if got := forecastOf(forecastSyllabus(), pace, "", kiritimati).Milestones[0].Ends; got != "2026-10-08" {
		t.Errorf("in Kiritimati Core ends %s, want 8 Oct", got)
	}
	// A period's first day is a day in the learner's calendar too.
	late := []PacePeriod{{HoursPerWeek: 0}, {From: "2026-10-02", HoursPerWeek: 10}}
	if got := forecastOf(forecastSyllabus(), late, "", honolulu).Milestones[0].Ends; got != "2026-10-08" {
		t.Errorf("in Honolulu, starting tomorrow, Core ends %s, want 8 Oct", got)
	}
}

func TestTriage(t *testing.T) {
	st := forecastSyllabus()
	st.syllabus.Milestones[0].Target = "2026-10-05"
	f := forecastOf(st, []PacePeriod{{HoursPerWeek: 10}}, "", thursday)
	core := f.Milestones[0]
	if core.Deadline != "2026-10-05" || !core.AfterDeadline ||
		core.Text != "At 10 h/week, Core ends 7 Oct 2026; its target is 5 Oct 2026." {
		t.Fatalf("core = %+v", core)
	}
	// Five days to 5 Oct give 3000 of the 4200 needed: 1200 left over is
	// 172 minutes, 2.9 h. The whole 10 h in five days is 14 h/week. Moving
	// "A two" (5 h) past the deadline frees enough.
	tr := f.Triage
	if tr == nil || tr.Milestone != "core" || tr.TrimHours != 2.9 || tr.RaisePaceTo != 14 ||
		!slices.Equal(tr.MoveLessons, []string{"a2"}) {
		t.Fatalf("triage = %+v", tr)
	}
	want := `To finish Core by 5 Oct 2026: raise the Pace to about 14 h/week until then, move "A two" past the ` +
		`deadline, or trim about 2.9 h of Stretch goals.`
	if tr.Text != want {
		t.Errorf("triage text:\n %s\nwant\n %s", tr.Text, want)
	}

	// A must-Milestone without a target is held to the Goal's deadline.
	st.syllabus.Milestones[0].Target = ""
	if g := forecastOf(st, []PacePeriod{{HoursPerWeek: 10}}, "2026-10-05", thursday); g.Triage == nil ||
		g.Milestones[0].Deadline != "2026-10-05" {
		t.Errorf("with the Goal's deadline: %+v", g)
	}

	// An optional Milestone that ends after its target gets no Triage.
	st.syllabus.Milestones[1].Target = "2026-10-08"
	opt := forecastOf(st, []PacePeriod{{HoursPerWeek: 10}}, "", thursday)
	if !opt.Milestones[1].AfterDeadline || opt.Triage != nil {
		t.Errorf("optional Milestone: %+v, triage %+v", opt.Milestones[1], opt.Triage)
	}
}

func TestTriageWhenTheDeadlineIsToday(t *testing.T) {
	st := forecastSyllabus()
	st.syllabus.Milestones[0].Target = "2026-10-01" // today: 600 of 4200
	st.lessons["a2"] = &lessonState{phase: PhaseTeaching}
	tr := forecastOf(st, []PacePeriod{{HoursPerWeek: 10}}, "", thursday).Triage
	// All 10 h today. 3600 left over is 515 minutes (8.6 h): more than half
	// of Core, so not Stretch goals to trim. Only "A one" (5 h) is not
	// started, which is not enough to move.
	if tr == nil || tr.MoveLessons != nil || tr.TrimHours != 0 || tr.RaisePaceTo != 0 || tr.HoursToday != 10 {
		t.Fatalf("triage = %+v", tr)
	}
	if want := "To finish Core by 1 Oct 2026: work about 10 h on it today."; tr.Text != want {
		t.Errorf("text = %q", tr.Text)
	}
}

func TestTriageOnceTheDeadlinePassed(t *testing.T) {
	st := forecastSyllabus()
	st.syllabus.Milestones[0].Target = "2026-09-30"
	tr := forecastOf(st, []PacePeriod{{HoursPerWeek: 10}}, "", thursday).Triage
	if tr == nil || !tr.DeadlinePassed || tr.TrimHours != 0 || tr.RaisePaceTo != 0 || tr.SuggestDeadline != "2026-10-07" ||
		tr.Text != "The target date of Core, 30 Sep 2026, has passed: choose a new date, such as 7 Oct 2026, when it is "+
			"forecast to end." {
		t.Errorf("triage = %+v", tr)
	}
	// The Goal's deadline is called the deadline.
	goal := forecastOf(forecastSyllabus(), []PacePeriod{{HoursPerWeek: 10}}, "2026-09-01", thursday)
	if core := goal.Milestones[0]; core.DeadlineFrom != DeadlineFromGoal ||
		core.Text != "At 10 h/week, Core ends 7 Oct 2026; the deadline is 1 Sep 2026." {
		t.Errorf("core = %+v", core)
	}
	if tr := goal.Triage; tr == nil || tr.Text != "The deadline, 1 Sep 2026, has passed: choose a new date, such as "+
		"7 Oct 2026, when it is forecast to end." {
		t.Errorf("triage = %+v", goal.Triage)
	}
}

// Only options that can still finish the Milestone are offered; when none
// can, a later date is.
func TestTriageNeverOffersTheImpossible(t *testing.T) {
	st := newStudyState()
	st.syllabus = &Syllabus{Milestones: []Milestone{
		{ID: "core", Title: "Core", Priority: PriorityMust, Target: "2026-10-02", Lessons: []SyllabusLesson{
			{ID: "a1", Title: "A one", Hours: 60}}}}}
	tr := forecastOf(&st, []PacePeriod{{HoursPerWeek: 10}}, "", thursday).Triage
	// 60 h in two days is 210 h/week, over 168; moving or trimming it all
	// would empty the Milestone.
	if tr == nil || tr.RaisePaceTo != 0 || tr.MoveLessons != nil || tr.TrimHours != 0 || tr.SuggestDeadline == "" {
		t.Fatalf("triage = %+v", tr)
	}
	if want := "No change to the Pace or the Lessons can finish Core by 2 Oct 2026: choose a later date, such as " +
		dateText(tr.SuggestDeadline) + ", when it is forecast to end."; tr.Text != want {
		t.Errorf("text = %q", tr.Text)
	}
	// The Pace offered is never above what topic_update accepts.
	st.syllabus.Milestones[0].Target = "2026-10-10"
	if tr := forecastOf(&st, []PacePeriod{{HoursPerWeek: 10}}, "", thursday).Triage; tr == nil || tr.RaisePaceTo > maxHoursPerWeek {
		t.Errorf("triage = %+v", tr)
	} else if _, err := checkPace([]PacePeriod{{HoursPerWeek: tr.RaisePaceTo}}); err != nil {
		t.Errorf("raise_pace_to %v is refused: %v", tr.RaisePaceTo, err)
	}
}

func TestTriageMovesTheFewestLessons(t *testing.T) {
	st := newStudyState()
	st.syllabus = &Syllabus{Milestones: []Milestone{
		{ID: "extra", Title: "Extra", Priority: PriorityIfTime, Lessons: []SyllabusLesson{
			{ID: "b1", Title: "B one", Hours: 1}, {ID: "b2", Title: "B two", Hours: 1}}},
		{ID: "core", Title: "Core", Priority: PriorityMust, Target: "2026-10-07", Lessons: []SyllabusLesson{
			{ID: "a1", Title: "A one", Hours: 5}, {ID: "a2", Title: "A two", Hours: 6}}},
	}}
	// Seven days at 10 h/week fit 10 h of 13: 3 h over. B one and B two free
	// only 2 h, so A two moves, and they need not.
	tr := forecastOf(&st, []PacePeriod{{HoursPerWeek: 10}}, "", thursday).Triage
	if tr == nil || !slices.Equal(tr.MoveLessons, []string{"a2"}) {
		t.Errorf("triage = %+v", tr)
	}
}

// "Raise" only when the Pace offered is higher than today's.
func TestTriageSetsThePaceWhenAPeriodBreaks(t *testing.T) {
	st := newStudyState()
	st.syllabus = &Syllabus{Milestones: []Milestone{
		{ID: "core", Title: "Core", Priority: PriorityMust, Target: "2026-10-15", Lessons: []SyllabusLesson{
			{ID: "a1", Title: "A one", Hours: 5}}}}}
	pace := []PacePeriod{{HoursPerWeek: 10}, {From: "2026-10-02", HoursPerWeek: 0}}
	f := forecastOf(&st, pace, "", thursday)
	if f.Pace != "10 h/week, then a break (0 h/week) from 2 Oct 2026" {
		t.Errorf("pace = %q", f.Pace)
	}
	if tr := f.Triage; tr == nil || tr.RaisePaceTo != 2.5 ||
		!strings.Contains(tr.Text, "set the Pace to about 2.5 h/week until then") {
		t.Errorf("triage = %+v", tr)
	}
}

// Any estimate is at least a minute of work, so a Milestone is never done
// while a Lesson is left.
func TestATinyEstimateIsStillWork(t *testing.T) {
	st := newStudyState()
	st.syllabus = &Syllabus{Milestones: []Milestone{
		{ID: "core", Title: "Core", Priority: PriorityMust, Lessons: []SyllabusLesson{{ID: "a1", Title: "A one", Hours: 0.001}}}}}
	m := forecastOf(&st, []PacePeriod{{HoursPerWeek: 10}}, "", thursday).Milestones[0]
	if m.Done || m.Ends != "2026-10-01" || m.RemainingHours != 0.1 {
		t.Errorf("milestone = %+v", m)
	}
	if none := forecastOf(&st, nil, "", thursday).Milestones[0]; none.Text != "Core has a few minutes of work left; set a Pace to see when it ends." {
		t.Errorf("text = %q", none.Text)
	}
	st.syllabus.Milestones[0].Lessons[0].Hours = 0.33
	if m := forecastOf(&st, []PacePeriod{{HoursPerWeek: 10}}, "", thursday).Milestones[0]; m.RemainingHours != 0.3 {
		t.Errorf("remaining hours = %v, want 0.3", m.RemainingHours)
	}
}

func TestTheHorizon(t *testing.T) {
	// 1 minute a week takes 600 weeks for 10 h: past ten years.
	f := forecastOf(forecastSyllabus(), []PacePeriod{{HoursPerWeek: 1.0 / 60}}, "", thursday)
	if f.Milestones[0].Ends != "" || !strings.Contains(f.Milestones[0].Text, "more than ten years") {
		t.Errorf("core = %+v", f.Milestones[0])
	}
	// Just inside: 10 h at 1 h/week is ten weeks.
	if got := forecastOf(forecastSyllabus(), []PacePeriod{{HoursPerWeek: 1}}, "", thursday).Milestones[0].Ends; got != "2026-12-09" {
		t.Errorf("at 1 h/week Core ends %s, want 9 Dec 2026", got)
	}
	// Beyond the horizon with a deadline: after it, and a Triage that can
	// still raise the Pace.
	st := forecastSyllabus()
	st.syllabus.Milestones[0].Target = "2026-12-31"
	f = forecastOf(st, []PacePeriod{{HoursPerWeek: 0}}, "", thursday)
	if m := f.Milestones[0]; !m.AfterDeadline || m.Ends != "" {
		t.Errorf("core = %+v", m)
	}
	if tr := f.Triage; tr == nil || tr.Ends != "" || tr.RaisePaceTo != 1 || tr.SuggestDeadline != "" {
		t.Errorf("triage = %+v", tr)
	}
}

func TestAPacePeriodStartingToday(t *testing.T) {
	pace := []PacePeriod{{HoursPerWeek: 0}, {From: "2026-10-01", HoursPerWeek: 10}}
	f := forecastOf(forecastSyllabus(), pace, "", thursday)
	if f.Milestones[0].Ends != "2026-10-07" || f.Pace != "10 h/week" {
		t.Errorf("forecast = %+v", f)
	}
}

// Daylight saving changes never add or lose a calendar day: Santiago skips
// the midnight of 6 Sep 2026, and Beirut that of 29 Mar 2026.
func TestForecastAcrossDaylightSavingChanges(t *testing.T) {
	santiago, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Skip(err)
	}
	beirut, err := time.LoadLocation("Asia/Beirut")
	if err != nil {
		t.Skip(err)
	}
	st := newStudyState()
	st.syllabus = &Syllabus{Milestones: []Milestone{
		{ID: "core", Title: "Core", Priority: PriorityMust, Lessons: []SyllabusLesson{{ID: "a1", Title: "A one", Hours: 6}}}}}
	pace := []PacePeriod{{HoursPerWeek: 7}} // an hour a day
	if got := forecastOf(&st, pace, "", time.Date(2026, 9, 1, 10, 0, 0, 0, santiago)).Milestones[0].Ends; got != "2026-09-06" {
		t.Errorf("from 1 Sep in Santiago, Core ends %s, want 6 Sep", got)
	}
	st.syllabus.Milestones[0].Lessons[0].Hours = 1
	if got := forecastOf(&st, pace, "", time.Date(2026, 9, 6, 12, 0, 0, 0, santiago)).Milestones[0].Ends; got != "2026-09-06" {
		t.Errorf("on 6 Sep in Santiago, Core ends %s, want that day", got)
	}
	st.syllabus.Milestones[0].Lessons[0].Hours = 3
	st.syllabus.Milestones[0].Target = "2026-03-31"
	f := forecastOf(&st, pace, "", time.Date(2026, 3, 29, 12, 0, 0, 0, beirut))
	if m := f.Milestones[0]; m.Ends != "2026-03-31" || m.AfterDeadline || f.Triage != nil {
		t.Errorf("in Beirut: %+v, triage %+v; want 31 Mar, on its target", m, f.Triage)
	}
}

func TestTriageOffersOptionalLessonsFirst(t *testing.T) {
	// extra (if_time) comes before a must-Milestone with a deadline, so its
	// Lessons are the first to move.
	st := newStudyState()
	st.syllabus = &Syllabus{Milestones: []Milestone{
		{ID: "extra", Title: "Extra", Priority: PriorityIfTime, Lessons: []SyllabusLesson{{ID: "b1", Title: "B one", Hours: 3}}},
		{ID: "core", Title: "Core", Priority: PriorityMust, Target: "2026-10-05", Lessons: []SyllabusLesson{
			{ID: "a1", Title: "A one", Hours: 5}, {ID: "a2", Title: "A two", Hours: 5}}},
	}}
	tr := forecastOf(&st, []PacePeriod{{HoursPerWeek: 10}}, "", thursday).Triage
	// 13 h needed, 3000 of 5460 fit: 352 minutes over; B one frees 180, A two
	// the rest.
	if tr == nil || !slices.Equal(tr.MoveLessons, []string{"b1", "a2"}) {
		t.Fatalf("triage = %+v", tr)
	}
}
