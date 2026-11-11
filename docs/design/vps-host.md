# Design draft: VPS host (postponed)

**Status:** postponed, not started. A draft to agree on with the owner before building; verify everything external (Lima on Debian, QEMU, OpenSSH options) in current docs — do not guess.

**Done when:** a sandbox runs on a Debian VPS, file and SSH approvals come from the Mac (requests appear in the Mac app), and the Mac answers them over the link.

## What exists and is reusable
- `bergfriedd` is one binary for both hosts (pure Go) — the Makefile only needs `GOOS=linux GOARCH=amd64|arm64`.
- `internal/api` (HTTP/JSON) is the app's interface and can control a VPS daemon unchanged over a forwarded unix socket.
- `internal/sandbox` (Lima wrapper, base VM, clones, Claude folders, monitor) is host-agnostic except `vmType: vz`/`virtiofs` in `vm/bergfried.yaml`.
- The broker already separates the **upstream** (`List`, `Sign`) from the rest — remote signing attaches there.
- Index, grants and blocklist live on the Mac and stay there (home and keys never leave the Mac).

## To clarify first
1. **KVM on the VPS:** `ls -l /dev/kvm`, `grep -cE '(vmx|svm)' /proc/cpuinfo`, `systemd-detect-virt` (nested virtualisation needed). Without KVM only a fallback (Incus containers) or very slow QEMU.
2. Debian version, architecture, user, resources (RAM/disk for several VMs of ~6 GB / 60 GB sparse).
3. **Lima on Debian:** install from release tarball, `qemu-system-x86`, `kvm` group, `vmType: qemu`, which `mountType` works for QEMU (virtiofs may be unavailable → 9p/reverse-sshfs), reverse `guestSocket` forwards, `limactl clone` cost on ext4.
4. Which SSH agent on the Mac holds the keys (keychain/1Password/ssh-agent).

## Proposal
**Link Mac ⇄ VPS:** the Mac daemon opens an outgoing, supervised `ssh` (reconnect with backoff, `ServerAliveInterval`, `ExitOnForwardFailure=yes`, no ControlMaster) with `-L` (app/Mac controls the VPS API) and `-R` (VPS daemon calls the Mac). Ideally a dedicated restricted key.
**New gRPC service `Uplink` (Mac side):** `ListFiles/SearchFiles`, `RequestFile/RequestFolder` (Mac streams approved files in chunks; requests wait while the link is down), `SSHList/SSHSign` (VPS broker forwards with session-bind data; Mac offline → login fails immediately; grants and clock are the Mac's), `RequestSSH`, `SSHAttempt`.
**Data model:** `hosts` table; `host` column on sandboxes/requests/grants/audit; app shows sandboxes of all hosts.
**QEMU template variant** (`vm/bergfried-qemu.yaml`), `bergfried-mcp` for linux/amd64, systemd unit for `bergfriedd`, install script, "Hosts" section in the app.

## Steps
1. Survey the VPS (KVM etc.), decide Lima/QEMU vs fallback. 2. Makefile for linux/amd64. 3. Get template, base VM and clone running by hand. 4. `serve` on the VPS without uplink. 5. Link (`internal/remote`). 6. `Uplink` proto, server and client (list/search → files with streaming and queue → folders). 7. Remote broker incl. offline behaviour. 8. Data model and Hosts UI. 9. End-to-end test and docs.

## Risks
Nested virtualisation, Mac sleep drops the link (`caffeinate` option), large files, MCP tool timeouts for long waits, grants expire by the Mac's clock.
