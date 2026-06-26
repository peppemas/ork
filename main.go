package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
)

const configFileName = "ork.json"

type Config struct {
	Disk  DiskConfig  `json:"disk"`
	VM    VMConfig    `json:"vm"`
	Image ImageConfig `json:"image"`
}

type DiskConfig struct {
	Path        string `json:"path"`
	DefaultSize string `json:"default_size"`
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

func defaultConfig() Config {
	return Config{
		Disk: DiskConfig{
			Path:        "data.img",
			DefaultSize: "5G",
		},
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

	if strings.TrimSpace(cfg.Disk.Path) == "" {
		cfg.Disk.Path = defaults.Disk.Path
	}
	if strings.TrimSpace(cfg.Disk.DefaultSize) == "" {
		cfg.Disk.DefaultSize = defaults.Disk.DefaultSize
	}
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
	fmt.Println("  ork create [size]               -> Create a virtual disk")
	fmt.Println("  ork build-image [template] [name] -> Build the LinuxKit kernel/initrd")
	fmt.Println("  ork start                       -> Start the micro-VM")
	fmt.Println("  ork ls [dir]                    -> List a directory in data.img")
	fmt.Println("  ork tree [dir]                  -> Recursively list a directory")
	fmt.Println("  ork stat <path>                 -> Show path information")
	fmt.Println("  ork read <file>                 -> Read a file from data.img")
	fmt.Println("  ork write <file> <text>         -> Write a file in data.img")
	fmt.Println("  ork append <file> <text>        -> Append text to a file")
	fmt.Println("  ork put <host-file> <file>      -> Copy a host file into data.img")
	fmt.Println("  ork get <file> [host-file]      -> Copy a file from data.img to the host")
	fmt.Println("  ork cp <src> <dst>              -> Copy a file inside data.img")
	fmt.Println("  ork touch <file>                -> Create or update an empty file")
	fmt.Println("  ork mkdir <dir>                 -> Create a directory")
	fmt.Println("  ork rm [-r] <path>              -> Remove a file or directory")
	fmt.Println("  ork mv <src> <dst>              -> Rename or move a path")
	fmt.Println("  ork label [name]                -> Read or set the FAT32 label")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  ork create 2G")
	fmt.Println("  ork start")
	fmt.Println("  ork ls /")
	fmt.Println("  ork put ./hello.txt /hello.txt")
	fmt.Println("  ork read /test.txt")
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "create":
			size := mustLoadConfig().Disk.DefaultSize
			if len(os.Args) > 2 {
				size = os.Args[2]
			}

			createVirtualDisk(size)
			return

		case "read":
			if len(os.Args) < 3 {
				usageAndExit("ork read <file-path>")
			}

			readVirtualDiskFile(os.Args[2])
			return

		case "ls":
			dirPath := "/"
			if len(os.Args) > 2 {
				dirPath = os.Args[2]
			}

			listVirtualDiskDir(dirPath)
			return

		case "tree":
			dirPath := "/"
			if len(os.Args) > 2 {
				dirPath = os.Args[2]
			}

			treeVirtualDiskDir(dirPath)
			return

		case "stat":
			if len(os.Args) < 3 {
				usageAndExit("ork stat <path>")
			}

			statVirtualDiskPath(os.Args[2])
			return

		case "write":
			if len(os.Args) < 4 {
				usageAndExit("ork write <file-path> <text>")
			}

			writeVirtualDiskFile(os.Args[2], []byte(strings.Join(os.Args[3:], " ")), false)
			return

		case "append":
			if len(os.Args) < 4 {
				usageAndExit("ork append <file-path> <text>")
			}

			writeVirtualDiskFile(os.Args[2], []byte(strings.Join(os.Args[3:], " ")), true)
			return

		case "put":
			if len(os.Args) < 4 {
				usageAndExit("ork put <host-file> <file-path>")
			}

			putHostFile(os.Args[2], os.Args[3])
			return

		case "get":
			if len(os.Args) < 3 {
				usageAndExit("ork get <file-path> [host-file]")
			}

			hostPath := ""
			if len(os.Args) > 3 {
				hostPath = os.Args[3]
			}

			getVirtualDiskFile(os.Args[2], hostPath)
			return

		case "cp":
			if len(os.Args) < 4 {
				usageAndExit("ork cp <source> <destination>")
			}

			copyVirtualDiskFile(os.Args[2], os.Args[3])
			return

		case "touch":
			if len(os.Args) < 3 {
				usageAndExit("ork touch <file-path>")
			}

			touchVirtualDiskFile(os.Args[2])
			return

		case "mkdir":
			if len(os.Args) < 3 {
				usageAndExit("ork mkdir <directory-path>")
			}

			makeVirtualDiskDir(os.Args[2])
			return

		case "rm":
			if len(os.Args) < 3 {
				usageAndExit("ork rm [-r] <path>")
			}

			recursive := false
			targetPath := os.Args[2]
			if os.Args[2] == "-r" || os.Args[2] == "--recursive" {
				if len(os.Args) < 4 {
					usageAndExit("ork rm -r <path>")
				}
				recursive = true
				targetPath = os.Args[3]
			}

			removeVirtualDiskPath(targetPath, recursive)
			return

		case "mv":
			if len(os.Args) < 4 {
				usageAndExit("ork mv <source> <destination>")
			}

			renameVirtualDiskPath(os.Args[2], os.Args[3])
			return

		case "label":
			label := ""
			if len(os.Args) > 2 {
				label = strings.Join(os.Args[2:], " ")
			}

			labelVirtualDisk(label)
			return

		case "build-image":
			buildImage(os.Args[2:])
			return

		case "start":
			launchVM()
			return

		default:
			printUsage()
			return
		}
	}

	launchVM()
}

