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
		go func() { Run(ctx, f, Job{"libation", "libation", 4 * time.Hour}, time.Second, quiet, nil); close(done) }()
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

func TestManualRunResetsCadence(t *testing.T) {
	for _, scenario := range []string{"stopped", "running", "offline"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &fake{running: scenario == "running"}
				if scenario == "offline" {
					f.inspectErr = errors.New("offline")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				trigger := make(chan struct{}, 1)
				done := make(chan struct{})
				go func() {
					Run(ctx, f, Job{"libation", "libation", 4 * time.Hour}, time.Second, quiet, trigger)
					close(done)
				}()
				synctest.Wait()
				time.Sleep(time.Hour)
				trigger <- struct{}{}
				synctest.Wait()
				want := 0
				if scenario == "stopped" {
					want = 1
				}
				if f.count() != want {
					t.Fatalf("manual starts=%d, want %d", f.count(), want)
				}
				f.mu.Lock()
				f.running = false
				f.inspectErr = nil
				f.mu.Unlock()
				time.Sleep(3 * time.Hour)
				synctest.Wait()
				if f.count() != want {
					t.Fatal("old deadline was not cancelled")
				}
				time.Sleep(time.Hour)
				synctest.Wait()
				if f.count() != want+1 {
					t.Fatal("missing run four hours after manual request")
				}
				cancel()
				<-done
			})
		})
	}
}

func TestManualRunLeavesOtherJobTimerUnchanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first, second := &fake{}, &fake{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		trigger := make(chan struct{}, 1)
		var workers sync.WaitGroup
		workers.Go(func() { Run(ctx, first, Job{"libation", "libation", 4 * time.Hour}, time.Second, quiet, trigger) })
		workers.Go(func() { Run(ctx, second, Job{"backup", "backup", 4 * time.Hour}, time.Second, quiet, nil) })
		synctest.Wait()
		time.Sleep(time.Hour)
		trigger <- struct{}{}
		synctest.Wait()
		if first.count() != 1 || second.count() != 0 {
			t.Fatal("manual request affected wrong job")
		}
		first.mu.Lock()
		first.running = false
		first.mu.Unlock()
		time.Sleep(3 * time.Hour)
		synctest.Wait()
		if first.count() != 1 || second.count() != 1 {
			t.Fatal("unrelated job cadence changed")
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if first.count() != 2 || second.count() != 1 {
			t.Fatal("target cadence was not reset")
		}
		cancel()
		workers.Wait()
	})
}
func TestDuplicateJobNames(t *testing.T) {
	a, b := valid(), valid()
	b["ofelia.job-run.libation.container"] = "backup"
	if _, err := Parse([]map[string]string{a, b}, "ofelia"); err == nil {
		t.Fatal("duplicate job names accepted")
	}
}
