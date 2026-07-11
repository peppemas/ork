---
name: ork-skill
description: >
  Use this skill whenever you need a real Linux environment to run code, execute shell
  commands, install packages, or process files — and you are on a Windows host.
  ORK boots a lightweight LinuxKit micro-VM via QEMU and exposes it over SSH so you can
  treat it as a disposable Linux sandbox. Trigger this skill when the task involves:
  running a shell script, compiling or executing Java/Node.js/Python/Go code, using Linux
  CLI tools (grep, awk, curl, tar, ffmpeg, etc.), working with file permissions or symlinks,
  running a background process or server, or any situation where "I need a Linux terminal"
  is the natural thought. Do not skip this skill just because the user did not say "VM" or
  "ORK" explicitly — if the task needs Linux, use ORK.
---

## What ORK gives you

ORK boots a minimal Alpine Linux VM and exposes it over SSH. You get:

- A real Linux shell running as `root`, default working directory `/workspace`
- A persistent ext4 disk at `/workspace` — files survive VM restarts
- Outbound internet access (QEMU SLiRP NAT)
- Commands to run shell instructions, upload files, and download results

The container layer is stateless (packages installed with `apk` disappear on restart), but
everything written to `/workspace` is permanent.

---

## Step 0 — Always start the VM first

Before issuing any other ORK command, ensure the daemon is running. This call is
idempotent — if the VM is already up it exits immediately:

```powershell
ork.exe start --daemon
```

---

## Running commands

```powershell
# Single command — default workdir is /workspace
ork.exe exec -- <command>

# Shell one-liner
ork.exe exec -- sh -c "echo hello && ls -la"

# Override working directory
ork.exe exec --workdir /tmp -- pwd

# Arguments with spaces or flags — everything after -- is the command
ork.exe exec -- java -jar /workspace/app.jar --port 8080
```

`exec` is synchronous and forwards the remote exit code. Capture output by redirecting
inside the VM:

```powershell
ork.exe exec -- sh -c "your-command 2>&1 | tee /workspace/output.log"
```

To fire-and-forget a background process (the SSH session closes, the process keeps running):

```powershell
ork.exe exec -- sh -c "node /workspace/server.js >> /workspace/server.log 2>&1 &"
```

---

## Transferring files

```powershell
# Upload — defaults to /workspace/<filename>
ork.exe put path\to\file.jar

# Upload to an explicit remote path
ork.exe put path\to\file.jar /workspace/libs/file.jar

# Download to the current directory
ork.exe get /workspace/output.txt

# Download to an explicit local path
ork.exe get /workspace/report.pdf C:\Users\me\Desktop\report.pdf
```

Binary files (`.jar`, `.png`, `.jpg`, `.zip`, …) transfer correctly — raw bytes, no encoding.

For small text content you can skip `put` entirely and write inline:

```powershell
ork.exe exec -- sh -c "printf 'console.log(42)\n' > /workspace/hello.js"
```

---

## Installing Node.js (persistent across restarts)

Packages installed with `apk` vanish when the VM container restarts. Store the Node.js
binary on `/workspace` so it survives — you only do this once per workspace disk.

```powershell
# Download and extract the prebuilt Linux x64 binary
ork.exe exec -- sh -c "
  cd /workspace &&
  wget -q https://nodejs.org/dist/v22.13.0/node-v22.13.0-linux-x64.tar.gz &&
  tar xzf node-v22.13.0-linux-x64.tar.gz &&
  mv node-v22.13.0-linux-x64 .node &&
  rm node-v22.13.0-linux-x64.tar.gz &&
  echo done
"
```

Use it via the full path (always works, no PATH setup needed):

```powershell
ork.exe exec -- /workspace/.node/bin/node --version
ork.exe exec -- /workspace/.node/bin/node /workspace/app.js
ork.exe exec -- /workspace/.node/bin/npm install --prefix /workspace/myapp
```

Full upload → install → run → download cycle:

```powershell
ork.exe put app.js
ork.exe put package.json
ork.exe exec -- sh -c "cd /workspace && /workspace/.node/bin/npm install"
ork.exe exec -- /workspace/.node/bin/node /workspace/app.js
ork.exe get /workspace/output.json
```

---

## Installing other software

For tools needed only in the current session, `apk add` is the fastest path:

```powershell
ork.exe exec -- apk add --no-cache curl git python3 ffmpeg
```

These disappear after `ork stop`. For persistent tools, download prebuilt binaries to
`/workspace/bin/` and call them by path, same as the Node.js pattern above.

---

## Inspecting the workspace

```powershell
# List files
ork.exe exec -- ls -lh /workspace

# Disk usage summary
ork.exe exec -- df -h /workspace

# Largest items
ork.exe exec -- du -sh /workspace/* | sort -rh | head -20

# Find files by name
ork.exe exec -- find /workspace -name "*.json"
```

---

## Persistence reference

| What | Survives restart? |
|------|-------------------|
| Files in `/workspace` | **Yes** — ext4 disk |
| Binaries stored in `/workspace/.node/`, etc. | **Yes** |
| `node_modules` inside `/workspace/` | **Yes** |
| Packages installed with `apk add` | **No** — container is stateless |
| Background processes started with `&` | **No** — killed on VM stop |
| Environment variables exported in a session | **No** — each `exec` is a fresh SSH session |

---

## Stopping the VM

```powershell
ork.exe stop
```

Sends `poweroff` gracefully. The workspace disk is not affected.

---

## Troubleshooting

**VM won't start / SSH timeout**
Check `.ork\vm.log` for kernel or container boot errors.

**Command produces no output**
Capture stderr too: `.\ork.exe exec -- sh -c "cmd 2>&1 | tee /workspace/debug.log"`

**Node.js missing after restart**
Always call it as `/workspace/.node/bin/node`. If the directory is gone, the workspace was
recreated — re-run the download step above.

**Workspace full**
```powershell
ork.exe exec -- du -sh /workspace/* | sort -rh | head -20
```
