package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	gossh "golang.org/x/crypto/ssh"
)

const configFileName = "ork.json"

type Config struct {
	VM        VMConfig        `json:"vm"`
	Image     ImageConfig     `json:"image"`
	Workspace WorkspaceConfig `json:"workspace"`
	Exec      ExecConfig      `json:"exec"`
}

type VMConfig struct {
	Memory      string `json:"memory"`
	Kernel      string `json:"kernel"`
	Initrd      string `json:"initrd"`
	Cmdline     string `json:"cmdline"`
	QEMUPath    string `json:"qemu_path"`
	Accelerator string `json:"accelerator"`
}

type ImageConfig struct {
	LinuxKitPath string `json:"linuxkit_path"`
	Template     string `json:"template"`
	Name         string `json:"name"`
	Format       string `json:"format"`
}

type WorkspaceConfig struct {
	Path        string `json:"path"`
	DefaultSize string `json:"default_size"`
	Mount       string `json:"mount"`
}

type ExecConfig struct {
	Host         string `json:"host"`
	SSHPort      int    `json:"ssh_port"`
	User         string `json:"user"`
	KeyPath      string `json:"key_path"`
	StatePath    string `json:"state_path"`
	ReadyTimeout string `json:"ready_timeout"`
}

type VMState struct {
	PID           int       `json:"pid"`
	SSHHost       string    `json:"ssh_host"`
	SSHPort       int       `json:"ssh_port"`
	WorkspacePath string    `json:"workspace_path"`
	StartedAt     time.Time `json:"started_at"`
}

func defaultConfig() Config {
	return Config{
		VM: VMConfig{
			Memory:      "2G",
			Kernel:      "test-disk-kernel",
			Initrd:      "test-disk-initrd.img",
			Cmdline:     "test-disk-cmdline",
			QEMUPath:    "",
			Accelerator: "auto",
		},
		Image: ImageConfig{
			LinuxKitPath: "",
			Template:     "templates/test-disk.yaml",
			Name:         "test-disk",
			Format:       "kernel+initrd",
		},
		Workspace: WorkspaceConfig{
			Path:        "workspace.img",
			DefaultSize: "100M",
			Mount:       "/workspace",
		},
		Exec: ExecConfig{
			Host:         "127.0.0.1",
			SSHPort:      2222,
			User:         "root",
			KeyPath:      ".ork/id_rsa",
			StatePath:    ".ork/vm.json",
			ReadyTimeout: "60s",
		},
	}
}

func loadConfig(configPath string) (Config, error) {
	cfg := defaultConfig()

	content, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}

	if err := json.Unmarshal(content, &cfg); err != nil {
		return cfg, err
	}

	applyConfigDefaults(&cfg)
	return cfg, nil
}

func applyConfigDefaults(cfg *Config) {
	defaults := defaultConfig()

	if strings.TrimSpace(cfg.VM.Memory) == "" {
		cfg.VM.Memory = defaults.VM.Memory
	}
	if strings.TrimSpace(cfg.VM.Kernel) == "" {
		cfg.VM.Kernel = defaults.VM.Kernel
	}
	if strings.TrimSpace(cfg.VM.Initrd) == "" {
		cfg.VM.Initrd = defaults.VM.Initrd
	}
	if strings.TrimSpace(cfg.VM.Cmdline) == "" {
		cfg.VM.Cmdline = defaults.VM.Cmdline
	}
	if strings.TrimSpace(cfg.VM.Accelerator) == "" {
		cfg.VM.Accelerator = defaults.VM.Accelerator
	}
	if strings.TrimSpace(cfg.Image.Template) == "" {
		cfg.Image.Template = defaults.Image.Template
	}
	if strings.TrimSpace(cfg.Image.Name) == "" {
		cfg.Image.Name = defaults.Image.Name
	}
	if strings.TrimSpace(cfg.Image.Format) == "" {
		cfg.Image.Format = defaults.Image.Format
	}
	if strings.TrimSpace(cfg.Workspace.Path) == "" {
		cfg.Workspace.Path = defaults.Workspace.Path
	}
	if strings.TrimSpace(cfg.Workspace.DefaultSize) == "" {
		cfg.Workspace.DefaultSize = defaults.Workspace.DefaultSize
	}
	if strings.TrimSpace(cfg.Workspace.Mount) == "" {
		cfg.Workspace.Mount = defaults.Workspace.Mount
	}
	if strings.TrimSpace(cfg.Exec.Host) == "" {
		cfg.Exec.Host = defaults.Exec.Host
	}
	if cfg.Exec.SSHPort == 0 {
		cfg.Exec.SSHPort = defaults.Exec.SSHPort
	}
	if strings.TrimSpace(cfg.Exec.User) == "" {
		cfg.Exec.User = defaults.Exec.User
	}
	if strings.TrimSpace(cfg.Exec.KeyPath) == "" {
		cfg.Exec.KeyPath = defaults.Exec.KeyPath
	}
	if strings.TrimSpace(cfg.Exec.StatePath) == "" {
		cfg.Exec.StatePath = defaults.Exec.StatePath
	}
	if strings.TrimSpace(cfg.Exec.ReadyTimeout) == "" {
		cfg.Exec.ReadyTimeout = defaults.Exec.ReadyTimeout
	}
}

