package web_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/shelf/web"
)

// strict are the test's limits as a learner's Shelf has them: nothing is
// fetched from the computer Shelf runs on.
var strict = func() web.Limits {
	l := limits
	l.Loopback = false
	return l
}()

// The learner's agent chooses which addresses are Sources and reads what
// was fetched through a search, and Shelf fetches with the learner's
// network. So Shelf fetches nothing from the computer it runs on, where
// whatever listens trusts whoever reaches it, unless the learner allowed it.
func TestNothingIsFetchedFromThisComputerUnlessAllowed(t *testing.T) {
	var asked atomic.Int64
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("a key that only this computer was to read"))
	})
	port := server.URL[strings.LastIndex(server.URL, ":"):]
	for _, address := range []string{
		server.URL + "/latest/meta-data/",
		"http://localhost" + port + "/",
		"http://LOCALHOST." + port + "/",
		"http://app.localhost" + port + "/",
		"http://[::1]" + port + "/",
		"http://[::ffff:127.0.0.1]" + port + "/",
		"http://127.0.0.2" + port + "/",
	} {
		_, err := web.NewFetcher(strict, "study-shelf/test").Fetch(context.Background(), address)
		reason := problem(t, err, "this computer itself", "--allow-loopback")
		if strings.Contains(reason, "meta-data") || strings.Contains(reason, "a key") {
			t.Errorf("the reason for %s repeats the path or the page: %s", address, reason)
		}
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("the server on this computer was asked %d times by a Shelf that fetches nothing from it", n)
	}
	// A learner who serves the page to themselves allows it.
	page, err := fetch(t, server.URL+"/notes")
	if err != nil || !strings.Contains(page.Text, "a key that only this computer") || asked.Load() != 1 {
		t.Errorf("with loopback allowed the fetch gave %+v, %v, and the server was asked %d times", page, err, asked.Load())
	}
}

// The link-local addresses hold no page a learner studies from, and a
// cloud's machines hand their credentials to whoever asks at one of them.
// Nothing is fetched from them, allowed or not, nor from the unspecified
// address, which leads to this computer.
func TestNothingIsFetchedFromTheLinkLocalAddresses(t *testing.T) {
	for _, address := range []string{
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/role",
		"http://[fe80::1]/",
		"http://[::ffff:169.254.169.254]/",
		"http://0.0.0.0/",
		"http://[::]/",
		// Where a cloud does the same outside the link-local range.
		"http://100.100.100.200/latest/meta-data/ram/security-credentials/role",
		"http://[fd00:ec2::254]/latest/meta-data/iam/security-credentials/role",
		"http://168.63.129.16/machine?comp=goalstate",
	} {
		for _, l := range []web.Limits{strict, limits} {
			_, err := web.NewFetcher(l, "study-shelf/test").Fetch(context.Background(), address)
			if reason := problem(t, err, "Shelf fetches nothing from it"); strings.Contains(reason, "security-credentials") {
				t.Errorf("the reason for %s repeats the path: %s", address, reason)
			}
		}
	}
}

// An address is checked for every hop: a page anyone can reach that sends
// Shelf on to an address it fetches nothing from is not followed there.
//
// A redirect to this computer, for a Shelf that fetches nothing from it,
// cannot be tried from a server on this computer, which such a Shelf does
// not reach either: it is refused as the connection is made
// (TestARefusedConnectionIsAReasonAndNothingIsSent).
func TestARedirectToWhereNothingIsFetchedFromIsNotFollowed(t *testing.T) {
	for _, to := range []string{"http://169.254.169.254/latest/meta-data/", "http://[fe80::1]/"} {
		outer := serve(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusFound) })
		_, err := fetch(t, outer.URL+"/looks-innocent")
		problem(t, err, "redirects to one Shelf fetches nothing from")
	}
}
