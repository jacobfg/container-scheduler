package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func valid() map[string]string {
	return map[string]string{
		"ofelia.enabled": "true", "ofelia.service": "true",
		"ofelia.job-run.libation.schedule":   "@every 4h",
		"ofelia.job-run.libation.container":  "libation",
		"ofelia.job-run.libation.no-overlap": "true",
		"ofelia.job-run.libation.delete":     "false",
	}
}
func TestParse(t *testing.T) {
	jobs, err := Parse([]map[string]string{valid()}, "ofelia")
	if err != nil || len(jobs) != 1 || jobs[0].Interval != 4*time.Hour {
		t.Fatalf("%v %v", jobs, err)
	}
	for _, tc := range []struct{ key, value string }{
		{"schedule", "0 */4 * * *"}, {"schedule", "@every 0s"}, {"schedule", "@every -1h"},
		{"container", ""}, {"container", "../bad"}, {"no-overlap", "false"}, {"delete", "true"}, {"command", "ignored"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			labels := valid()
			labels["ofelia.job-run.libation."+tc.key] = tc.value
			if _, err := Parse([]map[string]string{labels}, "ofelia"); err == nil {
				t.Fatal("accepted invalid labels")
			}
		})
	}
	for _, key := range []string{"schedule", "container", "no-overlap", "delete"} {
		labels := valid()
		delete(labels, "ofelia.job-run.libation."+key)
		if _, err := Parse([]map[string]string{labels}, "ofelia"); err == nil {
			t.Fatalf("accepted missing %s", key)
		}
	}
	if _, err := Parse([]map[string]string{valid(), valid()}, "ofelia"); err == nil {
		t.Fatal("accepted duplicate target")
	}
	labels := valid()
	labels["ofelia.job-exec.foo.command"] = "echo hi"
	if _, err := Parse([]map[string]string{labels}, "ofelia"); err == nil {
		t.Fatal("accepted exec")
	}
	labels = valid()
	labels["ofelia.enabled"] = "false"
	if _, err := Parse([]map[string]string{labels}, "ofelia"); err == nil {
		t.Fatal("accepted zero jobs")
	}
	custom := map[string]string{}
	for key, value := range valid() {
		custom["custom"+key[len("ofelia"):]] = value
	}
	if _, err := Parse([]map[string]string{custom}, "custom"); err != nil {
		t.Fatal(err)
	}
}

type fake struct {
	mu                   sync.Mutex
	running              bool
	starts               int
	names                []string
	inspectErr, startErr error
	block                bool
}

func (f *fake) Labels(context.Context, string) ([]map[string]string, error) {
	return []map[string]string{valid()}, nil
}
func (f *fake) Running(ctx context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.names = append(f.names, name)
	if f.block {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return f.running, f.inspectErr
}
func (f *fake) Start(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.names = append(f.names, name)
	if f.startErr != nil {
		return f.startErr
	}
	f.starts++
	f.running = true
	return nil
}
func (f *fake) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.starts }

func TestTickLifecycle(t *testing.T) {
	f := &fake{}
	job := Job{"libation", "libation", time.Hour}
	ctx := context.Background()
	tick := func() {
		t.Helper()
		if err := Tick(ctx, f, job, time.Second, quiet); err != nil {
			t.Fatal(err)
		}
	}
	tick()
	tick()
	if f.count() != 1 {
		t.Fatal("overlap")
	}
	// A replacement/stopped container is resolved by the same name on the next tick.
	f.running = false
	tick()
	if f.count() != 2 {
		t.Fatal("did not start replacement")
	}
	for _, name := range f.names {
		if name != "libation" {
			t.Fatal(name)
		}
	}
	f.inspectErr = errors.New("offline")
	if Tick(ctx, f, job, time.Second, quiet) == nil {
		t.Fatal("lost inspect error")
	}
	f.inspectErr = nil
	f.running = false
	f.startErr = errors.New("start failure")
	if Tick(ctx, f, job, time.Second, quiet) == nil {
		t.Fatal("lost start error")
	}
	f.startErr = nil
	tick()
	if f.count() != 3 {
		t.Fatal("failed to recover")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if Tick(ctx, f, job, time.Second, quiet) == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestTimingAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { Run(ctx, f, Job{"libation", "libation", 4 * time.Hour}, time.Second, quiet); close(done) }()
		synctest.Wait()
		time.Sleep(4*time.Hour - time.Second)
		synctest.Wait()
		if f.count() != 0 {
			t.Fatal("early first run")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if f.count() != 1 {
			t.Fatal("missing first run")
		}
		time.Sleep(4 * time.Hour)
		synctest.Wait()
		if f.count() != 1 {
			t.Fatal("overlap")
		}
		cancel()
		<-done
		if !f.running {
			t.Fatal("shutdown stopped job")
		}
	})
}
func TestTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{block: true}
		err := Tick(context.Background(), f, Job{"job", "libation", time.Hour}, time.Second, quiet)
		if !errors.Is(err, context.DeadlineExceeded) || f.count() != 0 {
			t.Fatalf("%v", err)
		}
	})
}
func TestMissedDeadlines(t *testing.T) {
	base := time.Unix(0, 0)
	now := base.Add(17 * time.Hour)
	if got := Next(base.Add(4*time.Hour), now, 4*time.Hour); !got.Equal(base.Add(20 * time.Hour)) {
		t.Fatal(got)
	}
}
func TestDiscovery(t *testing.T) {
	if _, err := Discover(context.Background(), &fake{}, "", "ofelia", time.Second); err == nil {
		t.Fatal("missing selector accepted")
	}
	if _, err := Discover(context.Background(), &fake{}, "scope=libation", "ofelia", time.Second); err != nil {
		t.Fatal(err)
	}
}
