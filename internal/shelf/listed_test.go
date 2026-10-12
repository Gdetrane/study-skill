package shelf

import (
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// A status lists no more Sources than it may. When there are more, a Source
// that is being indexed is never the one left out: study follows it by
// asking status.
func TestAStatusNeverLeavesOutASourceBeingIndexed(t *testing.T) {
	src := func(id, state string) plugincontract.SourceStatus {
		return plugincontract.SourceStatus{ID: id, State: state}
	}
	const indexed, indexing, failed = plugincontract.StateIndexed, plugincontract.StateIndexing, plugincontract.StateFailed
	ids := func(sources []plugincontract.SourceStatus) (out string) {
		for _, s := range sources {
			out += s.ID
		}
		return out
	}
	for name, tc := range map[string]struct {
		sources []plugincontract.SourceStatus
		most    int
		want    string
	}{
		"as many as it may list":                  {[]plugincontract.SourceStatus{src("a", indexed), src("b", failed), src("c", indexing)}, 3, "abc"},
		"one more, and the last is being indexed": {[]plugincontract.SourceStatus{src("a", indexed), src("b", failed), src("c", indexed), src("z", indexing)}, 3, "abz"},
		"one more, and the first is":              {[]plugincontract.SourceStatus{src("a", indexing), src("b", failed), src("c", indexed), src("z", indexed)}, 3, "abc"},
		"more being indexed than it may list":     {[]plugincontract.SourceStatus{src("a", indexing), src("b", indexing), src("c", indexing), src("d", indexed)}, 2, "ab"},
		"none being indexed":                      {[]plugincontract.SourceStatus{src("a", indexed), src("b", indexed), src("c", failed)}, 2, "ab"},
	} {
		got := listed(tc.sources, tc.most)
		if ids(got) != tc.want || len(got) > tc.most {
			t.Errorf("%s: a status of at most %d lists %q, want %q", name, tc.most, ids(got), tc.want)
		}
	}
}
