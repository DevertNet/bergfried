# M2 – Datei-Anfragen (Plan)

**Ziel:** `request_file` wird bestätigt und die Datei liegt in der VM.

## Bausteine
1. **Ein Go-Modul** (`go.mod` im Root): `daemon/`, `mcp/`, `proto/gen/` teilen sich den Protokoll-Code.
2. **Protobuf/gRPC** (`proto/bergfried/v1`), Codegen in Docker (`make proto`), generierter Code im Repo.
   - `SessionAPI` (VM → Daemon): `ListFiles`, `SearchFiles`, `RequestFile`, `RequestFolder`.
   - `Admin` (CLI → Daemon): `ListRequests`, `Decide`.
3. **Identität der Session = der Socket.** Der Daemon lauscht je Session auf `~/.lima/<vm>/sock/bergfried.sock`
   (Lima `portForwards` mit `reverse: true` → in der VM `/run/user/<uid>/bergfried.sock`). Die Session wird aus dem
   Socket abgeleitet, nie aus einem Feld der Anfrage. Der Agent-Name kommt per gRPC-Metadata (in der VM nur
   behauptet, keine harte Grenze). Socket-Rechte 0600.
4. **Index** (`daemon/internal/index`): Home wird rekursiv erfasst, Sperrliste-Pfade tauchen gar nicht auf.
   Standard-Sperrliste: `~/.ssh`, `~/Library`, `~/.config`, `~/.mcp-einfach123-secrets`, Schlüsselbünde,
   `.env*`, plus `~/.bergfried` und `~/.lima`. Symlinks werden nie gefolgt; vor jeder Freigabe wird der echte Pfad
   erneut gegen die Sperrliste geprüft (Pfad-Tricks mit `..` und Symlinks = Tests).
5. **Freigabe:** Anfragen landen in SQLite (`requests`) und im Audit-Log. Entscheidung per macOS-Dialog
   (`osascript`, Erlauben/Ablehnen, der Begründungstext des Agents wird nur als Text angezeigt) oder
   `bergfriedd requests list|allow|deny`. Gesperrte Pfade: sofortige Ablehnung ohne Rückfrage.
   `request_folder` über 100 MB / 500 Dateien: zweite Bestätigung.
6. **Kopie:** nach `~/.bergfried/sessions/<s>/shared/in/<s>/<relativer Pfad>` (ro-Mount in der VM), Mac-Datei wird
   kopiert, nicht gemountet. Antwort an den Agent: der VM-Pfad.
7. **`bergfried-mcp`** (Go, offizielles MCP-SDK, stdio): Tools `list_files`, `search_files`, `request_file`,
   `request_folder`. `create`/`start` installiert das Binary per `limactl cp` und registriert es mit
   `claude mcp add --scope user`.
8. **`bergfriedd serve`**: langlaufender Daemon (launchd-Plist kommt mit M4), Sockets je laufender Session.

## Entscheidungen, die ich getroffen habe (bitte prüfen)
- Dialog per `osascript` statt UserNotifications (Aktions-Buttons gibt es erst mit der App in M4).
- Vorlage wird Version 2 (Basis `bergfried-base-v2`), weil der Socket-Forward in die Vorlage kommt.
- `propose_template_change` kommt nach M2 (siehe DESIGN-NOTES).
