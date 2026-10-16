package kubernetes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContextNamespace(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "config")
	cfg := `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: "https://127.0.0.1:6443"}
users:
- name: u
  user: {token: x}
contexts:
- name: with-ns
  context: {cluster: c, user: u, namespace: prod}
- name: no-ns
  context: {cluster: c, user: u}
current-context: with-ns
`
	if err := os.WriteFile(kc, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ContextNamespace(kc, ""); got != "prod" {
		t.Errorf("current context: got %q, want prod", got)
	}
	if got := ContextNamespace(kc, "no-ns"); got != "default" {
		t.Errorf("context without namespace: got %q, want default", got)
	}
	if got := ContextNamespace(filepath.Join(dir, "missing"), ""); got != "default" {
		t.Errorf("missing kubeconfig: got %q, want default", got)
	}
}
