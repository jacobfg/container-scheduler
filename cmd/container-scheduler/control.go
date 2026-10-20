package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func socketPath() string {
	if path := os.Getenv("SCHEDULER_SOCKET"); path != "" {
		return path
	}
	return "/run/container-scheduler/control.sock"
}

type runRequest struct {
	Job string `json:"job,omitempty"`
	All bool   `json:"all,omitempty"`
}
type controlServer struct {
	listener net.Listener
	server   *http.Server
	errors   chan error
}

func (s *controlServer) Close() {
	_ = s.server.Close()
	// Close even if Serve has not yet registered the listener.
	_ = s.listener.Close()
}

func listenControl(path string, triggers map[string]chan struct{}) (*controlServer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket %s", path)
		}
		conn, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			conn.Close()
			return nil, fmt.Errorf("control socket already in use: %s", path)
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("probe existing socket: %w", err)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	server := &http.Server{
		Handler:           controlHandler(triggers),
		ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second,
		WriteTimeout: 2 * time.Second, IdleTimeout: 2 * time.Second,
	}
	control := &controlServer{listener: listener, server: server, errors: make(chan error, 1)}
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		control.errors <- err
	}()
	return control, nil
}

func controlHandler(triggers map[string]chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				http.Error(w, "GET required", http.StatusMethodNotAllowed)
				return
			}
			if len(triggers) == 0 {
				http.Error(w, "no jobs loaded", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintln(w, "healthy")
			return
		}
		if r.URL.Path != "/run-now" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		var request runRequest
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			http.Error(w, "expected one JSON request", 400)
			return
		}
		if (request.Job != "") == request.All {
			http.Error(w, "specify a job or all", 400)
			return
		}
		selected := triggers
		if !request.All {
			trigger, ok := triggers[request.Job]
			if !ok {
				http.Error(w, fmt.Sprintf("unknown job %q", request.Job), 404)
				return
			}
			selected = map[string]chan struct{}{request.Job: trigger}
		}
		queued, coalesced := 0, 0
		for _, trigger := range selected {
			select {
			case trigger <- struct{}{}:
				queued++
			default:
				coalesced++
			}
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "Run-now request accepted: %d queued, %d already pending. See scheduler logs for execution results.\n", queued, coalesced)
	})
}

func runNow(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 2 || args[0] != "run-now" || args[1] == "" || (strings.HasPrefix(args[1], "-") && args[1] != "--all") {
		return fmt.Errorf("usage: container-scheduler run-now JOB | run-now --all | healthcheck")
	}
	request := runRequest{Job: args[1]}
	if args[1] == "--all" {
		request = runRequest{All: true}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return controlRequest(ctx, http.MethodPost, "/run-now", string(body), http.StatusAccepted, out)
}

func runCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 1 && args[0] == "healthcheck" {
		return controlRequest(ctx, http.MethodGet, "/healthz", "", http.StatusOK, out)
	}
	return runNow(ctx, args, out)
}

func controlRequest(ctx context.Context, method, endpoint, body string, expectedStatus int, out io.Writer) error {
	path := socketPath()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, method, "http://scheduler"+endpoint, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("contact scheduler at %s: %w", path, err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, 8192))
	if err != nil {
		return err
	}
	if response.StatusCode != expectedStatus {
		return fmt.Errorf("%s rejected (HTTP %d): %s", endpoint, response.StatusCode, strings.TrimSpace(string(result)))
	}
	_, err = out.Write(result)
	return err
}
