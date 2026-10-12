package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// An address is refused as the connection to it is made, with the number a
// name led to: so it holds for every name, whatever it is looked up as, and
// for every address a page redirects to.
func TestAConnectionIsRefusedByWhereItLeads(t *testing.T) {
	strict := NewFetcher(Limits{}, "study-shelf/test")
	allowed := NewFetcher(Limits{Loopback: true}, "study-shelf/test")
	for address, want := range map[string]struct{ strict, allowed bool }{
		"127.0.0.1:80":                {true, false},
		"127.8.9.10:8080":             {true, false},
		"[::1]:443":                   {true, false},
		"[::ffff:127.0.0.1]:80":       {true, false},
		"169.254.169.254:80":          {true, true},
		"[::ffff:169.254.169.254]:80": {true, true},
		"[fe80::1%eth0]:80":           {true, true},
		"0.0.0.0:80":                  {true, true},
		"[::]:80":                     {true, true},
		"224.0.0.1:80":                {true, true},
		// Where a cloud hands out credentials outside the link-local range.
		"100.100.100.200:80": {true, true},
		"[fd00:ec2::254]:80": {true, true},
		"168.63.129.16:80":   {true, true},
		// The learner's own network, and the rest of the world.
		"10.0.0.7:80":       {false, false},
		"192.168.1.10:8080": {false, false},
		"172.16.3.4:443":    {false, false},
		"100.64.0.1:80":     {false, false},
		"[fd00::1]:80":      {false, false},
		"93.184.216.34:443": {false, false},
	} {
		if got := strict.refuseDial("tcp", address, nil) != nil; got != want.strict {
			t.Errorf("a connection to %s refused = %v, want %v", address, got, want.strict)
		}
		if got := allowed.refuseDial("tcp", address, nil) != nil; got != want.allowed {
			t.Errorf("with loopback allowed, a connection to %s refused = %v, want %v", address, got, want.allowed)
		}
	}
	if err := strict.refuseDial("tcp", "example.org:80", nil); err == nil {
		t.Error("a connection to what is no number was let through")
	}
}

// What is refused as the connection is made reaches the learner as a reason,
// and nothing is sent: a redirect to this computer, from a page that is not
// on it, is the case.
func TestARefusedConnectionIsAReasonAndNothingIsSent(t *testing.T) {
	asked := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked = true }))
	defer server.Close()
	f := NewFetcher(Limits{}, "study-shelf/test")
	client := &http.Client{Transport: f.transport}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(req)
	var r *refusal
	if !errors.As(err, &r) || !r.loopback || asked {
		t.Fatalf("a request to this computer through Shelf's transport gave %v (asked: %v); want it refused as the connection is made", err, asked)
	}
	u, _ := url.Parse("http://books.example/page")
	var p *Problem
	if err := f.failure(context.Background(), u, err); !errors.As(err, &p) || p.Reason == "" {
		t.Errorf("the failure of a refused connection is %v; want a reason for the learner", err)
	} else if want := "books.example leads to 127.0.0.1 is this computer itself"; len(p.Reason) < len(want) || p.Reason[:len(want)] != want {
		t.Errorf("the reason is %q; want it to say where the name led", p.Reason)
	}
}

// The learner's proxy is their own choice, wherever it listens: the
// connection a request makes to its own proxy is not one to a page. That
// holds for the request that goes through the proxy and for no other: the
// same address, asked for as a page a moment later, is refused like any
// other on this computer.
func TestAConnectionToARequestsOwnProxyIsNotRefused(t *testing.T) {
	asked := map[string]int{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked[r.URL.String()]++
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("a page the proxy fetched for " + r.URL.Host))
	}))
	defer proxy.Close()
	through, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	f := NewFetcher(Limits{Timeout: 5 * time.Second, MaxBytes: 1 << 20, MaxRedirects: 3}, "study-shelf/test")
	// As with HTTPS_PROXY alone in the environment would be for https: here
	// one host's requests go through the proxy, and no other's.
	f.proxyFor = func(req *http.Request) (*url.URL, error) {
		if req.URL.Hostname() == "books.example" {
			return through, nil
		}
		return nil, nil
	}
	page, err := f.Fetch(context.Background(), "http://books.example/chapter-1")
	if err != nil || page.Text != "a page the proxy fetched for books.example" || asked["http://books.example/chapter-1"] != 1 {
		t.Fatalf("a fetch through a proxy on this computer gave %+v, %v (the proxy was asked %v)", page, err, asked)
	}

	// The proxy's own address as a page, asked through the same Fetcher's
	// transport after the fetch above, as a name that leads there would be:
	// past the look at how the address is written, so that only the
	// connection can refuse it.
	client := &http.Client{Transport: f.transport}
	ctx := context.WithValue(context.Background(), hopKey{}, &hop{})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(req)
	var r *refusal
	if !errors.As(err, &r) || !r.loopback {
		t.Errorf("a request for the proxy's own address as a page gave %v; want it refused as the connection is made", err)
	}
	if n := asked["/admin"]; n != 0 {
		t.Errorf("the proxy was asked for /admin %d times, as a page", n)
	}
	// And by a Fetcher's own Fetch, which refuses the number before that.
	if _, err := f.Fetch(context.Background(), proxy.URL+"/admin"); err == nil {
		t.Error("the proxy's own address was fetched as a page")
	}

	for address, want := range map[string]string{"http://proxy.example": "proxy.example:80", "https://proxy.example": "proxy.example:443",
		"http://proxy.example:3128": "proxy.example:3128", "socks5://[::1]": "[::1]:1080"} {
		u, _ := url.Parse(address)
		if got := hostPort(u); got != want {
			t.Errorf("a connection to the proxy %s is asked for as %s, want %s", address, got, want)
		}
	}
}