func readVirtualDiskFile(filePath string) {
	cfg := mustLoadConfig()

	absDiskPath, err := filepath.Abs(cfg.Disk.Path)
	if err != nil {
		log.Fatalf(
			"[ORK] Could not resolve the disk path: %v",
			err,
		)
	}

	if _, err := os.Stat(absDiskPath); err != nil {
		if os.IsNotExist(err) {
			log.Fatalf(
				"[ORK] Disk %s does not exist.",
				absDiskPath,
			)
		}

		log.Fatalf(
			"[ORK] Could not access disk %s: %v",
			absDiskPath,
			err,
		)
	}

	// FAT filesystem paths are treated as absolute.
	filePath = strings.TrimSpace(filePath)

	if filePath == "" {
		log.Fatal("[ORK] The file path is empty.")
	}

	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}

	virtualDisk, err := diskfs.Open(absDiskPath)
	if err != nil {
		log.Fatalf(
			"[ORK] Could not open disk %s: %v",
			absDiskPath,
			err,
		)
	}

	fs, err := virtualDisk.GetFilesystem(0)
	if err != nil {
		log.Fatalf(
			"[ORK] Could not open the FAT32 filesystem: %v",
			err,
		)
	}

	file, err := fs.OpenFile(filePath, os.O_RDONLY)
	if err != nil {
		log.Fatalf(
			"[ORK] Could not open %s inside the disk: %v",
			filePath,
			err,
		)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		log.Fatalf(
			"[ORK] Could not read %s: %v",
			filePath,
			err,
		)
	}

	fmt.Printf("[ORK] Disk: %s\n", absDiskPath)
	fmt.Printf("[ORK] File:  %s\n", filePath)
	fmt.Println("------------------------------------------------------------")
	fmt.Print(string(content))

	if len(content) > 0 && content[len(content)-1] != '\n' {
		fmt.Println()
	}

	fmt.Println("------------------------------------------------------------")
}

func usageAndExit(command string) {
	fmt.Println("Usage:")
	fmt.Printf("  %s\n", command)
	os.Exit(1)
}

func openVirtualDiskFilesystem() (filesystem.FileSystem, string) {
	cfg := mustLoadConfig()

	absDiskPath, err := filepath.Abs(cfg.Disk.Path)
	if err != nil {
		log.Fatalf(
			"[ORK] Could not resolve the disk path: %v",
			err,
		)
	}

	if _, err := os.Stat(absDiskPath); err != nil {
		if os.IsNotExist(err) {
			log.Fatalf(
				"[ORK] Disk %s does not exist.",
				absDiskPath,
			)
		}

		log.Fatalf(
			"[ORK] Could not access disk %s: %v",
			absDiskPath,
			err,
		)
	}

	virtualDisk, err := diskfs.Open(absDiskPath, diskfs.WithOpenMode(diskfs.ReadWrite))
	if err != nil {
		log.Fatalf(
			"[ORK] Could not open disk %s: %v",
			absDiskPath,
			err,
		)
	}

	fs, err := virtualDisk.GetFilesystem(0)
	if err != nil {
		log.Fatalf(
			"[ORK] Could not open the FAT32 filesystem: %v",
			err,
		)
	}

	return fs, absDiskPath
}

func cleanVirtualPath(input string) string {
	filePath := strings.TrimSpace(strings.ReplaceAll(input, "\\", "/"))
	if filePath == "" {
		log.Fatal("[ORK] The path is empty.")
	}

	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}

	filePath = path.Clean(filePath)
	if filePath == "." {
		return "/"
	}
	return filePath
}