func mustLoadConfig() Config {
	cfg, err := loadConfig(configFileName)
	if err != nil {
		log.Fatalf("[ORK] Could not load %s: %v", configFileName, err)
	}
	return cfg
}

func printUsage() {
	fmt.Println("ORK usage:")
	fmt.Println("  ork create-workspace [size]         -> Create the ext4 workspace disk")
	fmt.Println("  ork build-image [template] [name]   -> Build the LinuxKit kernel/initrd")
	fmt.Println("  ork start [--daemon]                -> Start the micro-VM")
	fmt.Println("  ork exec [--workdir dir] -- <cmd>   -> Run a command in the daemon VM")
	fmt.Println("  ork run [--workdir dir] -- <cmd>    -> Alias for ork exec")
	fmt.Println("  ork put <local-file> [remote-path]  -> Upload a file into the VM workspace")
	fmt.Println("  ork get <remote-path> [local-file]  -> Download a file from the VM")
	fmt.Println("  ork stop                            -> Stop the daemon VM")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  ork create-workspace 10G")
	fmt.Println("  ork build-image")
	fmt.Println("  ork start --daemon")
	fmt.Println("  ork exec -- node app.js")
	fmt.Println("  ork put app.jar")
	fmt.Println("  ork get /workspace/output.png result.png")
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "create-workspace":
			size := mustLoadConfig().Workspace.DefaultSize
			if len(os.Args) > 2 {
				size = os.Args[2]
			}
			createWorkspaceDisk(size)
			return

		case "build-image":
			buildImage(os.Args[2:])
			return

		case "start":
			if len(os.Args) > 2 && os.Args[2] == "--daemon" {
				startDaemonVM()
				return
			}
			launchVM()
			return

		case "exec":
			execInVM(os.Args[2:])
			return

		case "run":
			execInVM(os.Args[2:])
			return

		case "put":
			if len(os.Args) < 3 {
				usageAndExit("ork put <local-file> [remote-path]")
			}
			remotePath := ""
			if len(os.Args) > 3 {
				remotePath = os.Args[3]
			}
			putFileInVM(os.Args[2], remotePath)
			return

		case "get":
			if len(os.Args) < 3 {
				usageAndExit("ork get <remote-path> [local-file]")
			}
			localPath := ""
			if len(os.Args) > 3 {
				localPath = os.Args[3]
			}
			getFileFromVM(os.Args[2], localPath)
			return

		case "stop":
			stopDaemonVM()
			return

		default:
			printUsage()
			return
		}
	}

	launchVM()
}

func usageAndExit(command string) {
	fmt.Println("Usage:")
	fmt.Printf("  %s\n", command)
	os.Exit(1)
}

func parseDiskSize(value string) (int64, error) {
	value = strings.TrimSpace(strings.ToUpper(value))
	if value == "" {
		return 0, errors.New("empty size")
	}

	multiplier := int64(1)

	switch {
	case strings.HasSuffix(value, "G"):
		multiplier = 1024 * 1024 * 1024
		value = strings.TrimSuffix(value, "G")
	case strings.HasSuffix(value, "M"):
		multiplier = 1024 * 1024
		value = strings.TrimSuffix(value, "M")
	case strings.HasSuffix(value, "K"):
		multiplier = 1024
		value = strings.TrimSuffix(value, "K")
	}

	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric value: %w", err)
	}

	if number <= 0 {
		return 0, errors.New("size must be greater than zero")
	}

	size := number * multiplier

	const minimumSize = 64 * 1024 * 1024
	if size < minimumSize {
		return 0, fmt.Errorf("size too small: recommended minimum is 64M")
	}

	return size, nil
}

