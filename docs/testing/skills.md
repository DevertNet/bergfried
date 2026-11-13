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

The app shows this as an orange hint in the sandbox card while the VM is running and `skills_mount` is `false` ("Shared skills are not mounted in this VM yet. Stop and start the VM once; the mount is added automatically.", the same text as tooltip). Nothing is shown when the field is `true` or missing. The daemon does not change the mounts of a running VM; the next stop/start adds the mount.

**To be verified in the real app:** the hint only on a running VM and only for `skills_mount: false`. The app part of this change was written without being able to run it.

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

## Proposals

The shared folder is read-only in the VM, so an assistant proposes changes with `propose_skill_change`; you decide in the app.

1. In a sandbox ask the assistant: "Create a shared skill `demo-proposal` with a `SKILL.md` that explains the test; call `propose_skill_change` with the complete content." The call blocks for up to 10 minutes.
2. The app shows a request of kind `skill` with the skill names, the reason and one entry per file (`path`, `op` = create/modify/delete, `diff`). **To be verified in the real app:** the diff rendering and the Allow/Deny buttons.
   App steps: in the Requests tab the card shows "wants to change the shared skill(s) … (%lld file(s))" and **Review changes …**; the sheet shows the warning, every file with Create / Change / Delete / No change, the complete diff (green `+`, red `-`, `@@` secondary, monospaced, selectable, per file scrollable, no truncation) and an optional note for Deny. **Allow** and **Deny** call `POST /v1/skills/decide`; on an error (409) the message stays in the sheet and the request ends `failed` in the history. The menu bar entry only opens the window – it has no Allow/Deny. **To be verified in the real app:** all of the above, since the app part was written without being able to run it.
3. Allow: the file appears on the Mac at `~/.bergfried/skills/demo-proposal/SKILL.md`; the assistant gets "Approved and applied …"; the audit log has `skills.allowed`.
4. Repeat the proposal and deny it with a note (for example "use another name"): nothing is written and the assistant sees the note (`skills.denied`).
5. A proposal whose content contains a zero-width space (U+200B) is refused before it reaches the app, with `Invalid proposal: … invisible or line-breaking character U+200B`.
6. Open a proposal and, before allowing it, change the same file on the Mac. Allowing it fails: the request ends as `failed`, the audit log has `skills.apply_failed`, and the assistant is told to propose again.

The app is the decision maker; for a terminal test the same decisions work over the app socket:

```bash
curl -s --unix-socket ~/.bergfried/app.sock http://localhost/v1/requests | jq '.[] | select(.kind=="skill")'
curl -s --unix-socket ~/.bergfried/app.sock -X POST http://localhost/v1/skills/decide \
  -d '{"id":ID,"allow":true}'                     # {"status":"allowed"}
curl -s --unix-socket ~/.bergfried/app.sock -X POST http://localhost/v1/skills/decide \
  -d '{"id":ID,"allow":false,"note":"use another name"}'
```

## Notes

- `limactl edit … --mount-only` replaces **all** mounts; the daemon always passes `shared/in`, `shared/out:w` and the skills folder together.
- The skills folder is read-only inside the VM; edit skills only on the Mac.
- Delete the test sandbox afterwards: `./bin/bergfriedd delete skills-test --force`.
