# Design draft: template proposals by the agent (`propose_template_change`)

The agent may only **propose** changes to the VM template, never apply them.

- MCP tool `propose_template_change(summary, reason, patch)`: `patch` is a unified diff against the current `bergfried.yaml` (version included in the request).
- A third request kind next to files and SSH, with sandbox and agent name; shown in the app and written to the audit log.
- The daemon validates **before** showing: the patch applies, `limactl validate` passes and the security rules hold. Mounts, `forwardAgent`, socket/port forwards and home paths in a patch raise a clear warning; proposals that mount home are refused automatically.
- You see the diff, reason and warnings and can **deny**, **apply to this sandbox only** (overlay used on rebuild) or **adopt into the template** (new version number).
- Nothing is applied to a running VM; changes take effect on rebuild or for new sandboxes. A rebuild needs another confirmation because the disk may be lost.
- Small extra packages (apt/npm) belong in project Docker setups; the template is for VM-wide changes only.
