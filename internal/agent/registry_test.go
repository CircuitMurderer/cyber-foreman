package agent_test

import (
	"testing"

	"cyber-foreman/internal/agent"
	processadapter "cyber-foreman/internal/agent/process"
)

func TestRegistryRejectsDuplicateNamesAndListsAdapters(t *testing.T) {
	configured := agent.WithMetadata(processadapter.NewAdapter(), agent.Metadata{
		Selectable: true, Driver: "process", DefaultModel: "model", DefaultWorkspace: "/work",
	})
	registry, err := agent.NewRegistry(configured)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(processadapter.NewAdapter()); err == nil {
		t.Fatal("duplicate adapter registration succeeded")
	}
	listed := registry.List()
	if len(listed) != 1 || listed[0].Name != "process" || !listed[0].Installed || !listed[0].Healthy ||
		!listed[0].Selectable || listed[0].DefaultModel != "model" || listed[0].DefaultWorkspace != "/work" {
		t.Fatalf("unexpected descriptors: %#v", listed)
	}
	if _, err := registry.Get("missing"); err == nil {
		t.Fatal("missing adapter lookup succeeded")
	}
}