func fatDirectoryPath(filePath string) string {
	if filePath == "/" {
		return "."
	}
	return strings.TrimPrefix(filePath, "/")
}

func listVirtualDiskDir(dirPath string) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	dirPath = cleanVirtualPath(dirPath)
	entries, err := fs.ReadDir(fatDirectoryPath(dirPath))
	if err != nil {
		log.Fatalf("[ORK] Could not list %s: %v", dirPath, err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})

	fmt.Printf("[ORK] Disk:      %s\n", absDiskPath)
	fmt.Printf("[ORK] Directory: %s\n", dirPath)
	fmt.Println("------------------------------------------------------------")
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			log.Fatalf("[ORK] Could not read information for %s: %v", entry.Name(), err)
		}

		kind := "file"
		if entry.IsDir() {
			kind = "dir "
		}

		fmt.Printf("%s %12d %s\n", kind, info.Size(), entry.Name())
	}
	fmt.Println("------------------------------------------------------------")
}

func treeVirtualDiskDir(rootPath string) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	rootPath = cleanVirtualPath(rootPath)
	fmt.Printf("[ORK] Disk: %s\n", absDiskPath)
	fmt.Printf("[ORK] Tree:  %s\n", rootPath)
	fmt.Println("------------------------------------------------------------")
	walkVirtualTree(fs, rootPath, "")
	fmt.Println("------------------------------------------------------------")
}

func walkVirtualTree(fs filesystem.FileSystem, dirPath string, indent string) {
	entries, err := fs.ReadDir(fatDirectoryPath(dirPath))
	if err != nil {
		log.Fatalf("[ORK] Could not list %s: %v", dirPath, err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})

	for _, entry := range entries {
		name := entry.Name()
		fmt.Printf("%s%s\n", indent, name)
		if entry.IsDir() {
			walkVirtualTree(fs, path.Join(dirPath, name), indent+"  ")
		}
	}
}

func statVirtualDiskPath(targetPath string) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	targetPath = cleanVirtualPath(targetPath)
	info, err := fs.Stat(fatDirectoryPath(targetPath))
	if err != nil {
		log.Fatalf("[ORK] Could not stat %s: %v", targetPath, err)
	}

	fmt.Printf("[ORK] Disk: %s\n", absDiskPath)
	fmt.Printf("Path:    %s\n", targetPath)
	fmt.Printf("Name:    %s\n", info.Name())
	fmt.Printf("Type:    %s\n", fileKind(info.IsDir()))
	fmt.Printf("Size:    %d\n", info.Size())
	fmt.Printf("Mode:    %s\n", info.Mode())
	fmt.Printf("ModTime: %s\n", info.ModTime().Format("2006-01-02 15:04:05"))
}

func fileKind(isDir bool) string {
	if isDir {
		return "directory"
	}
	return "file"
}

func writeVirtualDiskFile(filePath string, content []byte, appendMode bool) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	filePath = cleanVirtualPath(filePath)
	flags := os.O_CREATE | os.O_RDWR | os.O_TRUNC
	action := "Wrote"
	if appendMode {
		flags = os.O_CREATE | os.O_RDWR | os.O_APPEND
		action = "Updated"
	}

	file, err := fs.OpenFile(filePath, flags)
	if err != nil {
		log.Fatalf("[ORK] Could not open %s for writing: %v", filePath, err)
	}
	defer file.Close()

	if _, err := io.Copy(file, bytes.NewReader(content)); err != nil {
		log.Fatalf("[ORK] Could not write %s: %v", filePath, err)
	}

	fmt.Printf("[ORK] Disk: %s\n", absDiskPath)
	fmt.Printf("[ORK] %s %d byte in %s\n", action, len(content), filePath)
}

func putHostFile(hostPath string, diskPath string) {
	content, err := os.ReadFile(hostPath)
	if err != nil {
		log.Fatalf("[ORK] Could not read %s: %v", hostPath, err)
	}

	writeVirtualDiskFile(diskPath, content, false)
}

func getVirtualDiskFile(diskPath string, hostPath string) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	diskPath = cleanVirtualPath(diskPath)
	if strings.TrimSpace(hostPath) == "" {
		hostPath = path.Base(diskPath)
	}

	file, err := fs.OpenFile(diskPath, os.O_RDONLY)
	if err != nil {
		log.Fatalf("[ORK] Could not open %s inside the disk: %v", diskPath, err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		log.Fatalf("[ORK] Could not read %s: %v", diskPath, err)
	}

	if err := os.WriteFile(hostPath, content, 0o644); err != nil {
		log.Fatalf("[ORK] Could not write %s: %v", hostPath, err)
	}

	fmt.Printf("[ORK] Disk: %s\n", absDiskPath)
	fmt.Printf("[ORK] Copied %s -> %s (%d byte)\n", diskPath, hostPath, len(content))
}

