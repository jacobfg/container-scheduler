// Package scheduler starts existing containers without managing their lifecycle.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Job struct {
	Name, Container string
	Interval        time.Duration
}
type Engine interface {
	Labels(context.Context, string) ([]map[string]string, error)
	Running(context.Context, string) (bool, error)
	Start(context.Context, string) error
}

var containerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func Parse(sources []map[string]string, prefix string) ([]Job, error) {
	if prefix == "" || strings.ContainsAny(prefix, " \t\n") {
		return nil, fmt.Errorf("label prefix must be nonempty and contain no whitespace")
	}
	var jobs []Job
	targets := map[string]bool{}
	for _, labels := range sources {
		enabled := labels[prefix+".enabled"]
		if enabled == "" || enabled == "false" {
			continue
		}
		if enabled != "true" {
			return nil, fmt.Errorf("%s.enabled must be true or false", prefix)
		}
		groups := map[string]map[string]string{}
		for key, value := range labels {
			if !strings.HasPrefix(key, prefix+".") {
				continue
			}
			suffix := strings.TrimPrefix(key, prefix+".")
			if suffix == "enabled" || suffix == "service" {
				continue
			}
			parts := strings.Split(suffix, ".")
			if len(parts) != 3 || parts[0] != "job-run" || parts[1] == "" {
				return nil, fmt.Errorf("unsupported label %q: only job-run is supported", key)
			}
			switch parts[2] {
			case "schedule", "container", "no-overlap", "delete":
			default:
				return nil, fmt.Errorf("unsupported job option %q", key)
			}
			if groups[parts[1]] == nil {
				groups[parts[1]] = map[string]string{}
			}
			groups[parts[1]][parts[2]] = value
		}
		for name, fields := range groups {
			fail := func() error {
				return fmt.Errorf("job %q requires schedule='@every <positive Go duration>', container=<name>, no-overlap=true, delete=false", name)
			}
			if !strings.HasPrefix(fields["schedule"], "@every ") {
				return nil, fail()
			}
			interval, err := time.ParseDuration(strings.TrimPrefix(fields["schedule"], "@every "))
			if err != nil || interval <= 0 || !containerName.MatchString(fields["container"]) || fields["no-overlap"] != "true" || fields["delete"] != "false" {
				return nil, fail()
			}
			if targets[fields["container"]] {
				return nil, fmt.Errorf("multiple jobs target container %q", fields["container"])
			}
			targets[fields["container"]] = true
			jobs = append(jobs, Job{name, fields["container"], interval})
		}
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("no jobs discovered; check selector and %s.enabled labels", prefix)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Container < jobs[j].Container })
	return jobs, nil
}

func Discover(ctx context.Context, engine Engine, selector, prefix string, timeout time.Duration) ([]Job, error) {
	key, value, ok := strings.Cut(selector, "=")
	if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("CONTAINER_LABEL_SELECTOR must be key=value")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	labels, err := engine.Labels(ctx, selector)
	if err != nil {
		return nil, fmt.Errorf("discover containers: %w", err)
	}
	return Parse(labels, prefix)
}

func Tick(ctx context.Context, engine Engine, job Job, timeout time.Duration, log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	running, err := engine.Running(ctx, job.Container)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", job.Container, err)
	}
	if running {
		log.Info("skipped running container", "job", job.Name, "container", job.Container)
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := engine.Start(ctx, job.Container); err != nil {
		return fmt.Errorf("start %s: %w", job.Container, err)
	}
	log.Info("container start accepted", "job", job.Name, "container", job.Container)
	return nil
}

// Next preserves the original cadence while discarding all elapsed deadlines.
func Next(deadline, now time.Time, interval time.Duration) time.Time {
	if deadline.After(now) {
		return deadline
	}
	return now.Add(interval - now.Sub(deadline)%interval)
}

func Run(ctx context.Context, engine Engine, job Job, timeout time.Duration, log *slog.Logger) {
	next := time.Now().Add(job.Interval)
	log.Info("job scheduled", "job", job.Name, "container", job.Container, "interval", job.Interval, "next", next)
	timer := time.NewTimer(time.Until(next))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		if err := Tick(ctx, engine, job, timeout, log); err != nil && ctx.Err() == nil {
			log.Error("scheduled start failed; retrying next interval", "job", job.Name, "error", err)
		}
		next = Next(next, time.Now(), job.Interval)
		timer.Reset(time.Until(next))
	}
}