func ensureParentDir(filePath string, mode os.FileMode) error {
	dir := filepath.Dir(filePath)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, mode)
}

func createWorkspaceDisk(sizeStr string) {
	cfg := mustLoadConfig()
	publicKey, err := ensureSSHKeyPair(cfg.Exec.KeyPath)
	if err != nil {
		log.Fatalf("[ORK] Could not prepare SSH key: %v", err)
	}

	if err := createExt4Workspace(cfg.Workspace.Path, sizeStr, publicKey); err != nil {
		log.Fatalf("[ORK] Could not create workspace disk: %v", err)
	}

	absPath, _ := filepath.Abs(cfg.Workspace.Path)
	fmt.Printf("[ORK] ext4 workspace ready: %s\n", absPath)
}

func ensureWorkspaceReady(cfg Config) {
	publicKey, err := ensureSSHKeyPair(cfg.Exec.KeyPath)
	if err != nil {
		log.Fatalf("[ORK] Could not prepare SSH key: %v", err)
	}

	if _, err := os.Stat(cfg.Workspace.Path); os.IsNotExist(err) {
		fmt.Printf("[ORK Warning] Workspace %s does not exist. Creating a %s ext4 workspace automatically...\n", cfg.Workspace.Path, cfg.Workspace.DefaultSize)
		if err := createExt4Workspace(cfg.Workspace.Path, cfg.Workspace.DefaultSize, publicKey); err != nil {
			log.Fatalf("[ORK] Could not create workspace disk: %v", err)
		}
	} else if err != nil {
		log.Fatalf("[ORK] Could not access workspace disk %s: %v", cfg.Workspace.Path, err)
	}
}

func createExt4Workspace(workspacePath string, sizeStr string, publicKey string) error {
	size, err := parseDiskSize(sizeStr)
	if err != nil {
		return fmt.Errorf("invalid workspace size: %w", err)
	}

	absPath, err := filepath.Abs(workspacePath)
	if err != nil {
		return fmt.Errorf("resolve workspace path: %w", err)
	}

	if _, err := os.Stat(absPath); err == nil {
		return fmt.Errorf("workspace disk %s already exists", absPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check workspace disk: %w", err)
	}

	if err := ensureParentDir(absPath, 0o755); err != nil {
		return fmt.Errorf("create workspace parent directory: %w", err)
	}

	virtualDisk, err := diskfs.Create(absPath, size, diskfs.SectorSizeDefault)
	if err != nil {
		return fmt.Errorf("raw image creation failed: %w", err)
	}

	_, err = virtualDisk.CreateFilesystem(disk.FilesystemSpec{
		Partition:   0,
		FSType:      filesystem.TypeExt4,
		VolumeLabel: "ORKWORK",
	})
	if err != nil {
		_ = virtualDisk.Close()
		_ = os.Remove(absPath)
		return fmt.Errorf("ext4 formatting failed: %w", err)
	}
	if err := virtualDisk.Close(); err != nil {
		_ = os.Remove(absPath)
		return fmt.Errorf("close workspace disk: %w", err)
	}

	if err := writeWorkspaceAuthorizedKey(absPath, publicKey); err != nil {
		_ = os.Remove(absPath)
		return err
	}

	return nil
}

func writeWorkspaceAuthorizedKey(workspacePath string, publicKey string) error {
	virtualDisk, err := diskfs.Open(workspacePath, diskfs.WithOpenMode(diskfs.ReadWrite))
	if err != nil {
		return fmt.Errorf("open workspace disk: %w", err)
	}
	defer virtualDisk.Close()

	fs, err := virtualDisk.GetFilesystem(0)
	if err != nil {
		return fmt.Errorf("open ext4 filesystem: %w", err)
	}
	defer fs.Close()

	if err := fs.Mkdir(".ork"); err != nil && !isAlreadyExistsError(err) {
		return fmt.Errorf("create .ork directory: %w", err)
	}

	file, err := fs.OpenFile(".ork/authorized_keys", os.O_CREATE|os.O_RDWR|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("open authorized_keys: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, strings.NewReader(strings.TrimSpace(publicKey)+"\n")); err != nil {
		return fmt.Errorf("write authorized_keys: %w", err)
	}

	return nil
}

func isAlreadyExistsError(err error) bool {
	return errors.Is(err, os.ErrExist) || strings.Contains(strings.ToLower(err.Error()), "exist")
}

func ensureSSHKeyPair(keyPath string) (string, error) {
	publicPath := keyPath + ".pub"
	if privateInfo, privateErr := os.Stat(keyPath); privateErr == nil && !privateInfo.IsDir() {
		if publicBytes, err := os.ReadFile(publicPath); err == nil && strings.TrimSpace(string(publicBytes)) != "" {
			return strings.TrimSpace(string(publicBytes)), nil
		}
		return "", fmt.Errorf("private key %s exists but public key %s is missing", keyPath, publicPath)
	} else if privateErr != nil && !os.IsNotExist(privateErr) {
		return "", privateErr
	}

	if err := ensureParentDir(keyPath, 0o700); err != nil {
		return "", err
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return "", err
	}

	privateBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateBytes,
	})

	if err := os.WriteFile(keyPath, privatePEM, 0o600); err != nil {
		return "", err
	}
	_ = os.Chmod(keyPath, 0o600)

	pubKey := marshalAuthorizedRSAKey(&privateKey.PublicKey)
	if err := os.WriteFile(publicPath, []byte(pubKey+"\n"), 0o644); err != nil {
		return "", err
	}

	return pubKey, nil
}

