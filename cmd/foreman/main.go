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

	"cyber-foreman/internal/agent"
	opencodeadapter "cyber-foreman/internal/agent/opencode"
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
	timeout := flags.Duration("timeout", 10*time.Minute, "prompt timeout")
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
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	adapter, err := opencodeadapter.NewAdapter(opencodeadapter.Config{Binary: *binary})
	if err != nil {
		return err
	}
	session, err := adapter.Start(ctx, agent.StartRequest{TaskID: "opencode-cli", CWD: absCWD})
	if err != nil {
		return err
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = adapter.Stop(stopCtx, session.ID)
	}()
	events, err := adapter.Events(ctx, session.ID)
	if err != nil {
		return err
	}
	if *model != "" {
		if err := adapter.SetConfigOption(ctx, session.ID, agent.ConfigOption{ID: "model", Value: *model}); err != nil {
			return fmt.Errorf("select OpenCode model: %w", err)
		}
	}
	type promptOutcome struct {
		turn   int
		result agent.PromptResult
		err    error
	}
	outcome := make(chan promptOutcome, 2)
	startPrompt := func(turn int, text string) {
		go func() {
			result, err := adapter.Prompt(ctx, session.ID, agent.PromptRequest{Text: text})
			outcome <- promptOutcome{turn: turn, result: result, err: err}
		}()
	}
	turn := 1
	interrupted := false
	firstStopReason := ""
	startPrompt(turn, *prompt)
	encoder := json.NewEncoder(os.Stdout)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if err := encoder.Encode(event); err != nil {
				return err
			}
			if *interruptWith != "" && turn == 1 && !interrupted && isAgentMessageChunk(event) {
				if err := adapter.Cancel(ctx, session.ID); err != nil {
					return fmt.Errorf("interrupt OpenCode prompt: %w", err)
				}
				interrupted = true
				if err := encoder.Encode(domain.Event{
					TaskID: "opencode-cli", SessionID: session.ID,
					Type: domain.EventAgentInterrupt, Timestamp: time.Now().UTC(),
					Data: map[string]string{"trigger": "agent_message_chunk"},
				}); err != nil {
					return err
				}
			}
		case completed := <-outcome:
			if completed.turn == 1 && *interruptWith != "" {
				if !interrupted {
					if completed.err != nil {
						return completed.err
					}
					return errors.New("OpenCode prompt completed before an output chunk could be interrupted")
				}
				firstStopReason = completed.result.StopReason
				turn = 2
				if err := encoder.Encode(domain.Event{
					TaskID: "opencode-cli", SessionID: session.ID,
					Type: domain.EventAgentFollowUp, Timestamp: time.Now().UTC(),
					Data: map[string]int{"turn": turn},
				}); err != nil {
					return err
				}
				startPrompt(turn, *interruptWith)
				continue
			}
			if completed.err != nil {
				return completed.err
			}
			return encoder.Encode(map[string]any{
				"task_id": "opencode-cli", "session_id": session.ID,
				"type": "agent.prompt_completed", "timestamp": time.Now().UTC(),
				"data": map[string]any{
					"turn": turn, "stop_reason": completed.result.StopReason,
					"interrupted": interrupted, "first_stop_reason": firstStopReason,
				},
			})
		case <-ctx.Done():
			_ = adapter.Cancel(context.Background(), session.ID)
			return ctx.Err()
		}
	}
}

func isAgentMessageChunk(event domain.Event) bool {
	if event.Type != domain.EventAgentSessionUpdate {
		return false
	}
	data, ok := event.Data.(domain.AgentSessionUpdateData)
	if !ok {
		return false
	}
	var update struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(data.Update, &update); err != nil {
		return false
	}
	return update.SessionUpdate == "agent_message_chunk"
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
  foreman opencode [--model google/MODEL] [--interrupt-with TEXT] --prompt TEXT
`)
}
