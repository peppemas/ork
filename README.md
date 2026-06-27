![ork_logo.png](images/ork_logo.png)

# ORK

**ORK** is a CLI tool for running disposable Linux micro-VMs on Windows. It uses [LinuxKit](https://github.com/linuxkit/linuxkit) to build minimal OS images and [QEMU](https://www.qemu.org/) to run them, exposing a clean interface to start VMs, execute commands, and transfer files — all over SSH.

The primary use case is **agentic AI workloads**: give a coding agent or automated pipeline a clean, isolated Linux environment on Windows without Docker Desktop or WSL.

---

## How it works

```
┌─────────────────────────────────────────────┐
│  Windows host                               │
│                                             │
│  ork.exe ──────► QEMU (WHPX accelerator)    │
│                      │                      │
│                      │  virtio disk         │
│                      ▼                      │
│              ┌───────────────┐              │
│              │  LinuxKit VM  │              │
│              │               │              │
│              │  kernel 6.12  │              │
│              │  containerd   │              │
│              │  dropbear SSH │◄── port 2222 │
│              │               │              │
│              │  /workspace   │◄── ext4 disk │
│              └───────────────┘              │
└─────────────────────────────────────────────┘
```

- The VM runs a Linux 6.12 kernel with a minimal containerd-based userspace (LinuxKit).
- A single ext4 disk image (`workspace.img`) is mounted inside the VM as `/workspace`. This is where you put your files and run your workloads.
- SSH access is provided by [Dropbear](https://matt.ucc.asn.au/dropbear/dropbear.html) — lightweight, no privilege separation, works cleanly inside containers.
- All SSH operations in `ork` use Go's native `golang.org/x/crypto/ssh` library, so there are no Windows ACL issues with key files.
- An RSA key pair is generated automatically under `.ork/` on first run. The public key is embedded into `workspace.img` so the VM trusts it without passwords.

---

## Prerequisites

| Tool                                                      | Purpose             | Install                      |
|-----------------------------------------------------------|---------------------|------------------------------|
| [QEMU](https://www.qemu.org/download/#windows)            | Runs the VM         | `winget install QEMU.QEMU`   |
| [LinuxKit](https://github.com/linuxkit/linuxkit/releases) | Builds the OS image | Download binary, add to PATH |

QEMU must be on `PATH`, or set `vm.qemu_path` in `ork.json`. LinuxKit must be on `PATH`, or set `image.linuxkit_path` in `ork.json`.

---

## Install QEMU

### Windows
```bash
winget install SoftwareFreedomConservancy.QEMU
```

### Linux
```bash
apt install qemu-system-x86_64
```

---

## Quick start

```powershell
# 1. Build the VM image (downloads container layers, produces kernel + initrd)
.\ork.exe build-image

# 2. Start the VM as a background daemon (creates workspace.img automatically)
.\ork.exe start --daemon

# 3. Run a command inside the VM
.\ork.exe exec -- uname -a

# 4. Upload a file into /workspace
.\ork.exe put myapp.jar

# 5. Download a file from the VM
.\ork.exe get /workspace/output.txt

# 6. Stop the VM
.\ork.exe stop
```

---

## Commands

| Command                              | Description                                                              |
|--------------------------------------|--------------------------------------------------------------------------|
| `ork build-image [template] [name]`  | Build the LinuxKit kernel + initrd from a YAML template                  |
| `ork start`                          | Start the VM interactively (attached to console)                         |
| `ork start --daemon`                 | Start the VM as a background daemon, wait for SSH readiness              |
| `ork exec [--workdir dir] -- <cmd>`  | Run a command inside the daemon VM                                       |
| `ork run [--workdir dir] -- <cmd>`   | Alias for `exec`                                                         |
| `ork put <local-file> [remote-path]` | Upload a file into the VM (default destination: `/workspace/<filename>`) |
| `ork get <remote-path> [local-file]` | Download a file from the VM                                              |
| `ork stop`                           | Gracefully power off the daemon VM                                       |
| `ork create-workspace [size]`        | Manually create the ext4 workspace disk                                  |

### exec / run options

```powershell
# Default working directory is /workspace
.\ork.exe exec -- ls -la

# Override working directory
.\ork.exe exec --workdir /tmp -- pwd

# Multi-argument commands with spaces
.\ork.exe exec -- java -jar /workspace/myapp.jar --port 8080
```

---

## Configuration

`ork` reads `ork.json` from the current directory. All fields are optional — defaults are used for anything omitted.

```json
{
  "vm": {
    "memory": "2G",
    "kernel": "test-disk-kernel",
    "initrd": "test-disk-initrd.img",
    "cmdline": "test-disk-cmdline",
    "qemu_path": "C:\\Program Files\\qemu\\qemu-system-x86_64.exe",
    "accelerator": "auto"
  },
  "image": {
    "linuxkit_path": "",
    "template": "templates/test-disk.yaml",
    "name": "test-disk",
    "format": "kernel+initrd"
  },
  "workspace": {
    "path": "workspace.img",
    "default_size": "10G",
    "mount": "/workspace"
  },
  "exec": {
    "host": "127.0.0.1",
    "ssh_port": 2222,
    "user": "root",
    "key_path": ".ork/id_rsa",
    "state_path": ".ork/vm.json",
    "ready_timeout": "60s"
  }
}
```

**`vm.accelerator`** defaults to `auto`, which selects WHPX on Windows, KVM on Linux, and HVF on macOS.

---

## Build

```powershell
# Windows
.\build.bat

# Linux / macOS
./build.sh
```

The binary is placed in `bin/`.

---

## VM image template

The LinuxKit YAML template at `templates/test-disk.yaml` defines what runs inside the VM:

- **`onboot/network`** — configures the network interface (QEMU user networking, 10.0.2.15/24)
- **`services/sshd`** — mounts the ext4 workspace disk, sets up authorized keys from `.ork/authorized_keys`, and starts Dropbear SSH on port 22

To add software to the VM (e.g. Java, Node.js), add a service container to the template and rebuild the image with `ork build-image`.

---

## File layout

```
.
├── ork.json                  # Configuration (optional)
├── templates/
│   └── test-disk.yaml        # LinuxKit VM definition
├── workspace.img             # ext4 disk mounted as /workspace in the VM
├── test-disk-kernel          # Built VM kernel (after ork build-image)
├── test-disk-initrd.img      # Built VM initrd (after ork build-image)
├── test-disk-cmdline         # Kernel command line
└── .ork/
    ├── id_rsa                # SSH private key (auto-generated)
    ├── id_rsa.pub            # SSH public key
    └── vm.json               # Daemon VM state (PID, port, etc.)
```

---

## Notes

- **`workspace.img` is persistent** across VM restarts. Only delete it if you want a clean slate (it will be recreated automatically on the next `ork start --daemon`).
- **Rebuilding the image** (`ork build-image`) is only needed when you change the LinuxKit template. It is not needed when changing `ork.json` or your workspace files.
- **Port 2222** on localhost is forwarded to the VM's port 22. If the port is in use, change `exec.ssh_port` in `ork.json`.
- The VM has outbound internet access through QEMU's built-in SLiRP NAT.