func marshalAuthorizedRSAKey(publicKey *rsa.PublicKey) string {
	var payload bytes.Buffer
	writeSSCString(&payload, "ssh-rsa")
	writeSSCBigInt(&payload, big.NewInt(int64(publicKey.E)))
	writeSSCBigInt(&payload, publicKey.N)
	return "ssh-rsa " + base64.StdEncoding.EncodeToString(payload.Bytes()) + " ork"
}

func writeSSCString(buffer *bytes.Buffer, value string) {
	_ = binary.Write(buffer, binary.BigEndian, uint32(len(value)))
	buffer.WriteString(value)
}

func writeSSCBigInt(buffer *bytes.Buffer, value *big.Int) {
	bytesValue := value.Bytes()
	if len(bytesValue) > 0 && bytesValue[0]&0x80 != 0 {
		bytesValue = append([]byte{0}, bytesValue...)
	}
	_ = binary.Write(buffer, binary.BigEndian, uint32(len(bytesValue)))
	buffer.Write(bytesValue)
}

func resolveQEMUBinary(cfg VMConfig, goos string, lookPath func(string) (string, error)) (string, error) {
	qemuPath := strings.TrimSpace(cfg.QEMUPath)
	if qemuPath != "" {
		return qemuPath, nil
	}

	candidates := []string{"qemu-system-x86_64"}
	if goos == "windows" {
		candidates = append(candidates, "qemu-system-x86_64.exe")
	}

	var lastErr error
	for _, candidate := range candidates {
		resolved, err := lookPath(candidate)
		if err == nil {
			return resolved, nil
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = exec.ErrNotFound
	}
	return "", fmt.Errorf("qemu-system-x86_64 was not found on PATH: %w", lastErr)
}

func resolveLinuxKitBinary(cfg ImageConfig, goos string, lookPath func(string) (string, error)) (string, error) {
	linuxKitPath := strings.TrimSpace(cfg.LinuxKitPath)
	if linuxKitPath != "" {
		return linuxKitPath, nil
	}

	candidates := []string{"linuxkit"}
	if goos == "windows" {
		candidates = append(candidates, "linuxkit.exe")
	}

	var lastErr error
	for _, candidate := range candidates {
		resolved, err := lookPath(candidate)
		if err == nil {
			return resolved, nil
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = exec.ErrNotFound
	}
	return "", fmt.Errorf("linuxkit was not found on PATH: %w", lastErr)
}

func resolveAccelerator(configured string, goos string, stat func(string) (os.FileInfo, error)) string {
	accelerator := strings.TrimSpace(configured)
	if accelerator != "" && !strings.EqualFold(accelerator, "auto") {
		return accelerator
	}

	switch goos {
	case "windows":
		return "whpx"
	case "linux":
		if _, err := stat("/dev/kvm"); err == nil {
			return "kvm"
		}
		return "tcg"
	case "darwin":
		return "hvf"
	default:
		return "tcg"
	}
}

func buildImage(args []string) {
	cfg := mustLoadConfig()

	template := cfg.Image.Template
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		template = args[0]
	}

	name := cfg.Image.Name
	if len(args) > 1 && strings.TrimSpace(args[1]) != "" {
		name = args[1]
	}

	linuxKitPath, err := resolveLinuxKitBinary(cfg.Image, runtime.GOOS, exec.LookPath)
	if err != nil {
		log.Fatalf("[ORK] LinuxKit not found. Install linuxkit and make it available on PATH, or set image.linuxkit_path in %s: %v", configFileName, err)
	}

	cmd := exec.Command(
		linuxKitPath,
		"build",
		"--format", cfg.Image.Format,
		"--name", name,
		template,
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("[ORK] Building LinuxKit image %q from %s...\n", name, template)
	if err := cmd.Run(); err != nil {
		log.Fatalf("[ORK] LinuxKit image build failed: %v", err)
	}
}

func buildQEMUArgs(cfg Config, daemon bool) []string {
	accelerator := resolveAccelerator(cfg.VM.Accelerator, runtime.GOOS, os.Stat)
	args := []string{
		"-accel", accelerator,
		"-m", cfg.VM.Memory,
		"-kernel", cfg.VM.Kernel,
		"-initrd", cfg.VM.Initrd,
	}

	cmdlineBytes, err := os.ReadFile(cfg.VM.Cmdline)
	if err != nil {
		log.Fatalf("Error reading %s: %v", cfg.VM.Cmdline, err)
	}
	cmdline := strings.TrimSpace(string(cmdlineBytes))
	args = append(args,
		"-append", cmdline,
		"-nographic",
		"-no-reboot",
		"-drive", "file="+cfg.Workspace.Path+",if=virtio,format=raw",
		"-netdev", fmt.Sprintf("user,id=net0,hostfwd=tcp:%s:%d-:22", cfg.Exec.Host, cfg.Exec.SSHPort),
		"-device", "virtio-net-pci,netdev=net0",
	)

	return args
}

func startDaemonVM() {
	cfg := mustLoadConfig()
	ensureWorkspaceReady(cfg)

	if state, err := readVMState(cfg.Exec.StatePath); err == nil {
		if err := waitForSSH(cfg, 2*time.Second); err == nil {
			fmt.Printf("[ORK] VM already running: pid=%d ssh=%s:%d\n", state.PID, state.SSHHost, state.SSHPort)
			return
		}
		_ = os.Remove(cfg.Exec.StatePath)
	}

	qemuPath, err := resolveQEMUBinary(cfg.VM, runtime.GOOS, exec.LookPath)
	if err != nil {
		log.Fatalf("[ORK] QEMU not found. Install qemu-system-x86_64 and make it available on PATH, or set vm.qemu_path in %s: %v", configFileName, err)
	}

	if err := ensureParentDir(cfg.Exec.StatePath, 0o700); err != nil {
		log.Fatalf("[ORK] Could not create state directory: %v", err)
	}

	logPath := filepath.Join(filepath.Dir(cfg.Exec.StatePath), "vm.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatalf("[ORK] Could not open VM log %s: %v", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(qemuPath, buildQEMUArgs(cfg, true)...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		log.Fatalf("[ORK] Could not start QEMU daemon: %v", err)
	}

	state := VMState{
		PID:           cmd.Process.Pid,
		SSHHost:       cfg.Exec.Host,
		SSHPort:       cfg.Exec.SSHPort,
		WorkspacePath: cfg.Workspace.Path,
		StartedAt:     time.Now(),
	}
	if err := writeVMState(cfg.Exec.StatePath, state); err != nil {
		_ = cmd.Process.Kill()
		log.Fatalf("[ORK] Could not write VM state: %v", err)
	}

	timeout, err := time.ParseDuration(cfg.Exec.ReadyTimeout)
	if err != nil {
		timeout = 60 * time.Second
	}

	fmt.Printf("[ORK] VM daemon started: pid=%d ssh=%s:%d\n", state.PID, state.SSHHost, state.SSHPort)
	fmt.Printf("[ORK] VM log: %s\n", logPath)
	if err := waitForSSH(cfg, timeout); err != nil {
		_ = cmd.Process.Kill()
		_ = os.Remove(cfg.Exec.StatePath)
		log.Fatalf("[ORK] VM did not become SSH-ready: %v", err)
	}
	fmt.Println("[ORK] VM is SSH-ready.")
}

func launchVM() {
	cfg := mustLoadConfig()
	ensureWorkspaceReady(cfg)

	qemuPath, err := resolveQEMUBinary(cfg.VM, runtime.GOOS, exec.LookPath)
	if err != nil {
		log.Fatalf("[ORK] QEMU not found. Install qemu-system-x86_64 and make it available on PATH, or set vm.qemu_path in %s: %v", configFileName, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd := exec.CommandContext(ctx, qemuPath, buildQEMUArgs(cfg, false)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("[ORK] Starting the LinuxKit micro-VM...")
	fmt.Println("------------------------------------------------------------")
	fmt.Println("To close QEMU: Ctrl+A, then X")
	fmt.Println("Or press Ctrl+C to terminate it from the orchestrator.")
	fmt.Println("------------------------------------------------------------")

	err = cmd.Run()

	fmt.Println()
	fmt.Println("------------------------------------------------------------")

	switch {
	case ctx.Err() != nil:
		fmt.Println("[ORK] Shutdown requested by the user.")
	case err == nil:
		fmt.Println("[ORK] The micro-VM exited successfully.")
	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			fmt.Printf("[ORK] QEMU exited with code %d.\n", exitErr.ExitCode())
		} else {
			log.Fatalf("[ORK] Error while running the VM: %v", err)
		}
	}
}

func readVMState(statePath string) (VMState, error) {
	var state VMState
	content, err := os.ReadFile(statePath)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(content, &state); err != nil {
		return state, err
	}
	return state, nil
}

func writeVMState(statePath string, state VMState) error {
	if err := ensureParentDir(statePath, 0o700); err != nil {
		return err
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath, append(content, '\n'), 0o644)
}

func waitForSSH(cfg Config, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	var lastOutput bytes.Buffer
	for {
		lastOutput.Reset()
		err := runSSHCommand(cfg, []string{"true"}, "", nil, &lastOutput, &lastOutput)
		if err == nil {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			output := strings.TrimSpace(lastOutput.String())
			if output != "" {
				return fmt.Errorf("%w: %s", lastErr, output)
			}
			return lastErr
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func execInVM(args []string) {
	cfg := mustLoadConfig()
	if _, err := readVMState(cfg.Exec.StatePath); err != nil {
		log.Fatalf("[ORK] No daemon VM state found. Start it with: ork start --daemon")
	}

	workdir, commandArgs := parseExecArgs(args, cfg.Workspace.Mount)
	if len(commandArgs) == 0 {
		usageAndExit("ork exec [--workdir /workspace] -- <command> [args...]")
	}

	err := runSSHCommand(cfg, commandArgs, workdir, os.Stdin, os.Stdout, os.Stderr)
	if err == nil {
		return
	}

	var exitErr *gossh.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitStatus())
	}
	log.Fatalf("[ORK] SSH command failed: %v", err)
}

func parseExecArgs(args []string, defaultWorkdir string) (string, []string) {
	workdir := defaultWorkdir
	for len(args) > 0 {
		switch args[0] {
		case "--":
			return workdir, args[1:]
		case "--workdir", "-C":
			if len(args) < 2 {
				usageAndExit("ork exec --workdir <dir> -- <command> [args...]")
			}
			workdir = args[1]
			args = args[2:]
		default:
			return workdir, args
		}
	}
	return workdir, nil
}

func dialSSH(cfg Config) (*gossh.Client, error) {
	keyData, err := os.ReadFile(cfg.Exec.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("read SSH key: %w", err)
	}
	signer, err := gossh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parse SSH key: %w", err)
	}
	return gossh.Dial("tcp",
		fmt.Sprintf("%s:%d", cfg.Exec.Host, cfg.Exec.SSHPort),
		&gossh.ClientConfig{
			User:            cfg.Exec.User,
			Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
			HostKeyCallback: gossh.InsecureIgnoreHostKey(),
			Timeout:         3 * time.Second,
		})
}

func runSSHCommand(cfg Config, commandArgs []string, workdir string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	client, err := dialSSH(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	session.Stdin = stdin
	session.Stdout = stdout
	session.Stderr = stderr

	return session.Run(remoteShellCommand(commandArgs, workdir))
}

func putFileInVM(localPath string, remotePath string) {
	cfg := mustLoadConfig()
	if _, err := readVMState(cfg.Exec.StatePath); err != nil {
		log.Fatalf("[ORK] No daemon VM state found. Start it with: ork start --daemon")
	}

	if remotePath == "" {
		remotePath = cfg.Workspace.Mount + "/" + filepath.Base(localPath)
	}

	f, err := os.Open(localPath)
	if err != nil {
		log.Fatalf("[ORK] Cannot open %s: %v", localPath, err)
	}
	defer f.Close()

	client, err := dialSSH(cfg)
	if err != nil {
		log.Fatalf("[ORK] SSH connection failed: %v", err)
	}
	defer client.Close()

	mkSession, err := client.NewSession()
	if err != nil {
		log.Fatalf("[ORK] SSH session failed: %v", err)
	}
	_ = mkSession.Run("mkdir -p " + shellQuote(path.Dir(remotePath)))
	mkSession.Close()

	upSession, err := client.NewSession()
	if err != nil {
		log.Fatalf("[ORK] SSH session failed: %v", err)
	}
	defer upSession.Close()

	upSession.Stdin = f
	upSession.Stderr = os.Stderr
	if err := upSession.Run("cat > " + shellQuote(remotePath)); err != nil {
		log.Fatalf("[ORK] Upload failed: %v", err)
	}

	fmt.Printf("[ORK] %s → %s\n", localPath, remotePath)
}

func getFileFromVM(remotePath string, localPath string) {
	cfg := mustLoadConfig()
	if _, err := readVMState(cfg.Exec.StatePath); err != nil {
		log.Fatalf("[ORK] No daemon VM state found. Start it with: ork start --daemon")
	}

	if localPath == "" {
		localPath = filepath.Base(remotePath)
	}

	f, err := os.Create(localPath)
	if err != nil {
		log.Fatalf("[ORK] Cannot create %s: %v", localPath, err)
	}

	if err := runSSHCommand(cfg, []string{"cat", remotePath}, "", nil, f, os.Stderr); err != nil {
		_ = f.Close()
		_ = os.Remove(localPath)
		log.Fatalf("[ORK] Download failed: %v", err)
	}
	_ = f.Close()

	fmt.Printf("[ORK] %s → %s\n", remotePath, localPath)
}

func remoteShellCommand(commandArgs []string, workdir string) string {
	quoted := make([]string, 0, len(commandArgs))
	for _, arg := range commandArgs {
		quoted = append(quoted, shellQuote(arg))
	}

	command := strings.Join(quoted, " ")
	if strings.TrimSpace(workdir) == "" {
		return "exec " + command
	}
	return "cd " + shellQuote(workdir) + " && exec " + command
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func stopDaemonVM() {
	cfg := mustLoadConfig()
	state, err := readVMState(cfg.Exec.StatePath)
	if err != nil {
		log.Fatalf("[ORK] No daemon VM state found.")
	}

	_ = runSSHCommand(cfg, []string{"poweroff"}, "", nil, os.Stdout, os.Stderr)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := waitForSSH(cfg, 500*time.Millisecond); err != nil {
			_ = os.Remove(cfg.Exec.StatePath)
			fmt.Println("[ORK] VM stopped.")
			return
		}
		time.Sleep(500 * time.Millisecond)
	}

	process, err := os.FindProcess(state.PID)
	if err == nil {
		_ = process.Kill()
	}
	_ = os.Remove(cfg.Exec.StatePath)
	fmt.Println("[ORK] VM stopped.")
}
