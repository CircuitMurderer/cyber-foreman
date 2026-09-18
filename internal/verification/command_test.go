package verification

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCommandVerifier(t *testing.T) {
	verifier := CommandVerifier{
		OutputLimit: 32,
		Env:         []string{"CYBER_FOREMAN_VERIFY_HELPER=1"},
	}
	result := verifier.Run(context.Background(), t.TempDir(), []Command{
		{Argv: helperCommand("pass")},
		{Argv: helperCommand("fail")},
		{Argv: helperCommand("not-run")},
	})
	if result.Passed {
		t.Fatal("expected failed verification")
	}
	if len(result.Commands) != 2 {
		t.Fatalf("ran %d commands, want fail-fast after 2", len(result.Commands))
	}
	failed := result.Commands[1]
	if failed.ExitCode != 7 || !failed.Truncated || !strings.HasSuffix(failed.Output, "failure-tail") {
		t.Fatalf("unexpected failed result: %#v", failed)
	}
}

func TestCommandVerifierTimeout(t *testing.T) {
	verifier := CommandVerifier{Env: []string{"CYBER_FOREMAN_VERIFY_HELPER=1"}}
	result := verifier.Run(context.Background(), t.TempDir(), []Command{{
		Argv: helperCommand("sleep"), Timeout: 50 * time.Millisecond,
	}})
	if result.Passed || len(result.Commands) != 1 || !result.Commands[0].TimedOut {
		t.Fatalf("unexpected timeout result: %#v", result)
	}
}

func TestCommandVerifierRedactsCredentials(t *testing.T) {
	const secret = "credential-value-123"
	verifier := CommandVerifier{Env: []string{
		"CYBER_FOREMAN_VERIFY_HELPER=1",
		"SERVICE_API_KEY=" + secret,
	}}
	result := verifier.Run(context.Background(), t.TempDir(), []Command{{Argv: helperCommand("secret")}})
	if !result.Passed || strings.Contains(result.Commands[0].Output, secret) ||
		!strings.Contains(result.Commands[0].Output, "[REDACTED]") {
		t.Fatalf("credential was not redacted: %#v", result)
	}
}

func TestCommandVerifierHelper(t *testing.T) {
	if os.Getenv("CYBER_FOREMAN_VERIFY_HELPER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "pass":
		fmt.Print("ok")
		os.Exit(0)
	case "fail":
		fmt.Print(strings.Repeat("x", 80) + "failure-tail")
		os.Exit(7)
	case "sleep":
		time.Sleep(10 * time.Second)
		os.Exit(0)
	case "secret":
		fmt.Print("api_key=" + os.Getenv("SERVICE_API_KEY"))
		os.Exit(0)
	default:
		os.Exit(9)
	}
}

func helperCommand(mode string) []string {
	return []string{os.Args[0], "-test.run=TestCommandVerifierHelper", "--", mode}
}
