package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
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

// The learner's proxy is their own choice, wherever it listens: a
// connection to it is not one to a page.
func TestAConnectionToTheLearnersProxyIsNotRefused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback interface to listen on: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	f := NewFetcher(Limits{}, "study-shelf/test")
	dial := f.transport.(*http.Transport).DialContext
	if conn, err := dial(context.Background(), "tcp", listener.Addr().String()); err == nil {
		_ = conn.Close()
		t.Fatal("a connection to this computer was made by a Shelf that fetches nothing from it")
	}
	proxy, _ := url.Parse("http://" + listener.Addr().String())
	f.proxies.Store(hostPort(proxy), true)
	conn, err := dial(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("a connection to the learner's proxy on this computer: %v", err)
	}
	_ = conn.Close()
	for address, want := range map[string]string{"http://proxy.example": "proxy.example:80", "https://proxy.example": "proxy.example:443",
		"http://proxy.example:3128": "proxy.example:3128", "socks5://[::1]": "[::1]:1080"} {
		u, _ := url.Parse(address)
		if got := hostPort(u); got != want {
			t.Errorf("a connection to the proxy %s is asked for as %s, want %s", address, got, want)
		}
	}
}
