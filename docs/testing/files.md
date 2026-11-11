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

## Known limits
- The folder path in dialogs is claimed from inside the VM (same Linux user) — not proof. The **sandbox** comes from the socket.
- A request waits up to 10 minutes; whether Claude Code or OpenCode wait that long for an MCP tool is unverified.
