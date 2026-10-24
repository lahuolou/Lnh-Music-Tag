package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafePluginRelPath(t *testing.T) {
	valid := []string{"plugin.json", "bin/linux-amd64/plugin", "ui/index.html"}
	for _, p := range valid {
		if got, err := safePluginRelPath(p); err != nil || got == "" {
			t.Errorf("safePluginRelPath(%q) = %q, %v", p, got, err)
		}
	}
	invalid := []string{"", "../outside", "a/../../outside", "/etc/passwd", `C:\\Windows\\system.ini`, `..\\outside`}
	for _, p := range invalid {
		if _, err := safePluginRelPath(p); err == nil {
			t.Errorf("safePluginRelPath(%q) unexpectedly accepted", p)
		}
	}
}

func TestAtomicReplaceDirReplacesExistingVersion(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "demo")
	stage := filepath.Join(root, ".staging")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "version.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "version.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplaceDir(stage, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "version.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("got %q, want new", got)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("staging directory should have been renamed, stat err=%v", err)
	}
}

func TestAtomicReplaceDirRollsBackOnFailedInstall(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "demo")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "version.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	missingStage := filepath.Join(root, "missing-stage")
	if err := atomicReplaceDir(missingStage, dest); err == nil {
		t.Fatal("expected failed rename")
	}
	got, err := os.ReadFile(filepath.Join(dest, "version.txt"))
	if err != nil {
		t.Fatalf("old version not restored: %v", err)
	}
	if string(got) != "old" {
		t.Fatalf("got %q, want old version preserved", got)
	}
}

func TestNormalizePluginAPIVersion(t *testing.T) {
	if got, err := normalizePluginAPIVersion(0); err != nil || got != PluginAPIVersion {
		t.Fatalf("legacy version: got %d, err=%v", got, err)
	}
	if got, err := normalizePluginAPIVersion(PluginAPIVersion); err != nil || got != PluginAPIVersion {
		t.Fatalf("v1: got %d, err=%v", got, err)
	}
	if _, err := normalizePluginAPIVersion(PluginAPIVersion + 1); err == nil {
		t.Fatal("unsupported API version should be rejected")
	}
}
