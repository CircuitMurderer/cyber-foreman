package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cyber-foreman/internal/access"
	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/agent/acpagent"
	codexadapter "cyber-foreman/internal/agent/codex"
	opencodeadapter "cyber-foreman/internal/agent/opencode"
	processadapter "cyber-foreman/internal/agent/process"
	"cyber-foreman/internal/agentconfig"
	"cyber-foreman/internal/api"
	"cyber-foreman/internal/app"
	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/event"
	sqlitestore "cyber-foreman/internal/storage/sqlite"
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
	opencodeBinary := flags.String("opencode-bin", defaultOpenCodeCommand(), "OpenCode executable or command on PATH")
	grokBinary := flags.String("grok-bin", defaultGrokCommand(), "Grok Build executable or command on PATH")
	codexBinary := flags.String("codex-bin", "codex", "Codex CLI executable or command on PATH")
	codexAPIBase := flags.String("codex-api-base", "", "model API base URL for Codex; empty uses Codex login")
	codexAPIKeyEnv := flags.String("codex-api-key-env", "OPENAI_API_KEY", "environment variable containing the Codex provider API key")
	codexModel := flags.String("codex-model", "", "default Codex model when --codex-api-base is set")
	codexAPIFormat := flags.String("codex-api-format", "chat-completions", "Codex provider wire format: responses, chat-completions, or anthropic-messages")
	agentsFile := flags.String("agents-file", "config/agents.json", "JSON file containing selectable agent profiles; empty enables legacy command flags")
	webDir := flags.String("web-dir", "web/dist", "Vite production build directory; empty disables the web console")
	databasePath := flags.String("db", "data/foreman.db", "SQLite database path")
	if err := flags.Parse(args); err != nil {
		return err
	}

	store, err := sqlitestore.Open(*databasePath)
	if err != nil {
		return fmt.Errorf("open persistence store: %w", err)
	}
	defer store.Close()
	bus := event.NewBusWithJournal(store)
	configuredAdapters := []agent.Adapter{processadapter.NewAdapter()}
	var securityConfig *agentconfig.Security
	var supervisorConfig *agentconfig.SupervisorConfig
	if *agentsFile != "" {
		configuration, loadErr := agentconfig.LoadFile(*agentsFile)
		if loadErr != nil {
			return loadErr
		}
		securityConfig = configuration.Security
		supervisorConfig = configuration.Supervisor
		for _, profile := range configuration.Agents {
			adapter, adapterErr := agentconfig.Build(profile)
			if adapterErr != nil {
				return fmt.Errorf("configure agent %q: %w", profile.Name, adapterErr)
			}
			status := agent.Probe(ctx, adapter)
			if !status.Healthy {
				fmt.Fprintf(os.Stderr, "warning: agent %s unavailable: %s\n", profile.Name, status.Error)
			}
			configuredAdapters = append(configuredAdapters, adapter)
		}
	} else {
		profiles := []acpagent.Profile{
			acpagent.OpenCodeProfile(*opencodeBinary),
			acpagent.GrokProfile(*grokBinary),
		}
		for _, profile := range profiles {
			adapter, adapterErr := acpagent.NewAdapter(acpagent.Config{Profile: profile})
			if adapterErr != nil {
				return fmt.Errorf("configure ACP adapter %q: %w", profile.Name, adapterErr)
			}
			status := adapter.Probe(ctx)
			if !status.Healthy {
				fmt.Fprintf(os.Stderr, "warning: ACP adapter %s unavailable: %s\n", profile.Name, status.Error)
			}
			configuredAdapters = append(configuredAdapters, agent.WithMetadata(adapter, agent.Metadata{
				Selectable: true, Driver: agentconfig.DriverACP,
			}))
		}
		codexConfig := codexadapter.Config{Binary: *codexBinary}
		if strings.TrimSpace(*codexAPIBase) != "" {
			codexConfig.Provider = &codexadapter.ProviderConfig{
				BaseURL: *codexAPIBase, APIKeyEnv: *codexAPIKeyEnv, DefaultModel: *codexModel,
				Protocol: codexadapter.ProviderProtocol(*codexAPIFormat),
			}
		}
		codexAdapter, codexErr := codexadapter.NewAdapter(codexConfig)
		if codexErr != nil {
			return fmt.Errorf("configure Codex adapter: %w", codexErr)
		}
		codexStatus := codexAdapter.Probe(ctx)
		if !codexStatus.Healthy {
			fmt.Fprintf(os.Stderr, "warning: Codex adapter unavailable: %s\n", codexStatus.Error)
		}
		providerFormat := ""
		if codexConfig.Provider != nil {
			if codexConfig.Provider.Protocol == codexadapter.ProtocolAnthropic {
				providerFormat = agentconfig.FormatAnthropic
			} else {
				providerFormat = agentconfig.FormatOpenAI
			}
		}
		configuredAdapters = append(configuredAdapters, agent.WithMetadata(codexAdapter, agent.Metadata{
			Selectable: true, Driver: agentconfig.DriverCodex,
			ProviderFormat: providerFormat, DefaultModel: *codexModel,
		}))
	}
	adapters, err := agent.NewRegistry(configuredAdapters...)
	if err != nil {
		return fmt.Errorf("configure adapter registry: %w", err)
	}
	service, err := app.NewServiceWithRegistryAndStore(ctx, adapters, "process", bus, store)
	if err != nil {
		return fmt.Errorf("restore control plane: %w", err)
	}
	apiToken := ""
	if securityConfig != nil {
		policy, policyErr := access.NewPolicy(access.Config{
			WorkspaceRoots: securityConfig.WorkspaceRoots, RestrictWorkspace: securityConfig.WorkspaceRoots != nil,
			CommandAllowlist: securityConfig.CommandAllowlist, RestrictCommands: securityConfig.CommandAllowlist != nil,
		})
		if policyErr != nil {
			return fmt.Errorf("configure task access policy: %w", policyErr)
		}
		service.SetRequestPolicy(policy)
		if securityConfig.APITokenEnv != "" {
			var present bool
			apiToken, present = os.LookupEnv(securityConfig.APITokenEnv)
			if !present || strings.TrimSpace(apiToken) == "" {
				return fmt.Errorf("API token environment variable %s is not set", securityConfig.APITokenEnv)
			}
			apiToken = strings.TrimSpace(apiToken)
		}
	}
	if supervisorConfig != nil && supervisorConfig.SemanticReview != nil {
		reviewConfig := supervisorConfig.SemanticReview
		apiKey := ""
		reviewerEnabled := true
		if reviewConfig.APIKeyEnv != "" {
			value, present := os.LookupEnv(reviewConfig.APIKeyEnv)
			if !present || strings.TrimSpace(value) == "" {
				fmt.Fprintf(os.Stderr, "warning: semantic reviewer disabled: environment variable %s is not set\n", reviewConfig.APIKeyEnv)
				reviewerEnabled = false
			} else {
				apiKey = strings.TrimSpace(value)
			}
		}
		if reviewerEnabled {
			reviewer, reviewerErr := supervisor.NewOpenAIReviewer(supervisor.OpenAIReviewerConfig{
				BaseURL: reviewConfig.BaseURL, APIKey: apiKey, Model: reviewConfig.Model,
				Timeout: reviewConfig.TimeoutDuration(),
			})
			if reviewerErr != nil {
				return fmt.Errorf("configure semantic reviewer: %w", reviewerErr)
			}
			service.SetSemanticReviewer(reviewer)
			fmt.Printf("semantic reviewer enabled: openai/%s\n", reviewConfig.Model)
		}
	}
	if err := validateListenerSecurity(*addr, apiToken); err != nil {
		return err
	}
	var frontend []http.Handler
	if *webDir != "" {
		webHandler, webErr := api.NewSPAHandler(*webDir)
		if webErr != nil {
			if !errors.Is(webErr, os.ErrNotExist) {
				return fmt.Errorf("configure web console: %w", webErr)
			}
			fmt.Fprintf(os.Stderr, "warning: web console disabled: %s (run pnpm --dir web build)\n", webErr)
		} else {
			frontend = append(frontend, webHandler)
		}
	}
	var frontendHandler http.Handler
	if len(frontend) > 0 {
		frontendHandler = frontend[0]
	}
	server := &http.Server{
		Addr:              *addr,
		Handler:           api.NewServerWithOptions(service, bus, api.ServerOptions{Frontend: frontendHandler, APIToken: apiToken}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		close(shutdownDone)
	}()

	fmt.Printf("cyber-foreman listening on http://%s (database: %s)\n", *addr, store.Path())
	err = server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownDone
	return nil
}

func validateListenerSecurity(addr, apiToken string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid HTTP listen address %q: %w", addr, err)
	}
	if strings.TrimSpace(apiToken) != "" || strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing unauthenticated non-loopback listener %q: configure security.api_token_env", addr)
}

func defaultOpenCodeCommand() string {
	const projectWrapper = "scripts/opencode"
	if info, err := os.Stat(projectWrapper); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return projectWrapper
	}
	return "opencode"
}

func defaultGrokCommand() string {
	const projectWrapper = "scripts/grok"
	if info, err := os.Stat(projectWrapper); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return projectWrapper
	}
	return "grok"
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
  foreman serve [--addr 127.0.0.1:8090] [--opencode-bin PATH] [--grok-bin PATH] [--codex-bin PATH] [--codex-api-base URL --codex-api-key-env NAME --codex-model MODEL --codex-api-format FORMAT] [--agents-file agents.json] [--web-dir web/dist] [--db data/foreman.db]
  foreman run [--timeout 10m] [--cwd PATH] -- COMMAND [ARG...]
  foreman opencode [--model google/MODEL] [--idle-timeout 90s] [--timeout 10m] [--interrupt-with TEXT] --prompt TEXT
`)
}