func copyVirtualDiskFile(srcPath string, dstPath string) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	srcPath = cleanVirtualPath(srcPath)
	dstPath = cleanVirtualPath(dstPath)

	src, err := fs.OpenFile(srcPath, os.O_RDONLY)
	if err != nil {
		log.Fatalf("[ORK] Could not open %s: %v", srcPath, err)
	}
	defer src.Close()

	dst, err := fs.OpenFile(dstPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC)
	if err != nil {
		log.Fatalf("[ORK] Could not open %s for writing: %v", dstPath, err)
	}
	defer dst.Close()

	written, err := io.Copy(dst, src)
	if err != nil {
		log.Fatalf("[ORK] Could not copy %s to %s: %v", srcPath, dstPath, err)
	}

	fmt.Printf("[ORK] Disk: %s\n", absDiskPath)
	fmt.Printf("[ORK] Copied %s -> %s (%d byte)\n", srcPath, dstPath, written)
}

func touchVirtualDiskFile(filePath string) {
	writeVirtualDiskFile(filePath, nil, true)
}

func makeVirtualDiskDir(dirPath string) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	dirPath = cleanVirtualPath(dirPath)
	if err := fs.Mkdir(dirPath); err != nil {
		log.Fatalf("[ORK] Could not create directory %s: %v", dirPath, err)
	}

	fmt.Printf("[ORK] Disk: %s\n", absDiskPath)
	fmt.Printf("[ORK] Created directory: %s\n", dirPath)
}

func removeVirtualDiskPath(targetPath string, recursive bool) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	targetPath = cleanVirtualPath(targetPath)
	if targetPath == "/" {
		log.Fatal("[ORK] You cannot remove the disk root.")
	}

	if recursive {
		info, err := fs.Stat(fatDirectoryPath(targetPath))
		if err != nil {
			log.Fatalf("[ORK] Could not stat %s: %v", targetPath, err)
		}
		if info.IsDir() {
			if err := removeVirtualDiskTree(fs, targetPath); err != nil {
				log.Fatalf("[ORK] Could not recursively remove %s: %v", targetPath, err)
			}
			fmt.Printf("[ORK] Disk: %s\n", absDiskPath)
			fmt.Printf("[ORK] Recursively removed: %s\n", targetPath)
			return
		}
	}

	if err := fs.Remove(fatDirectoryPath(targetPath)); err != nil {
		log.Fatalf("[ORK] Could not remove %s: %v", targetPath, err)
	}

	fmt.Printf("[ORK] Disk: %s\n", absDiskPath)
	fmt.Printf("[ORK] Removed: %s\n", targetPath)
}

func removeVirtualDiskTree(fs filesystem.FileSystem, dirPath string) error {
	entries, err := fs.ReadDir(fatDirectoryPath(dirPath))
	if err != nil {
		return err
	}

	for _, entry := range entries {
		childPath := path.Join(dirPath, entry.Name())
		if entry.IsDir() {
			if err := removeVirtualDiskTree(fs, childPath); err != nil {
				return err
			}
			continue
		}

		if err := fs.Remove(fatDirectoryPath(childPath)); err != nil {
			return err
		}
	}

	return fs.Remove(fatDirectoryPath(dirPath))
}

func renameVirtualDiskPath(srcPath string, dstPath string) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	srcPath = cleanVirtualPath(srcPath)
	dstPath = cleanVirtualPath(dstPath)
	if srcPath == "/" || dstPath == "/" {
		log.Fatal("[ORK] You cannot rename the disk root.")
	}

	if err := fs.Rename(fatDirectoryPath(srcPath), fatDirectoryPath(dstPath)); err != nil {
		log.Fatalf("[ORK] Could not rename %s to %s: %v", srcPath, dstPath, err)
	}

	fmt.Printf("[ORK] Disk: %s\n", absDiskPath)
	fmt.Printf("[ORK] Renamed %s -> %s\n", srcPath, dstPath)
}

func labelVirtualDisk(label string) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	label = strings.TrimSpace(label)
	if label == "" {
		fmt.Printf("[ORK] Disk: %s\n", absDiskPath)
		fmt.Printf("[ORK] Label: %q\n", strings.TrimSpace(fs.Label()))
		return
	}

	if err := fs.SetLabel(label); err != nil {
		log.Fatalf("[ORK] Could not set label %q: %v", label, err)
	}

	fmt.Printf("[ORK] Disk: %s\n", absDiskPath)
	fmt.Printf("[ORK] Label set: %q\n", label)
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

	// Very small FAT32 filesystems may be invalid or not useful.
	const minimumSize = 64 * 1024 * 1024
	if size < minimumSize {
		return 0, fmt.Errorf(
			"size too small: recommended minimum is 64M",
		)
	}

	return size, nil
}

// createVirtualDisk creates the configured disk image and formats it directly as FAT32.
func createVirtualDisk(sizeStr string) {
	cfg := mustLoadConfig()

	size, err := parseDiskSize(sizeStr)
	if err != nil {
		log.Fatalf("[ORK] Invalid disk size: %v", err)
	}

	absPath, err := filepath.Abs(cfg.Disk.Path)
	if err != nil {
		log.Fatalf("[ORK] Could not resolve the disk path: %v", err)
	}

	if _, err := os.Stat(absPath); err == nil {
		log.Fatalf(
			"[ORK] Disk %s already exists. Remove it explicitly before recreating it.",
			absPath,
		)
	} else if !os.IsNotExist(err) {
		log.Fatalf("[ORK] Could not check the disk: %v", err)
	}

	fmt.Printf(
		"[ORK] Creating FAT32 disk %s, size %s...\n",
		absPath,
		sizeStr,
	)

	virtualDisk, err := diskfs.Create(
		absPath,
		size,
		diskfs.SectorSizeDefault,
	)
	if err != nil {
		log.Fatalf("[ORK] RAW image creation failed: %v", err)
	}

	_, err = virtualDisk.CreateFilesystem(disk.FilesystemSpec{
		// Partition 0 means a filesystem over the whole disk, without MBR or GPT.
		Partition: 0,
		FSType:    filesystem.TypeFat32,
	})
	if err != nil {
		_ = os.Remove(absPath)
		log.Fatalf("[ORK] FAT32 formatting failed: %v", err)
	}

	fmt.Printf("[ORK] FAT32 disk ready: %s\n", absPath)
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

func launchVM() {
	cfg := mustLoadConfig()

	qemuPath, err := resolveQEMUBinary(cfg.VM, runtime.GOOS, exec.LookPath)
	if err != nil {
		log.Fatalf("[ORK] QEMU not found. Install qemu-system-x86_64 and make it available on PATH, or set vm.qemu_path in %s: %v", configFileName, err)
	}

	// Create the disk automatically if it does not exist.
	if _, err := os.Stat(cfg.Disk.Path); os.IsNotExist(err) {
		fmt.Printf("[ORK Warning] Disk %s does not exist. Creating a %s disk automatically...\n", cfg.Disk.Path, cfg.Disk.DefaultSize)
		createVirtualDisk(cfg.Disk.DefaultSize)
	} else if err != nil {
		log.Fatalf("[ORK] Could not access disk %s: %v", cfg.Disk.Path, err)
	}

	cmdlineBytes, err := os.ReadFile(cfg.VM.Cmdline)
	if err != nil {
		log.Fatalf("Error reading %s: %v", cfg.VM.Cmdline, err)
	}

	cmdline := strings.TrimSpace(string(cmdlineBytes))
	accelerator := resolveAccelerator(cfg.VM.Accelerator, runtime.GOOS, os.Stat)

	args := []string{
		"-accel", accelerator,
		"-m", cfg.VM.Memory,
		"-kernel", cfg.VM.Kernel,
		"-initrd", cfg.VM.Initrd,
		"-append", cmdline,
		"-nographic",
		"-no-reboot",
		// Attach the virtual disk as a RAW virtio block drive.
		"-drive", "file=" + cfg.Disk.Path + ",if=virtio,format=raw",
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd := exec.CommandContext(ctx, qemuPath, args...)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("[Go Orchestrator] Starting the LinuxKit micro-VM...")
	fmt.Println("------------------------------------------------------------")
	fmt.Println("To close QEMU: Ctrl+A, then X")
	fmt.Println("Or press Ctrl+C to terminate it from the orchestrator.")
	fmt.Println("------------------------------------------------------------")

	err = cmd.Run()

	fmt.Println()
	fmt.Println("------------------------------------------------------------")

	switch {
	case ctx.Err() != nil:
		fmt.Println("[Go Orchestrator] Shutdown requested by the user.")
	case err == nil:
		fmt.Println("[Go Orchestrator] The micro-VM exited successfully.")
	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			fmt.Printf("[Go Orchestrator] QEMU exited with code %d.\n", exitErr.ExitCode())
		} else {
			log.Fatalf("Error while running the VM: %v", err)
		}
	}
}
