# Manual test: file requests

```bash
make build
export BERGFRIED_TEMPLATE=vm/bergfried.yaml
./bin/bergfriedd serve               # terminal 1: daemon (index, sockets, approval dialog)
./bin/bergfriedd create shop         # terminal 2: installs bergfried-mcp in the VM
./bin/bergfriedd login shop && ./bin/bergfriedd claude start shop work/main
```
In Claude: "List my files in Documents and fetch <file>".

- `list_files` / `search_files` return names, size and date only. `~/.ssh`, `~/Library`, `~/.config`, `.env` and similar never show up.
- `request_file` shows a macOS dialog (**Deny** is the default). Without dialog: `serve --no-dialog`, then `./bin/bergfriedd requests list` → `requests allow <id>` / `requests deny <id>`.
- After approval the copy is in the VM at `~/.bergfried/sandboxes/<sandbox>/shared/in/<sandbox>/…` (read-only).
- Blocked paths are refused without a dialog (`verdict: blocked`) and appear in `./bin/bergfriedd audit`.
- Folders over 100 MB / 500 files need a second confirmation (`requests allow <id> --confirm-large`).

Blocklist: `~/.bergfried/blocklist.json` (paths relative to home, name patterns). Defaults: `.ssh Library .config .gnupg .aws .kube .docker .netrc .git-credentials .claude .claude.json .bergfried .lima` and `.env .env.* *.keychain *.keychain-db *.kdbx id_rsa id_ed25519 id_ecdsa`. Restart `serve` after editing.

## Calling the MCP server without Claude
JSON-RPC over stdio inside the VM; the identity is the socket. Example (`initialize`, then `tools/call`; written from the MCP spec, not re-run after the docs rewrite):
```bash
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_files","arguments":{"path":""}}}' | BERGFRIED_FOLDER=shop/work/main ~/.local/bin/bergfried-mcp
```

## Host address (`get_host_address`)
Needs no approval. In a sandbox ask the assistant: "Which LAN address does my Mac have? Use `get_host_address`." Expected: the IPv4 of en0 (compare with `ipconfig getifaddr en0` on the Mac), the `<name>.local` name (compare with `scutil --get LocalHostName`), and a note that VM ports are not reachable from the phone. With Wi-Fi off the result has no IP. The audit log shows `host.address` with the count only. (Real Mac network behaviour not yet verified.)

## Known limits
- The folder path in dialogs is claimed from inside the VM (same Linux user) — not proof. The **sandbox** comes from the socket.
- A request waits up to 10 minutes; whether Claude Code or OpenCode wait that long for an MCP tool is unverified.

## Open the shared folders in Finder
On a sandbox card, **Files → Open “in” folder / “out” folder in Finder** opens `~/.bergfried/sandboxes/<name>/shared/in` or `.../out` (also while the VM is stopped). Check: after approving a file the `in` folder shows it; a file the assistant puts in `out` appears in the `out` folder. If a folder is missing, a banner names the path.
