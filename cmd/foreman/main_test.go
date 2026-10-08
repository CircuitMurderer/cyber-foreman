package main

import (
	"strings"
	"testing"
)

func TestValidateListenerSecurityAllowsLoopbackWithoutToken(t *testing.T) {
	t.Parallel()

	for _, addr := range []string{"127.0.0.1:8090", "[::1]:8090", "localhost:8090"} {
		addr := addr
		t.Run(addr, func(t *testing.T) {
			t.Parallel()
			if err := validateListenerSecurity(addr, ""); err != nil {
				t.Fatalf("validateListenerSecurity(%q): %v", addr, err)
			}
		})
	}
}

func TestValidateListenerSecurityRequiresTokenOutsideLoopback(t *testing.T) {
	t.Parallel()

	for _, addr := range []string{"0.0.0.0:8090", ":8090", "192.168.1.8:8090", "foreman.internal:8090"} {
		addr := addr
		t.Run(addr, func(t *testing.T) {
			t.Parallel()
			err := validateListenerSecurity(addr, "")
			if err == nil || !strings.Contains(err.Error(), "refusing unauthenticated") {
				t.Fatalf("validateListenerSecurity(%q) error = %v", addr, err)
			}
		})
	}
}

func TestValidateListenerSecurityAllowsAuthenticatedNetworkListener(t *testing.T) {
	t.Parallel()

	if err := validateListenerSecurity("0.0.0.0:8090", "secret"); err != nil {
		t.Fatalf("validateListenerSecurity: %v", err)
	}
}

func TestValidateListenerSecurityRejectsInvalidAddress(t *testing.T) {
	t.Parallel()

	err := validateListenerSecurity("127.0.0.1", "secret")
	if err == nil || !strings.Contains(err.Error(), "invalid HTTP listen address") {
		t.Fatalf("validateListenerSecurity error = %v", err)
	}
}
