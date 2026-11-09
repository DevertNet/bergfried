# Manual test: SSH broker

```bash
make build
./bin/bergfriedd base build          # template with the second reverse socket and ssh_config.d
./bin/bergfriedd serve               # uses your SSH_AUTH_SOCK as upstream (--upstream-agent <socket>)
./bin/bergfriedd create shop
./bin/bergfriedd login shop && ./bin/bergfriedd agent add shop main
```
**Use a test key, agent and server, never your real ones** (see [DEVELOPMENT.md](../DEVELOPMENT.md)).

In the agent: "I need SSH access to `server.example.com` to check X" → tool `request_ssh`.

1. The dialog shows (with a host-key warning if the host is not in your `known_hosts`): duration, **key**, scope (agent/all agents), mode (silent in the window / every connection).
2. Without dialog: `serve --no-dialog`, then `requests list` and
   `ssh allow <id> --minutes 30 [--key SHA256:…] [--user claude] [--all-agents] [--per-connection] [--trust-host-key]`.
3. `ssh keys` (keys of your agent), `ssh grants [--all]`, `ssh extend <grant> --minutes 60`, `ssh revoke <grant>`.

## Checks
- Without a grant: in the VM `ssh-add -l` with `SSH_AUTH_SOCK=/run/user/$(id -u)/bergfried-agent.sock` shows no keys and `ssh host` fails.
- During the grant: `ssh <user>@<host>` works, only with the chosen key and only to the approved host (host key is checked).
- Another host with the same grant → refused (`ssh.sign_denied`, reason in the log).
- After expiry or `revoke`: new connections fail, running `ssh` processes in the VM are terminated.
- A plain `ssh host` in the VM terminal creates a request through the `Match exec` hook; interactive `ssh` waits for your decision, scripts get a message and no wait (`BERGFRIED_SSH_WAIT=1|0` forces/prevents waiting).

## Notes
- **Server side:** during a grant an agent could add its own key to the target's `authorized_keys`. For important servers use a restricted user whose `authorized_keys` is root-owned (`AuthorizedKeysFile /etc/ssh/authorized_keys/%u`).
- "Only this agent" is display and audit only; technically the grant applies to the sandbox (same Linux user).
- Broker behaviour (no session-bind → refuse, forwarding → refuse, no writes to the agent) is covered by tests in `daemon/internal/broker`.
- The template uses `IdentityAgent /run/user/%i/bergfried-agent.sock` (system-wide); an existing `SSH_AUTH_SOCK` in the VM is not used.
