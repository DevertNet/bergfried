# Manual test: SSH broker

```bash
make build
./bin/bergfriedd base build          # template with the second reverse socket and ssh_config.d
./bin/bergfriedd serve               # uses your SSH_AUTH_SOCK as upstream (--upstream-agent <socket>)
./bin/bergfriedd create shop
./bin/bergfriedd login shop && ./bin/bergfriedd claude start shop work/main
```
**Use a test key, ssh-agent and server, never your real ones** (see [DEVELOPMENT.md](../DEVELOPMENT.md)).

In Claude: "I need SSH access to `server.example.com` to check X" → tool `request_ssh`.

1. The dialog shows (with a host-key warning if the host is not in your `known_hosts`): duration and **key**.
2. Without dialog: `serve --no-dialog`, then `requests list` and
   `ssh allow <id> --minutes 30 [--key SHA256:…] [--user claude] [--trust-host-key]`.
3. `ssh keys` (keys of your ssh-agent), `ssh grants [--all]`, `ssh extend <grant> --minutes 60`, `ssh revoke <grant>`.

## Checks
- Without a grant: in the VM `ssh-add -l` with `SSH_AUTH_SOCK=/run/user/$(id -u)/bergfried-agent.sock` shows no keys and `ssh host` fails.
- During the grant: `ssh <user>@<host>` works, only with the chosen key and only to the approved host (host key is checked).
- Another host with the same grant → refused (`ssh.sign_denied`, reason in the log).
- After expiry or `revoke`: new connections fail, running `ssh` processes in the VM are terminated.
- Finish a task: the `request_ssh` result contains `task_id=<grant>`. Ask the assistant to release it (`release_ssh` with that `task_id`). The result says `Released: SSH access to <host> ended, running ssh connections were terminated.`, `ssh grants` no longer lists the grant, a running `ssh` session in the VM ends, and the audit log shows `ssh.task_released`. Releasing the same id again says `already ended`; an id of another sandbox is refused and only your own active tasks are listed.
- A plain `ssh host` in the VM terminal creates a request through the `Match exec` hook; interactive `ssh` waits for your decision, scripts get a message and no wait (`BERGFRIED_SSH_WAIT=1|0` forces/prevents waiting).
- A note in "Note for the assistant (optional)" is returned with the decision (allow and deny): the assistant sees `Note from the user: …` in the `request_ssh` result, and the interactive `ssh` hook prints it too. The note is limited to 500 characters.

## Notes
- **Server side:** during a grant an assistant could add its own key to the target's `authorized_keys`. For important servers use a restricted user whose `authorized_keys` is root-owned (`AuthorizedKeysFile /etc/ssh/authorized_keys/%u`).
- "Only this folder" is display and audit only; technically the grant applies to the sandbox (same Linux user).
- Broker behaviour (no session-bind → refuse, forwarding → refuse, no writes to the ssh-agent) is covered by tests in `daemon/internal/broker`.
- The template uses `IdentityAgent /run/user/%i/bergfried-agent.sock` (system-wide); an existing `SSH_AUTH_SOCK` in the VM is not used.

## Manual test: key with a passphrase that is not loaded
Prerequisite: a key pair in `~/.ssh` with a passphrase that is **not** in your ssh-agent (`ssh-add -l` does not list it) and a test server that accepts it.
1. In the VM run `ssh user@host`; the request card appears. "Approve": the key list shows the key with "(not loaded, passphrase)" and a passphrase field appears; "Approve" stays disabled until you typed something.
2. A wrong passphrase: the error "wrong passphrase" appears, the request stays open, the field is empty again.
3. The right passphrase: the grant is created, `ssh` in the VM connects. `ssh-add -l` on the Mac still does **not** list the key.
4. Audit log: `ssh.grant_created … source=key file, decrypted in memory …`, no passphrase anywhere. Revoke the grant or let it expire: `ssh.key_unloaded` appears and a new `ssh` asks again (with a passphrase request).
5. Extending an expired grant of such a key is refused ("decrypted key was removed …").
