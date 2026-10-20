package scheduler

import (
	"context"
	"github.com/moby/moby/client"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDockerAPI(t *testing.T) {
	var calls []string
	cli, err := client.New(client.WithHost("tcp://proxy:2375"), client.WithAPIVersion("1.44"), client.WithHTTPClient(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		body := ""
		status := 200
		switch r.URL.Path {
		case "/v1.44/containers/json":
			if r.URL.Query().Get("all") != "1" && r.URL.Query().Get("all") != "true" {
				t.Error("must list stopped containers")
			}
			if !strings.Contains(r.URL.Query().Get("filters"), "scope=libation") {
				t.Error("missing discovery filter")
			}
			body = `[{"Id":"old-id","Labels":{"ofelia.enabled":"true"}}]`
		case "/v1.44/containers/libation/json":
			body = `{"Id":"replacement-id","State":{"Running":false}}`
		case "/v1.44/containers/libation/start":
			status = 204
		default:
			t.Errorf("unexpected API operation %s", r.URL.Path)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	d := Docker{cli}
	ctx := context.Background()
	if labels, err := d.Labels(ctx, "scope=libation"); err != nil || len(labels) != 1 {
		t.Fatalf("%v %v", labels, err)
	}
	if err := Tick(ctx, d, Job{Name: "libation", Container: "libation"}, time.Second, quiet); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[2] != "POST /v1.44/containers/libation/start" {
		t.Fatal(calls)
	}
}
