package agent_test

import (
	"testing"

	"cyber-foreman/internal/agent"
	processadapter "cyber-foreman/internal/agent/process"
)

func TestRegistryRejectsDuplicateNamesAndListsAdapters(t *testing.T) {
	registry, err := agent.NewRegistry(processadapter.NewAdapter())
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(processadapter.NewAdapter()); err == nil {
		t.Fatal("duplicate adapter registration succeeded")
	}
	listed := registry.List()
	if len(listed) != 1 || listed[0].Name != "process" {
		t.Fatalf("unexpected descriptors: %#v", listed)
	}
	if _, err := registry.Get("missing"); err == nil {
		t.Fatal("missing adapter lookup succeeded")
	}
}
