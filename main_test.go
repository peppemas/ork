package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diskfs/go-diskfs"
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
	if cfg.Workspace.Path != "workspace.img" {
		t.Fatalf("expected default workspace path, got %q", cfg.Workspace.Path)
	}
	if cfg.Exec.SSHPort != 2222 {
		t.Fatalf("expected default SSH port, got %d", cfg.Exec.SSHPort)
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

func TestParseExecArgs(t *testing.T) {
	workdir, command := parseExecArgs([]string{"--workdir", "/workspace/repo", "--", "git", "status"}, "/workspace")
	if workdir != "/workspace/repo" {
		t.Fatalf("workdir = %q, want /workspace/repo", workdir)
	}
	if strings.Join(command, " ") != "git status" {
		t.Fatalf("command = %#v", command)
	}

	workdir, command = parseExecArgs([]string{"pwd"}, "/workspace")
	if workdir != "/workspace" {
		t.Fatalf("default workdir = %q", workdir)
	}
	if len(command) != 1 || command[0] != "pwd" {
		t.Fatalf("command = %#v", command)
	}
}

func TestRemoteShellCommandQuotesArguments(t *testing.T) {
	got := remoteShellCommand([]string{"sh", "-lc", "echo 'hello world'"}, "/workspace/repo")
	want := `cd '/workspace/repo' && exec 'sh' '-lc' 'echo '"'"'hello world'"'"''`
	if got != want {
		t.Fatalf("remoteShellCommand() = %q, want %q", got, want)
	}
}

func TestBuildQEMUArgsIncludesWorkspaceAndSSHForward(t *testing.T) {
	dir := t.TempDir()
	cmdlinePath := filepath.Join(dir, "cmdline")
	if err := os.WriteFile(cmdlinePath, []byte("console=ttyS0\n"), 0o644); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}

	cfg := defaultConfig()
	cfg.Workspace.Path = "workspace.img"
	cfg.VM.Cmdline = cmdlinePath
	cfg.Exec.Host = "127.0.0.1"
	cfg.Exec.SSHPort = 2200

	args := strings.Join(buildQEMUArgs(cfg, true), "\x00")
	if !strings.Contains(args, "file=workspace.img,if=virtio,format=raw") {
		t.Fatalf("workspace drive missing from args: %q", args)
	}
	if !strings.Contains(args, "user,id=net0,hostfwd=tcp:127.0.0.1:2200-:22") {
		t.Fatalf("SSH hostfwd missing from args: %q", args)
	}
	if !strings.Contains(args, "virtio-net-pci,netdev=net0") {
		t.Fatalf("network device missing from args: %q", args)
	}
}

func TestVMStateRoundTrip(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), ".ork", "vm.json")
	state := VMState{
		PID:           123,
		SSHHost:       "127.0.0.1",
		SSHPort:       2222,
		WorkspacePath: "workspace.img",
	}
	if err := writeVMState(statePath, state); err != nil {
		t.Fatalf("writeVMState: %v", err)
	}
	got, err := readVMState(statePath)
	if err != nil {
		t.Fatalf("readVMState: %v", err)
	}
	if got.PID != state.PID || got.SSHPort != state.SSHPort || got.WorkspacePath != state.WorkspacePath {
		t.Fatalf("state roundtrip mismatch: %#v", got)
	}
}

func TestCreateExt4WorkspaceSeedsAuthorizedKeys(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "workspace.img")
	publicKey := "ssh-rsa AAAATEST ork"
	if err := createExt4Workspace(workspacePath, "64M", publicKey); err != nil {
		t.Fatalf("createExt4Workspace: %v", err)
	}

	virtualDisk, err := diskfs.Open(workspacePath)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	defer virtualDisk.Close()
	fs, err := virtualDisk.GetFilesystem(0)
	if err != nil {
		t.Fatalf("open filesystem: %v", err)
	}
	defer fs.Close()

	file, err := fs.OpenFile(".ork/authorized_keys", os.O_RDONLY)
	if err != nil {
		t.Fatalf("open authorized_keys: %v", err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read authorized_keys: %v", err)
	}
	if strings.TrimSpace(string(content)) != publicKey {
		t.Fatalf("authorized_keys = %q, want %q", strings.TrimSpace(string(content)), publicKey)
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
