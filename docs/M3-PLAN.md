# M3 – SSH-Broker (Plan)

**Ziel:** `ssh` klappt nur während einer Freigabe, danach Ablehnung. Der private Key verlässt nie den Mac.

## Funktionsweise
- Pro Sandbox ein eigener **ssh-agent-Socket** (`~/.lima/<vm>/sock/ssh-agent.sock`), per Lima-`reverse`-Forward in der VM als
  `/run/user/<uid>/bergfried-agent.sock`. Die VM nutzt ihn über `IdentityAgent` in `~/.ssh/config` (kein Agent-Forwarding vom Mac).
- Der Broker implementiert das ssh-agent-Protokoll (`golang.org/x/crypto/ssh/agent`) und signiert **upstream** mit dem echten Agent
  des Macs (`SSH_AUTH_SOCK` des Daemons oder `--upstream-agent`). Der Key selbst wird nie gelesen.
- **Ohne aktive Freigabe**: leere Key-Liste, jede Signatur wird abgelehnt (und protokolliert).
- **Mit Freigabe**: Liste zeigt nur den gewählten Key. Signiert wird nur, wenn
  1. die Freigabe für diese Sandbox noch gilt (Zeitfenster, nicht entzogen),
  2. der Client per `sandbox-bind@openssh.com` den Host-Key des Ziels gemeldet hat (Signatur wird geprüft, kein Forwarding),
  3. dieser Host-Key zu einem freigegebenen Host gehört (**Destination Constraint**, Fingerabdruck beim Freigeben festgehalten),
  4. die zu signierenden Daten zur gebundenen Sandbox-ID passen.
  Fehlt `sandbox-bind` (alter Client) → ablehnen (fail closed).
- **Host-Key beim Freigeben:** aus der `known_hosts` des Macs (`ssh-keygen -F`), sonst per `ssh-keyscan` mit Fingerprint im Dialog.
- **Entscheidung** (Dialog oder CLI): Dauer, Key, Geltung (Agent / alle Agents der Sandbox), optional „jede Verbindung einzeln bestätigen“.
  Standard-Key und User je Host in `~/.bergfried/hosts.json`. Verlängern und vorzeitig Entziehen jederzeit.
- **Ablauf/Entzug:** laufende `ssh`-Prozesse in der VM werden beendet; `ControlMaster`/`ControlPath` sind in der VM abgeschaltet.
- `~/.ssh/config` der VM: `IdentityAgent`, `ControlMaster no`, je Host `User` aus der Freigabe (verwalteter Block).
- Neues MCP-Tool `request_ssh(host, minutes?, reason)`.

## Grenzen (ehrlich)
- Der Agent-Name einer Freigabe dient Anzeige und Protokoll. Innerhalb der VM (gleicher Linux-User) lässt sich „nur dieser Agent“
  technisch nicht erzwingen – die Grenze ist die Sandbox.
- Während einer Freigabe könnte der Agent einen eigenen Key in `authorized_keys` des Ziels eintragen → eingeschränkter User,
  `authorized_keys` gehört root (`AuthorizedKeysFile`).
- Vorlage wird v3 (zweiter Reverse-Socket), Basis `bergfried-base-v3`.
