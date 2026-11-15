package main

import (
	"context"
	"strings"
	"testing"
)

// protects=startup rejects a missing mandatory variable before listening;
// fails_when=run starts a listener or fails to name STORAGE_SECRET_KEY;
// no_claim=live deployment, signal delivery or SeaweedFS connectivity.
func TestStartupMissingEnv(t *testing.T) {
	t.Setenv("STORAGE_ENDPOINT", "http://storage:8333")
	t.Setenv("STORAGE_ACCESS_KEY", "public-test-master")
	t.Setenv("STORAGE_SECRET_KEY", "")
	t.Setenv("CRED_SIGNING_KEY", "public-signing-key-32-characters-long")
	err := run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "STORAGE_SECRET_KEY") {
		t.Fatalf("startup error=%v", err)
	}
}
