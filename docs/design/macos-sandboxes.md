# Design draft: macOS sandboxes and a second template (not started)

**Goal:** keep developing Bergfried itself inside a sandbox. The Go parts can be built and tested on Linux, but the SwiftUI app needs a macOS toolchain (`swiftc`, macOS SDK, `codesign`). That means a second VM template with a **macOS guest** next to the current Linux one.

## What is verified (Lima documentation, read on 2026-11-09)
- Lima ≥ 2.1 runs macOS guests, **experimental**; host must be Apple silicon and the same or a newer macOS than the guest; `vmType: vz` only; templates `macos-15|26|27` are shipped with Lima 2.3 (checked locally).
- Differences from Linux guests: password login is enabled; **password-less `sudo` is disabled** (except `/sbin/shutdown -h now`, not configurable); several features are not implemented.
- Caveats: the video display cannot be turned off; **no automatic port forwarding** (use `ssh -L` or the vzNAT address); no custom CA certs; no containerd; first-login setup screens can be suppressed (`osOpts.Darwin.suppressFirstLoginSetup`, Lima ≥ 2.3).
- Not found in the docs, so **untested**: whether explicit `portForwards` with `guestSocket … reverse: true` work on macOS guests, whether `limactl clone` works for them, how long a clone takes, and disk size of a base VM (the restore image alone is ~20 GB). Apple's licence limits the host to **two running macOS VMs**.

## What breaks in Bergfried today (Linux assumptions)
| Area | Linux today | macOS guest |
|---|---|---|
| Daemon socket path | `/run/user/<uid>/bergfried.sock` | `/run/user` does not exist; needs another path (e.g. `/tmp/bergfried-<uid>/` or `~/.bergfried/`) in the template, in `bergfried-mcp` and in the ssh config |
| First boot | `systemd-machine-id-setup`, `dpkg-reconfigure openssh-server`, `systemctl restart ssh` | macOS has no equivalent steps; hardware UUID/ssh host keys differ; needs its own script |
| Hooks and config | `sudo install … /usr/local/bin`, `/etc/ssh/ssh_config.d/*` | **`sudo` is not password-less**: the install steps need a password or Lima's guest agent; macOS `ssh_config.d` support and `Match exec` need checking |
| Packages | `apt-get`, Docker, tmux | Homebrew (`tmux`, `git`, `go`), Command Line Tools; **no Docker** (so `make test` uses a native Go toolchain instead) |
| Binaries | `bergfried-mcp` built for linux/arm64 | needs darwin/arm64 (`InstallMCP` picks by guest OS) |
| Agent start | tmux window + `claude --continue --remote-control` | should work (tmux via Homebrew, Claude Code runs on macOS), **untested** |
| Monitor | probes with Linux shell commands (`ps`, `df`, tmux) | BSD `ps`/`df` flags differ |
| Guidance text | mentions Linux, Docker, `~/.git-credentials` | credential helper is `osxkeychain` by default: must be set to `store` |
| Web servers | Lima forwards guest ports to the Mac automatically | **no automatic forwarding** |

## Suggested shape
- A sandbox gets an `os` (`linux` | `macos`), chosen when it is created; templates become `vm/linux.yaml` and `vm/macos.yaml`; base VM names include the OS (`bergfried-base-linux-<hash>`).
- Put OS-specific behaviour behind one small interface in `daemon/internal/sandbox` (socket path, first boot, install hooks, install packages, monitor probe, guidance); keep tmux, agents and the request flow shared.
- Treat macOS as **experimental** in the UI until the open points above are verified with a real macOS guest.

## Problems specific to developing Bergfried inside a sandbox
- **No Lima inside a VM:** the VMs Bergfried manages cannot be started from inside a sandbox (no nested virtualisation guarantee), so end-to-end tests with real VMs must still run on the Mac. Unit tests, docs, Go builds and `fakegithub` tests work in a Linux sandbox.
- **Host actions stay manual:** installing a build, restarting the daemon, notification permissions, screenshots. The path is: agent pushes a branch → CI builds → you install (an app-built update, or a CI artifact from a branch build).
- **Release trust:** the sandbox token needs `Contents: write` on this repository, and a tag `v*` publishes a release that the updater installs. Before putting this repo into a sandbox token, protect it on GitHub (free for public repos): a **ruleset** that restricts creating/updating `v*` tags and requires pull requests for `main`, **without** a bypass for the token's account, and a `release` environment with a required reviewer in `release.yml`. Without this the agent could ship code to your Mac.
- **Docker in the Linux sandbox** works for `make test`; `make build` cross-compiles darwin/arm64 binaries there, but `app/build.sh` cannot run (needs macOS).

## Open
- Try a macOS guest by hand first (`limactl start template:macos`) and answer the untested points; only then build the second template.
