package main

import (
	"net"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

// Manual UI check: BB_BROWSER=<seconds> BB_DB=<path> go test -run TestBrowserHarness
// Serves the app over plain HTTP on 127.0.0.1:8765 (test-only; the real server is HTTPS) so a
// browser that distrusts the self-signed certificate can still render the pages.
func TestBrowserHarness(t *testing.T) {
	secs, _ := strconv.Atoi(os.Getenv("BB_BROWSER"))
	if secs == 0 {
		t.Skip("set BB_BROWSER=<seconds> to run")
	}
	db, err := openDB(os.Getenv("BB_DB"))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := newVault("x")
	a := &app{db: db, vault: v, tpl: parseTemplates()}
	srv := httptest.NewUnstartedServer(sameOrigin(a.routes()))
	l, err := newLocalListener("127.0.0.1:8765")
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = l
	srv.Start()
	defer srv.Close()
	time.Sleep(time.Duration(secs) * time.Second)
}

func newLocalListener(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }
