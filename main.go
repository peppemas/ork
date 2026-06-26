package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
)

const (
	qemuDir  = `C:\Program Files\qemu`
	diskName = "./data.img"
	cmdlineF = "./test-disk-cmdline"
	kernelF  = "./test-disk-kernel"
	initrdF  = "./test-disk-initrd.img"
)

func printUsage() {
	fmt.Println("Uso di ORK:")
	fmt.Println("  ork create [dimensione]         -> Crea un disco virtuale")
	fmt.Println("  ork start                       -> Avvia la micro-VM")
	fmt.Println("  ork ls [dir]                    -> Lista una directory in data.img")
	fmt.Println("  ork tree [dir]                  -> Lista ricorsivamente una directory")
	fmt.Println("  ork stat <path>                 -> Mostra informazioni su un path")
	fmt.Println("  ork read <file>                 -> Legge un file da data.img")
	fmt.Println("  ork write <file> <testo>        -> Scrive un file in data.img")
	fmt.Println("  ork append <file> <testo>       -> Aggiunge testo a un file")
	fmt.Println("  ork put <host-file> <file>      -> Copia un file host dentro data.img")
	fmt.Println("  ork get <file> [host-file]      -> Copia un file da data.img all'host")
	fmt.Println("  ork cp <src> <dst>              -> Copia un file dentro data.img")
	fmt.Println("  ork touch <file>                -> Crea o aggiorna un file vuoto")
	fmt.Println("  ork mkdir <dir>                 -> Crea una directory")
	fmt.Println("  ork rm [-r] <path>              -> Rimuove un file o directory")
	fmt.Println("  ork mv <src> <dst>              -> Rinomina o sposta un path")
	fmt.Println("  ork label [nome]                -> Legge o imposta l'etichetta FAT32")
	fmt.Println()
	fmt.Println("Esempi:")
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
			size := "2G"
			if len(os.Args) > 2 {
				size = os.Args[2]
			}

			createVirtualDisk(size)
			return

		case "read":
			if len(os.Args) < 3 {
				usageAndExit("ork read <percorso-file>")
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
				usageAndExit("ork stat <percorso>")
			}

			statVirtualDiskPath(os.Args[2])
			return

		case "write":
			if len(os.Args) < 4 {
				usageAndExit("ork write <percorso-file> <testo>")
			}

			writeVirtualDiskFile(os.Args[2], []byte(strings.Join(os.Args[3:], " ")), false)
			return

		case "append":
			if len(os.Args) < 4 {
				usageAndExit("ork append <percorso-file> <testo>")
			}

			writeVirtualDiskFile(os.Args[2], []byte(strings.Join(os.Args[3:], " ")), true)
			return

		case "put":
			if len(os.Args) < 4 {
				usageAndExit("ork put <host-file> <percorso-file>")
			}

			putHostFile(os.Args[2], os.Args[3])
			return

		case "get":
			if len(os.Args) < 3 {
				usageAndExit("ork get <percorso-file> [host-file]")
			}

			hostPath := ""
			if len(os.Args) > 3 {
				hostPath = os.Args[3]
			}

			getVirtualDiskFile(os.Args[2], hostPath)
			return

		case "cp":
			if len(os.Args) < 4 {
				usageAndExit("ork cp <sorgente> <destinazione>")
			}

			copyVirtualDiskFile(os.Args[2], os.Args[3])
			return

		case "touch":
			if len(os.Args) < 3 {
				usageAndExit("ork touch <percorso-file>")
			}

			touchVirtualDiskFile(os.Args[2])
			return

		case "mkdir":
			if len(os.Args) < 3 {
				usageAndExit("ork mkdir <percorso-directory>")
			}

			makeVirtualDiskDir(os.Args[2])
			return

		case "rm":
			if len(os.Args) < 3 {
				usageAndExit("ork rm [-r] <percorso>")
			}

			recursive := false
			targetPath := os.Args[2]
			if os.Args[2] == "-r" || os.Args[2] == "--recursive" {
				if len(os.Args) < 4 {
					usageAndExit("ork rm -r <percorso>")
				}
				recursive = true
				targetPath = os.Args[3]
			}

			removeVirtualDiskPath(targetPath, recursive)
			return

		case "mv":
			if len(os.Args) < 4 {
				usageAndExit("ork mv <sorgente> <destinazione>")
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
	absDiskPath, err := filepath.Abs(diskName)
	if err != nil {
		log.Fatalf(
			"[ORK] Impossibile determinare il percorso del disco: %v",
			err,
		)
	}

	if _, err := os.Stat(absDiskPath); err != nil {
		if os.IsNotExist(err) {
			log.Fatalf(
				"[ORK] Il disco %s non esiste.",
				absDiskPath,
			)
		}

		log.Fatalf(
			"[ORK] Impossibile accedere al disco %s: %v",
			absDiskPath,
			err,
		)
	}

	// I percorsi nel filesystem FAT vengono trattati come assoluti.
	filePath = strings.TrimSpace(filePath)

	if filePath == "" {
		log.Fatal("[ORK] Il percorso del file è vuoto.")
	}

	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}

	virtualDisk, err := diskfs.Open(absDiskPath)
	if err != nil {
		log.Fatalf(
			"[ORK] Impossibile aprire il disco %s: %v",
			absDiskPath,
			err,
		)
	}

	fs, err := virtualDisk.GetFilesystem(0)
	if err != nil {
		log.Fatalf(
			"[ORK] Impossibile aprire il filesystem FAT32: %v",
			err,
		)
	}

	file, err := fs.OpenFile(filePath, os.O_RDONLY)
	if err != nil {
		log.Fatalf(
			"[ORK] Impossibile aprire %s dentro il disco: %v",
			filePath,
			err,
		)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		log.Fatalf(
			"[ORK] Impossibile leggere %s: %v",
			filePath,
			err,
		)
	}

	fmt.Printf("[ORK] Disco: %s\n", absDiskPath)
	fmt.Printf("[ORK] File:  %s\n", filePath)
	fmt.Println("------------------------------------------------------------")
	fmt.Print(string(content))

	if len(content) > 0 && content[len(content)-1] != '\n' {
		fmt.Println()
	}

	fmt.Println("------------------------------------------------------------")
}

func usageAndExit(command string) {
	fmt.Println("Uso:")
	fmt.Printf("  %s\n", command)
	os.Exit(1)
}

func openVirtualDiskFilesystem() (filesystem.FileSystem, string) {
	absDiskPath, err := filepath.Abs(diskName)
	if err != nil {
		log.Fatalf(
			"[ORK] Impossibile determinare il percorso del disco: %v",
			err,
		)
	}

	if _, err := os.Stat(absDiskPath); err != nil {
		if os.IsNotExist(err) {
			log.Fatalf(
				"[ORK] Il disco %s non esiste.",
				absDiskPath,
			)
		}

		log.Fatalf(
			"[ORK] Impossibile accedere al disco %s: %v",
			absDiskPath,
			err,
		)
	}

	virtualDisk, err := diskfs.Open(absDiskPath, diskfs.WithOpenMode(diskfs.ReadWrite))
	if err != nil {
		log.Fatalf(
			"[ORK] Impossibile aprire il disco %s: %v",
			absDiskPath,
			err,
		)
	}

	fs, err := virtualDisk.GetFilesystem(0)
	if err != nil {
		log.Fatalf(
			"[ORK] Impossibile aprire il filesystem FAT32: %v",
			err,
		)
	}

	return fs, absDiskPath
}

func cleanVirtualPath(input string) string {
	filePath := strings.TrimSpace(strings.ReplaceAll(input, "\\", "/"))
	if filePath == "" {
		log.Fatal("[ORK] Il percorso e' vuoto.")
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
		log.Fatalf("[ORK] Impossibile listare %s: %v", dirPath, err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})

	fmt.Printf("[ORK] Disco:     %s\n", absDiskPath)
	fmt.Printf("[ORK] Directory: %s\n", dirPath)
	fmt.Println("------------------------------------------------------------")
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			log.Fatalf("[ORK] Impossibile leggere informazioni per %s: %v", entry.Name(), err)
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
	fmt.Printf("[ORK] Disco: %s\n", absDiskPath)
	fmt.Printf("[ORK] Tree:  %s\n", rootPath)
	fmt.Println("------------------------------------------------------------")
	walkVirtualTree(fs, rootPath, "")
	fmt.Println("------------------------------------------------------------")
}

func walkVirtualTree(fs filesystem.FileSystem, dirPath string, indent string) {
	entries, err := fs.ReadDir(fatDirectoryPath(dirPath))
	if err != nil {
		log.Fatalf("[ORK] Impossibile listare %s: %v", dirPath, err)
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
		log.Fatalf("[ORK] Impossibile leggere stat di %s: %v", targetPath, err)
	}

	fmt.Printf("[ORK] Disco: %s\n", absDiskPath)
	fmt.Printf("Path:    %s\n", targetPath)
	fmt.Printf("Nome:    %s\n", info.Name())
	fmt.Printf("Tipo:    %s\n", fileKind(info.IsDir()))
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
	action := "Scritto"
	if appendMode {
		flags = os.O_CREATE | os.O_RDWR | os.O_APPEND
		action = "Aggiornato"
	}

	file, err := fs.OpenFile(filePath, flags)
	if err != nil {
		log.Fatalf("[ORK] Impossibile aprire %s in scrittura: %v", filePath, err)
	}
	defer file.Close()

	if _, err := io.Copy(file, bytes.NewReader(content)); err != nil {
		log.Fatalf("[ORK] Impossibile scrivere %s: %v", filePath, err)
	}

	fmt.Printf("[ORK] Disco: %s\n", absDiskPath)
	fmt.Printf("[ORK] %s %d byte in %s\n", action, len(content), filePath)
}

func putHostFile(hostPath string, diskPath string) {
	content, err := os.ReadFile(hostPath)
	if err != nil {
		log.Fatalf("[ORK] Impossibile leggere %s: %v", hostPath, err)
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
		log.Fatalf("[ORK] Impossibile aprire %s dentro il disco: %v", diskPath, err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		log.Fatalf("[ORK] Impossibile leggere %s: %v", diskPath, err)
	}

	if err := os.WriteFile(hostPath, content, 0o644); err != nil {
		log.Fatalf("[ORK] Impossibile scrivere %s: %v", hostPath, err)
	}

	fmt.Printf("[ORK] Disco: %s\n", absDiskPath)
	fmt.Printf("[ORK] Copiato %s -> %s (%d byte)\n", diskPath, hostPath, len(content))
}

func copyVirtualDiskFile(srcPath string, dstPath string) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	srcPath = cleanVirtualPath(srcPath)
	dstPath = cleanVirtualPath(dstPath)

	src, err := fs.OpenFile(srcPath, os.O_RDONLY)
	if err != nil {
		log.Fatalf("[ORK] Impossibile aprire %s: %v", srcPath, err)
	}
	defer src.Close()

	dst, err := fs.OpenFile(dstPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC)
	if err != nil {
		log.Fatalf("[ORK] Impossibile aprire %s in scrittura: %v", dstPath, err)
	}
	defer dst.Close()

	written, err := io.Copy(dst, src)
	if err != nil {
		log.Fatalf("[ORK] Impossibile copiare %s in %s: %v", srcPath, dstPath, err)
	}

	fmt.Printf("[ORK] Disco: %s\n", absDiskPath)
	fmt.Printf("[ORK] Copiato %s -> %s (%d byte)\n", srcPath, dstPath, written)
}

func touchVirtualDiskFile(filePath string) {
	writeVirtualDiskFile(filePath, nil, true)
}

func makeVirtualDiskDir(dirPath string) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	dirPath = cleanVirtualPath(dirPath)
	if err := fs.Mkdir(dirPath); err != nil {
		log.Fatalf("[ORK] Impossibile creare directory %s: %v", dirPath, err)
	}

	fmt.Printf("[ORK] Disco: %s\n", absDiskPath)
	fmt.Printf("[ORK] Directory creata: %s\n", dirPath)
}

func removeVirtualDiskPath(targetPath string, recursive bool) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	targetPath = cleanVirtualPath(targetPath)
	if targetPath == "/" {
		log.Fatal("[ORK] Non puoi rimuovere la root del disco.")
	}

	if recursive {
		info, err := fs.Stat(fatDirectoryPath(targetPath))
		if err != nil {
			log.Fatalf("[ORK] Impossibile leggere stat di %s: %v", targetPath, err)
		}
		if info.IsDir() {
			if err := removeVirtualDiskTree(fs, targetPath); err != nil {
				log.Fatalf("[ORK] Impossibile rimuovere ricorsivamente %s: %v", targetPath, err)
			}
			fmt.Printf("[ORK] Disco: %s\n", absDiskPath)
			fmt.Printf("[ORK] Rimosso ricorsivamente: %s\n", targetPath)
			return
		}
	}

	if err := fs.Remove(fatDirectoryPath(targetPath)); err != nil {
		log.Fatalf("[ORK] Impossibile rimuovere %s: %v", targetPath, err)
	}

	fmt.Printf("[ORK] Disco: %s\n", absDiskPath)
	fmt.Printf("[ORK] Rimosso: %s\n", targetPath)
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
		log.Fatal("[ORK] Non puoi rinominare la root del disco.")
	}

	if err := fs.Rename(fatDirectoryPath(srcPath), fatDirectoryPath(dstPath)); err != nil {
		log.Fatalf("[ORK] Impossibile rinominare %s in %s: %v", srcPath, dstPath, err)
	}

	fmt.Printf("[ORK] Disco: %s\n", absDiskPath)
	fmt.Printf("[ORK] Rinominato %s -> %s\n", srcPath, dstPath)
}

func labelVirtualDisk(label string) {
	fs, absDiskPath := openVirtualDiskFilesystem()
	defer fs.Close()

	label = strings.TrimSpace(label)
	if label == "" {
		fmt.Printf("[ORK] Disco: %s\n", absDiskPath)
		fmt.Printf("[ORK] Label: %q\n", strings.TrimSpace(fs.Label()))
		return
	}

	if err := fs.SetLabel(label); err != nil {
		log.Fatalf("[ORK] Impossibile impostare label %q: %v", label, err)
	}

	fmt.Printf("[ORK] Disco: %s\n", absDiskPath)
	fmt.Printf("[ORK] Label impostata: %q\n", label)
}

