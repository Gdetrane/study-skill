package pluginclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// theKey is a key in the address of the services below, which nothing they
// say may bring back, in part either.
const theKey = "c0ffee5ecr3tk3y0123456789abcdef0"

// shows fails the test when text holds the start of the key.
func shows(t *testing.T, when, text string) {
	t.Helper()
	if strings.Contains(strings.ToLower(text), theKey[:6]) {
		t.Errorf("%s, study says the key, or the start of it: %s", when, text)
	}
}

// What a service writes when it refuses a request is kept up to a few
// hundred bytes. A key that stands across the end of what is kept is cut
// there, before anything looks for it: it is no key any more, and was shown
// up to its last byte. What is kept is the start of a longer text, and is
// read as one.
func TestAKeyAcrossTheEndOfARefusalIsNotShown(t *testing.T) {
	for _, kept := range []int{1, 8, 20, len(theKey) - 1} {
		body := "no" + strings.Repeat(" ", 512-2-kept) + theKey + " is not a key of ours"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(body))
		}))
		c := plugintest.NewComputer(t)
		_, err := pluginclient.Start(context.Background(), c.Env(""), c.RegisterURL(t, "remote", server.URL+"/mcp?key="+theKey), quick(c))
		server.Close()
		if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "(Unauthorized: no") {
			t.Fatalf("Start at a service that refuses = %v", err)
		}
		shows(t, fmt.Sprintf("with %d bytes of the key before the end of what is kept of a refusal", kept), err.Error())
	}
	// A refusal that is short is said whole, without the key in it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "the key "+theKey+" is not a key of ours", http.StatusUnauthorized)
	}))
	defer server.Close()
	c := plugintest.NewComputer(t)
	_, err := pluginclient.Start(context.Background(), c.Env(""), c.RegisterURL(t, "remote", server.URL+"/mcp?key="+theKey), quick(c))
	if err == nil || !strings.Contains(err.Error(), "(Unauthorized: the key … is not a key of ours)") {
		t.Errorf("Start at a service that refuses in a sentence = %v", err)
	}
}

// What is wrong with an answer is said with what the answer held, cut short
// and with what is not ASCII written as escapes: a key in such a value was
// cut in two, or spelled in escapes, and so not found. An answer that holds
// a part of the address is said of without it.
func TestAKeyInAValueThatBreaksTheContractIsNotShown(t *testing.T) {
	ctx := context.Background()
	var state atomic.Pointer[string]
	serve := func(key string) (*pluginclient.Connection, string) {
		address := service(t, plugintest.Config{
			Twist: func(tool string, answer []byte, refused bool) []byte {
				if said := state.Load(); said != nil && tool == plugincontract.ToolStatus {
					quoted, _ := json.Marshal(*said)
					return []byte(`{"sources":[{"id":"book","state":` + string(quoted) + `}]}`)
				}
				return answer
			}}) + "?key=" + key
		c := plugintest.NewComputer(t)
		s, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", address), quick(c))
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s, address
	}
	s, address := serve(theKey)
	for name, said := range map[string]string{
		"behind white space":            strings.Repeat(" ", 80-24) + theKey,
		"in a sentence":                 "failed: the upstream refused our key for this collection, which is " + theKey,
		"whole, at the start":           "failed with " + theKey,
		"the address, cut inside it":    "failed: nothing at " + address + " answers for this collection of yours",
		"the address, behind a preface": "failed: we could not reach " + address,
	} {
		state.Store(&said)
		_, err := s.Status(ctx, "c-0123456789ab")
		if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "is not what the contract describes") {
			t.Fatalf("Status with a state that is none (%s) = %v", name, err)
		}
		shows(t, "with the key "+name+" of a state that is none", err.Error())
		if strings.Contains(err.Error(), "/mcp") {
			t.Errorf("with the key %s of a state that is none, study says where the service is beyond its host: %v", name, err)
		}
	}
	// A value that holds no part of the address is said as it was.
	plain := "failed, and that is no state"
	state.Store(&plain)
	if _, err := s.Status(ctx, "c-0123456789ab"); err == nil || !strings.Contains(err.Error(), `"failed, and that is no state"`) {
		t.Errorf("Status with a state that is none and holds nothing of the address = %v", err)
	}
	// A key that is not ASCII is written in escapes where a value is
	// repeated, which are not the key to whoever looks for it afterwards.
	accented, _ := serve("cl\u00e9-secr\u00e8te-de-moi")
	said := "failed with cl\u00e9-secr\u00e8te-de-moi"
	state.Store(&said)
	_, err := accented.Status(ctx, "c-0123456789ab")
	if err == nil || strings.Contains(err.Error(), "secr") || strings.Contains(err.Error(), `\u00e9`) {
		t.Errorf("Status with a key that is not ASCII in a state that is none = %v", err)
	}
}

// A service that sends study a request is stopped, and the message names the
// method it asked for, which is the service's text like any other.
func TestTheMethodOfAServicesRequestIsShownWithoutTheKey(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	plugin := plugintest.New(plugintest.Config{Folder: t.TempDir()})
	defer plugin.Close()
	var asking atomic.Bool
	handler := plugin.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !asking.Load() || r.Method != http.MethodPost {
			handler.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"service-1\",\"method\":\"auth/%s\"}\n\n", r.URL.Query().Get("key"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	s, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", server.URL+"/mcp?key="+theKey), quick(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	asking.Store(true)
	for range 2 {
		_, err = s.Status(ctx, "c")
		if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "sent study a request (auth/…)") {
			t.Fatalf("Status of a service that sends a request = %v", err)
		}
		shows(t, "of a service that asks for a method named after the key", err.Error())
	}
}
