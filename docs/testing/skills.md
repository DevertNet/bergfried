# Manual test: shared skills folder

```bash
make build
export BERGFRIED_TEMPLATE=vm/bergfried.yaml
./bin/bergfriedd serve               # terminal 1: daemon
./bin/bergfriedd create skills-test  # terminal 2: new sandbox
```

## New sandbox

- `~/.bergfried/skills` exists on the Mac (created by the daemon if it was missing).
- In the VM the folder is mounted read-only at the same path:

  ```bash
  skills="$HOME/.bergfried/skills"
  limactl shell bergfried-skills-test -- mount | grep skills
  ```

  The line shows the Mac path of the skills folder with `ro` (virtiofs).
- Writing as the normal user **must fail**:

  ```bash
  limactl shell bergfried-skills-test -- bash -lc "echo test > '$skills/probe.txt'"
  ```

  Expected: `Permission denied` (or `Read-only file system`).
- Writing as root **must fail** too:

  ```bash
  limactl shell bergfried-skills-test -- sudo bash -lc "touch '$skills/x'"
  ```

  Expected: `Permission denied` (or `Read-only file system`). If either write succeeds, the read-only mount is **not** enforced on the host side – note the Lima version and the exact output and report it; this is still to be verified with a real Lima VM.
- The other two shared folders still work: `shared/in` is read-only, `shared/out` is writable.

## Existing sandbox (created before the skills mount)

Lima can only change the mounts of a stopped instance, so an existing sandbox gets the skills mount automatically at its next start:

```bash
./bin/bergfriedd stop skills-test
./bin/bergfriedd start skills-test
limactl shell bergfried-skills-test -- mount | grep skills
./bin/bergfriedd audit | grep skills_mount
```

- The mount is present after the start; the audit log has `sandbox.skills_mount` with the VM name.
- When the mount is already there, the daemon does not call `limactl edit` and writes no audit entry.
- If the edit fails, the start still happens; the audit log has `sandbox.skills_mount_failed` with the error and the sandbox works as before.

To simulate a sandbox without the mount, create it with an older daemon (before this change) or, while the VM is stopped, remove the `skills` entry from `<LIMA_HOME>/bergfried-skills-test/lima.yaml` (default `~/.lima/bergfried-skills-test/lima.yaml`).

## Hint for a running VM without the mount

`GET /v1/sandboxes` reports `skills_mount: false` for a sandbox whose instance config has no read-only skills mount (also for `status: missing`):

```bash
curl -s --unix-socket ~/.bergfried/app.sock http://localhost/v1/sandboxes \
  | jq '.[] | {name, status, skills_mount}'
```

The app hint for this field is documented later. The daemon does not change the mounts of a running VM; the next stop/start adds the mount.

## Using the skills

The shared folder is linked per skill folder into `~/.claude/skills` in the VM; Claude Code and
OpenCode both read that folder.

1. Put a `SKILL.md` into `~/.bergfried/skills` on the Mac, e.g. `~/.bergfried/skills/demo/SKILL.md`.
2. See the link in the VM (**to be verified** – needs a real VM):

   ```bash
   limactl shell bergfried-skills-test -- ls -l ~/.claude/skills
   ```

   `demo` is a symlink into `~/.bergfried/skills/demo`.
3. Claude lists the skill (e.g. `/skills` or in its skill overview; **to be verified**).
4. OpenCode lists it too (`opencode` skills; **to be verified**).
5. Edit the skill file on the Mac: the app shows **Restart needed** for running assistants.
   The daemon relinks the folder automatically (monitor); the assistants pick up the new
   content after a restart.
6. A local real folder with the same name in `~/.claude/skills` is not replaced (**to be verified**).
7. Removing the skill folder on the Mac removes the link in the VM.

## Notes

- `limactl edit … --mount-only` replaces **all** mounts; the daemon always passes `shared/in`, `shared/out:w` and the skills folder together.
- The skills folder is read-only inside the VM; edit skills only on the Mac.
- Delete the test sandbox afterwards: `./bin/bergfriedd delete skills-test --force`.