func parseDiskSize(value string) (int64, error) {
	value = strings.TrimSpace(strings.ToUpper(value))
	if value == "" {
		return 0, errors.New("dimensione vuota")
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
		return 0, fmt.Errorf("valore numerico non valido: %w", err)
	}

	if number <= 0 {
		return 0, errors.New("la dimensione deve essere maggiore di zero")
	}

	size := number * multiplier

	// FAT32 molto piccoli possono non essere validi o utili.
	const minimumSize = 64 * 1024 * 1024
	if size < minimumSize {
		return 0, fmt.Errorf(
			"dimensione troppo piccola: minimo consigliato 64M",
		)
	}

	return size, nil
}

// createVirtualDisk usa qemu-img per creare un disco RAW FAT32 nativo
// createVirtualDisk crea un file data.img e lo formatta direttamente in FAT32 (vfat)
func createVirtualDisk(sizeStr string) {
	size, err := parseDiskSize(sizeStr)
	if err != nil {
		log.Fatalf("[ORK] Dimensione disco non valida: %v", err)
	}

	absPath, err := filepath.Abs(diskName)
	if err != nil {
		log.Fatalf("[ORK] Impossibile risolvere il percorso del disco: %v", err)
	}

	if _, err := os.Stat(absPath); err == nil {
		log.Fatalf(
			"[ORK] Il disco %s esiste già. Rimuovilo esplicitamente per ricrearlo.",
			absPath,
		)
	} else if !os.IsNotExist(err) {
		log.Fatalf("[ORK] Impossibile controllare il disco: %v", err)
	}

	fmt.Printf(
		"[ORK] Creazione disco FAT32 %s, dimensione %s...\n",
		absPath,
		sizeStr,
	)

	virtualDisk, err := diskfs.Create(
		absPath,
		size,
		diskfs.SectorSizeDefault,
	)
	if err != nil {
		log.Fatalf("[ORK] Creazione immagine RAW fallita: %v", err)
	}

	_, err = virtualDisk.CreateFilesystem(disk.FilesystemSpec{
		// Partition 0 significa: filesystem sull'intero disco,
		// senza MBR o GPT.
		Partition: 0,
		FSType:    filesystem.TypeFat32,
	})
	if err != nil {
		_ = os.Remove(absPath)
		log.Fatalf("[ORK] Formattazione FAT32 fallita: %v", err)
	}

	fmt.Printf("[ORK] Disco FAT32 pronto: %s\n", absPath)
}

func launchVM() {
	qemuPath := filepath.Join(qemuDir, "qemu-system-x86_64.exe")

	// Controllo se il disco esiste, altrimenti avverte l'utente
	if _, err := os.Stat(diskName); os.IsNotExist(err) {
		fmt.Printf("[ORK Warning] Il disco %s non esiste. Lo creo automaticamente da 5G...\n", diskName)
		createVirtualDisk("5G")
	}

	cmdlineBytes, err := os.ReadFile(cmdlineF)
	if err != nil {
		log.Fatalf("Errore nella lettura di %s: %v", cmdlineF, err)
	}

	cmdline := strings.TrimSpace(string(cmdlineBytes))

	args := []string{
		"-accel", "whpx",
		"-m", "2G",
		"-kernel", kernelF,
		"-initrd", initrdF,
		"-append", cmdline,
		"-nographic",
		"-no-reboot",
		// Agganciamo il disco virtuale come drive virtio blocco RAW
		"-drive", "file=" + diskName + ",if=virtio,format=raw",
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd := exec.CommandContext(ctx, qemuPath, args...)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("[Orchestratore Go] Lancio della Micro-VM LinuxKit in corso...")
	fmt.Println("------------------------------------------------------------")
	fmt.Println("Per chiudere QEMU: Ctrl+A, poi X")
	fmt.Println("Oppure premi Ctrl+C per terminarlo dall'orchestratore.")
	fmt.Println("------------------------------------------------------------")

	err = cmd.Run()

	fmt.Println()
	fmt.Println("------------------------------------------------------------")

	switch {
	case ctx.Err() != nil:
		fmt.Println("[Orchestratore Go] Arresto richiesto dall'utente.")
	case err == nil:
		fmt.Println("[Orchestratore Go] La Micro-VM ha terminato correttamente.")
	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			fmt.Printf("[Orchestratore Go] QEMU è terminato con codice %d.\n", exitErr.ExitCode())
		} else {
			log.Fatalf("Errore durante l'esecuzione della VM: %v", err)
		}
	}
}
