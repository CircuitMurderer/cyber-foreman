package agent_test

import (
	"context"
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

func TestMetadataWrapperPreservesPermissionResolver(t *testing.T) {
	base := &permissionAdapter{Adapter: processadapter.NewAdapter()}
	configured := agent.WithMetadata(base, agent.Metadata{Selectable: true})
	if !agent.CanResolvePermission(configured) {
		t.Fatal("permission resolver capability was hidden by metadata wrapper")
	}
	if err := agent.ResolvePermission(context.Background(), configured, "session", "request", "allow"); err != nil {
		t.Fatal(err)
	}
	if !base.called {
		t.Fatal("permission resolution was not delegated")
	}
}

type permissionAdapter struct {
	agent.Adapter
	called bool
}

func (a *permissionAdapter) ResolvePermission(context.Context, string, string, string) error {
	a.called = true
	return nil
}
