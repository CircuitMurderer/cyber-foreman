package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	processadapter "cyber-foreman/internal/agent/process"
	"cyber-foreman/internal/api"
	"cyber-foreman/internal/app"
	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/event"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "serve":
		return serve(ctx, args[1:])
	case "run":
		return runCommand(ctx, args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := flags.String("addr", "127.0.0.1:8090", "HTTP listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}

	bus := event.NewBus()
	service := app.NewService(ctx, processadapter.NewAdapter(), bus)
	server := &http.Server{Addr: *addr, Handler: api.NewServer(service, bus).Handler(), ReadHeaderTimeout: 5 * time.Second}

	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		close(shutdownDone)
	}()

	fmt.Printf("cyber-foreman listening on http://%s\n", *addr)
	err := server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownDone
	return nil
}

func runCommand(parent context.Context, args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	timeout := flags.Duration("timeout", 0, "task timeout, for example 10m")
	cwd := flags.String("cwd", "", "working directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := flags.Args()
	if len(command) == 0 {
		return errors.New("run requires a command after --")
	}

	ctx := parent
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, *timeout)
		defer cancel()
	}

	bus := event.NewBus()
	events := bus.Subscribe(ctx, 256)
	service := app.NewService(ctx, processadapter.NewAdapter(), bus)
	task, err := service.StartTask(app.StartTaskRequest{Command: command, CWD: *cwd})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	for {
		select {
		case <-ctx.Done():
			_ = service.StopTask(task.ID)
			return ctx.Err()
		case evt, ok := <-events:
			if !ok {
				return ctx.Err()
			}
			if evt.TaskID != task.ID {
				continue
			}
			if err := encoder.Encode(evt); err != nil {
				return err
			}
			state, isStateEvent := evt.Data.(domain.TaskStateData)
			if evt.Type == domain.EventTaskState && isStateEvent && state.To.Terminal() {
				current, err := service.GetTask(task.ID)
				if err != nil {
					return err
				}
				if current.Status != domain.TaskCompleted {
					return fmt.Errorf("task ended with status %s: %s", current.Status, current.Error)
				}
				return nil
			}
		}
	}
}

func printUsage() {
	fmt.Print(`赛博监工 (cyber-foreman)

Usage:
  foreman serve [--addr 127.0.0.1:8090]
  foreman run [--timeout 10m] [--cwd PATH] -- COMMAND [ARG...]
`)
}
