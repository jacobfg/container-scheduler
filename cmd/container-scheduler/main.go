package main

import (
	"context"
	"fmt"
	"github.com/jacobfg/container-scheduler/internal/scheduler"
	"github.com/moby/moby/client"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	// The scratch runtime has no system timezone database.
	_ "time/tzdata"
)

func main() {
	if len(os.Args) > 1 {
		if err := runCommand(context.Background(), os.Args[1:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log); err != nil {
		log.Error("scheduler stopped", "error", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, log *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	prefix := os.Getenv("LABEL_PREFIX")
	if prefix == "" {
		prefix = "ofelia"
	}
	timeout := 15 * time.Second
	if value := os.Getenv("DOCKER_API_TIMEOUT"); value != "" {
		var err error
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return fmt.Errorf("DOCKER_API_TIMEOUT must be a positive Go duration")
		}
	}
	cli, err := client.New(client.FromEnv, client.WithTimeout(timeout))
	if err != nil {
		return err
	}
	defer cli.Close()
	engine := scheduler.Docker{Client: cli}
	jobs, err := scheduler.Discover(ctx, engine, os.Getenv("CONTAINER_LABEL_SELECTOR"), prefix, timeout)
	if err != nil {
		return err
	}
	triggers := make(map[string]chan struct{}, len(jobs))
	for _, job := range jobs {
		triggers[job.Name] = make(chan struct{}, 1)
	}
	control, err := listenControl(socketPath(), triggers)
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	defer control.Close()
	var workers sync.WaitGroup
	for _, job := range jobs {
		trigger := triggers[job.Name]
		workers.Go(func() { scheduler.Run(ctx, engine, job, timeout, log, trigger) })
	}
	log.Info("control socket ready", "path", socketPath())
	select {
	case <-ctx.Done():
	case err = <-control.errors:
	}
	cancel()
	control.Close()
	workers.Wait()
	log.Info("scheduler stopped; target containers left running")
	return err
}
