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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	opencodeadapter "cyber-foreman/internal/agent/opencode"
	processadapter "cyber-foreman/internal/agent/process"
	"cyber-foreman/internal/api"
	"cyber-foreman/internal/app"
	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/event"
	"cyber-foreman/internal/supervisor"
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
	case "opencode":
		return runOpenCode(ctx, args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runOpenCode(parent context.Context, args []string) error {
	flags := flag.NewFlagSet("opencode", flag.ContinueOnError)
	cwd := flags.String("cwd", ".", "working directory")
	prompt := flags.String("prompt", "", "prompt to send")
	interruptWith := flags.String("interrupt-with", "", "cancel after first output chunk, then send this follow-up prompt")
	model := flags.String("model", "google/gemini-3.8-flash", "OpenCode model in provider/model form")
	binary := flags.String("bin", "scripts/opencode", "OpenCode executable or project-local wrapper")
	idleTimeout := flags.Duration("idle-timeout", 90*time.Second, "time without meaningful progress before intervention")
	hardTimeout := flags.Duration("timeout", 10*time.Minute, "hard task timeout")
	maxNudges := flags.Int("max-nudges", 2, "maximum automatic idle nudges")
	maxRetries := flags.Int("max-retries", 2, "maximum automatic idle recoveries")
	verifyWorkspace := flags.Bool("verify-workspace", true, "verify Git and workspace changes before completion")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *prompt == "" && flags.NArg() > 0 {
		*prompt = strings.Join(flags.Args(), " ")
	}
	if *prompt == "" {
		return errors.New("opencode requires --prompt or positional prompt text")
	}
	absCWD, err := filepath.Abs(*cwd)
	if err != nil {
		return err
	}
	adapter, err := opencodeadapter.NewAdapter(opencodeadapter.Config{Binary: *binary})
	if err != nil {
		return err
	}
	bus := event.NewBus()
	events := bus.Subscribe(parent, 1024)
	service := app.NewService(parent, adapter, bus)
	policy := supervisor.DefaultPolicy()
	policy.IdleTimeout = *idleTimeout
	policy.HardTimeout = *hardTimeout
	policy.MaxNudges = *maxNudges
	policy.MaxRetries = *maxRetries
	task, err := service.StartTask(app.StartTaskRequest{
		Prompt: *prompt, Model: *model, InterruptWith: *interruptWith, CWD: absCWD,
		Supervision:  &policy,
		Verification: app.VerificationRequest{Workspace: *verifyWorkspace},
	})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	for {
		select {
		case <-parent.Done():
			_ = service.StopTask(task.ID)
			return parent.Err()
		case event, ok := <-events:
			if !ok {
				return parent.Err()
			}
			if event.TaskID != task.ID {
				continue
			}
			if err := encoder.Encode(event); err != nil {
				return err
			}
			state, isState := event.Data.(domain.TaskStateData)
			if event.Type != domain.EventTaskState || !isState || !state.To.Terminal() {
				continue
			}
			current, getErr := service.GetTask(task.ID)
			if getErr != nil {
				return getErr
			}
			if current.Status != domain.TaskCompleted {
				return fmt.Errorf("task ended with status %s: %s", current.Status, current.Error)
			}
			return nil
		}
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
  foreman opencode [--model google/MODEL] [--idle-timeout 90s] [--timeout 10m] [--interrupt-with TEXT] --prompt TEXT
`)
}
