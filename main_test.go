package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigUsesDefaultsWhenMissing(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "ork.json"))
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}

	defaults := defaultConfig()
	if cfg != defaults {
		t.Fatalf("expected defaults %#v, got %#v", defaults, cfg)
	}
}

func TestLoadConfigMergesPartialConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "ork.json")
	content := []byte(`{
		"disk": { "path": "custom.img" },
		"vm": { "memory": "4G", "accelerator": "tcg" },
		"image": { "name": "custom-image" }
	}`)
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}

	if cfg.Disk.Path != "custom.img" {
		t.Fatalf("expected custom disk path, got %q", cfg.Disk.Path)
	}
	if cfg.Disk.DefaultSize != "5G" {
		t.Fatalf("expected default disk size, got %q", cfg.Disk.DefaultSize)
	}
	if cfg.VM.Memory != "4G" {
		t.Fatalf("expected custom memory, got %q", cfg.VM.Memory)
	}
	if cfg.VM.Kernel != "test-disk-kernel" {
		t.Fatalf("expected default kernel, got %q", cfg.VM.Kernel)
	}
	if cfg.VM.Accelerator != "tcg" {
		t.Fatalf("expected custom accelerator, got %q", cfg.VM.Accelerator)
	}
	if cfg.Image.Name != "custom-image" {
		t.Fatalf("expected custom image name, got %q", cfg.Image.Name)
	}
	if cfg.Image.Template != "templates/test-disk.yaml" {
		t.Fatalf("expected default template, got %q", cfg.Image.Template)
	}
}

func TestResolveQEMUBinaryUsesConfiguredPath(t *testing.T) {
	called := false
	resolved, err := resolveQEMUBinary(VMConfig{QEMUPath: "/opt/qemu/bin/qemu-system-x86_64"}, "linux", func(string) (string, error) {
		called = true
		return "", errors.New("unexpected lookup")
	})
	if err != nil {
		t.Fatalf("resolveQEMUBinary returned error: %v", err)
	}
	if resolved != "/opt/qemu/bin/qemu-system-x86_64" {
		t.Fatalf("unexpected qemu path: %q", resolved)
	}
	if called {
		t.Fatal("lookPath should not be called when qemu_path is configured")
	}
}

func TestResolveQEMUBinaryLooksOnPath(t *testing.T) {
	resolved, err := resolveQEMUBinary(VMConfig{}, "linux", func(name string) (string, error) {
		if name != "qemu-system-x86_64" {
			t.Fatalf("unexpected lookup candidate: %q", name)
		}
		return "/usr/bin/qemu-system-x86_64", nil
	})
	if err != nil {
		t.Fatalf("resolveQEMUBinary returned error: %v", err)
	}
	if resolved != "/usr/bin/qemu-system-x86_64" {
		t.Fatalf("unexpected qemu path: %q", resolved)
	}
}

func TestResolveQEMUBinaryTriesWindowsExe(t *testing.T) {
	var candidates []string
	resolved, err := resolveQEMUBinary(VMConfig{}, "windows", func(name string) (string, error) {
		candidates = append(candidates, name)
		if name == "qemu-system-x86_64.exe" {
			return `C:\Program Files\qemu\qemu-system-x86_64.exe`, nil
		}
		return "", errors.New("not found")
	})
	if err != nil {
		t.Fatalf("resolveQEMUBinary returned error: %v", err)
	}
	if resolved != `C:\Program Files\qemu\qemu-system-x86_64.exe` {
		t.Fatalf("unexpected qemu path: %q", resolved)
	}
	if len(candidates) != 2 || candidates[0] != "qemu-system-x86_64" || candidates[1] != "qemu-system-x86_64.exe" {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
}

func TestResolveAccelerator(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		goos       string
		stat       func(string) (os.FileInfo, error)
		want       string
	}{
		{
			name:       "explicit",
			configured: "tcg",
			goos:       "windows",
			stat:       func(string) (os.FileInfo, error) { return nil, errors.New("unused") },
			want:       "tcg",
		},
		{
			name:       "windows auto",
			configured: "auto",
			goos:       "windows",
			stat:       func(string) (os.FileInfo, error) { return nil, errors.New("unused") },
			want:       "whpx",
		},
		{
			name:       "linux kvm",
			configured: "auto",
			goos:       "linux",
			stat:       func(string) (os.FileInfo, error) { return nil, nil },
			want:       "kvm",
		},
		{
			name:       "linux tcg",
			configured: "auto",
			goos:       "linux",
			stat:       func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
			want:       "tcg",
		},
		{
			name:       "darwin auto",
			configured: "auto",
			goos:       "darwin",
			stat:       func(string) (os.FileInfo, error) { return nil, errors.New("unused") },
			want:       "hvf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveAccelerator(tt.configured, tt.goos, tt.stat)
			if got != tt.want {
				t.Fatalf("resolveAccelerator() = %q, want %q", got, tt.want)
			}
		})
	}
}
