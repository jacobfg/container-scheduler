package main

import (
	"bytes"
	"context"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlRouting(t *testing.T) {
	jobs := map[string]chan struct{}{"libation": make(chan struct{}, 1), "backup": make(chan struct{}, 1)}
	handler := controlHandler(jobs)
	send := func(body string) int {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", "/run-now", strings.NewReader(body)))
		return w.Code
	}
	if code := send(`{"job":"libation"}`); code != 202 {
		t.Fatal(code)
	}
	if len(jobs["libation"]) != 1 || len(jobs["backup"]) != 0 {
		t.Fatal("triggered unrelated job")
	}
	if code := send(`{"job":"libation"}`); code != 202 {
		t.Fatal(code)
	}
	if len(jobs["libation"]) != 1 {
		t.Fatal("duplicate was not coalesced")
	}
	<-jobs["libation"]
	for _, body := range []string{`{}`, `{"job":"libation","all":true}`, `{"other":true}`, `{"all":true} {}`, strings.Repeat("x", 5000)} {
		if code := send(body); code != 400 {
			t.Fatalf("%s: %d", body, code)
		}
	}
	if code := send(`{"job":"unknown"}`); code != 404 {
		t.Fatal(code)
	}
	if len(jobs["libation"])+len(jobs["backup"]) != 0 {
		t.Fatal("invalid request queued work")
	}
	if code := send(`{"all":true}`); code != 202 {
		t.Fatal(code)
	}
	if len(jobs["libation"]) != 1 || len(jobs["backup"]) != 1 {
		t.Fatal("all did not trigger both jobs")
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/run-now", 405}, {"POST", "/unknown", 404}} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Fatal(w.Code)
		}
	}
}

func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
func TestRunNowCLI(t *testing.T) {
	path := filepath.Join(socketDir(t), "control.sock")
	t.Setenv("SCHEDULER_SOCKET", path)
	jobs := map[string]chan struct{}{"libation": make(chan struct{}, 1), "backup": make(chan struct{}, 1)}
	server, err := listenControl(path, jobs)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if second, err := listenControl(path, jobs); err == nil {
		second.Close()
		t.Fatal("replaced active socket")
	}
	var out bytes.Buffer
	if err := runNow(context.Background(), []string{"run-now", "libation"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "accepted") || len(jobs["libation"]) != 1 || len(jobs["backup"]) != 0 {
		t.Fatal(out.String())
	}
	<-jobs["libation"]
	if err := runNow(context.Background(), []string{"run-now", "--all"}, &out); err != nil {
		t.Fatal(err)
	}
	if len(jobs["libation"]) != 1 || len(jobs["backup"]) != 1 {
		t.Fatal("all failed")
	}
	for _, args := range [][]string{{}, {"run-now"}, {"run-now", ""}, {"wrong", "libation"}, {"run-now", "--typo"}, {"run-now", "a", "b"}, {"run-now", "unknown"}} {
		if err := runNow(context.Background(), args, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	server.Close()
	if err := runNow(context.Background(), []string{"run-now", "libation"}, &out); err == nil {
		t.Fatal("succeeded with stopped scheduler")
	}
}
func TestSocketStaleAndNonSocket(t *testing.T) {
	path := filepath.Join(socketDir(t), "control.sock")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := listenControl(path, nil); err == nil {
		s.Close()
		t.Fatal("removed regular file")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "keep" {
		t.Fatal("file changed")
	}
	os.Remove(path)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	listener.Close()
	server, err := listenControl(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket not cleaned up: %v", err)
	}
}

func TestHealthcheck(t *testing.T) {
	path := filepath.Join(socketDir(t), "control.sock")
	t.Setenv("SCHEDULER_SOCKET", path)
	jobs := map[string]chan struct{}{"libation": make(chan struct{}, 1)}
	server, err := listenControl(path, jobs)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var output bytes.Buffer
	if err := runCommand(context.Background(), []string{"healthcheck"}, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "healthy\n" || len(jobs["libation"]) != 0 {
		t.Fatal("healthcheck queued work or returned unexpected status")
	}
	jobs["libation"] <- struct{}{}
	if err := runCommand(context.Background(), []string{"healthcheck"}, &output); err != nil {
		t.Fatal("pending work blocked healthcheck:", err)
	}
	server.Close()
	if err := runCommand(context.Background(), []string{"healthcheck"}, &output); err == nil {
		t.Fatal("unavailable scheduler reported healthy")
	}
	if err := runCommand(context.Background(), []string{"healthcheck", "extra"}, &output); err == nil {
		t.Fatal("extra arguments accepted")
	}
	for _, tc := range []struct {
		method string
		jobs   map[string]chan struct{}
		status int
	}{
		{"GET", nil, 503}, {"POST", jobs, 405},
	} {
		w := httptest.NewRecorder()
		controlHandler(tc.jobs).ServeHTTP(w, httptest.NewRequest(tc.method, "/healthz", nil))
		if w.Code != tc.status {
			t.Fatal(w.Code)
		}
	}
}
