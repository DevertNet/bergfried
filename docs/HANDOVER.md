# Übergabe: Stand, Entscheidungen, Fallstricke

Diese Datei ist für einen **neuen Agent** (oder Menschen), der Bergfried weiterbaut. Zuerst lesen: dieses Dokument, dann
[`CLAUDE.md`](../CLAUDE.md) (Arbeitsregeln), [`ROADMAP.md`](ROADMAP.md) und – für den nächsten Schritt – [`M5-PLAN.md`](M5-PLAN.md).
Die ursprüngliche Aufgabenstellung (Grundprinzipien, Architektur) steht in [`SPEC.md`](SPEC.md).

## Stand (Meilensteine)

| # | Stand | Hinweis |
|---|---|---|
| M1 Fundament | **fertig** | `bergfriedd` CLI, Lima-Vorlage, Klon aus Basis-VM, Remote Control vom Handy bestätigt |
| M2 Datei-Anfragen | **fertig** | Index mit Sperrliste, `bergfried-mcp`, Kopie nach `shared/in`, Ordner-Import |
| M3 SSH-Broker | **fertig** | ssh-agent-Protokoll mit Destination Constraints, Freigaben mit Ablauf, Hook für `ssh` im Terminal |
| M4 Control-App | **fertig** (ohne Xcode gebaut) | Sessions, Agents, Anfragen, SSH-Freigaben, Protokoll, Einstellungen, Menüleiste, Mitteilungen |
| Git-Push + CI über den Mac | **gebaut** (Branch `agent/git-push-ci`), gegen echtes GitHub noch ungeprüft | [`DESIGN-GIT.md`](DESIGN-GIT.md), [`git-test.md`](git-test.md) |
| M5 VPS-Host | **zurückgestellt** (in einigen Wochen) | Plan: [`M5-PLAN.md`](M5-PLAN.md) |
| M6 Datei-Browser/Editor | offen | „In VS Code öffnen“ wurde bewusst auf M6 verschoben (SSH-Befehl kopieren gibt es schon) |
| M7 Handy-Freigaben | zurückgestellt | laut Spezifikation |

Alles ist auf `main` (GitHub: `DevertNet/bergfried`, privat). `make test` ist grün (Go, in Docker).

## Architektur in 10 Sätzen

1. **Eine Session = ein Projekt = eine Lima-VM** (`~/.lima/bergfried-<name>`, ältere: `bf-<name>`), geklont aus einer **Basis-VM**
   `bergfried-base-<Hash der Vorlage>` (Sekunden statt Minuten). Die Vorlage `vm/bergfried.yaml` ist in der App bearbeitbar
   (Kopie unter `~/.bergfried/template/`), geprüft durch `session.CheckTemplate` (keine Mounts, kein `forwardAgent`, kein Docker-Socket,
   beide Bergfried-Sockets Pflicht).
2. **Genau zwei Mounts je Session:** `~/.bergfried/sessions/<s>/shared/in` (read-only) und `shared/out`. Code kommt per `git clone` oder
   **kopiertem Import** (`importer`, nie gemountet) nach `~/work/main` der VM.
3. **`bergfriedd serve`** (ein Go-Binary; macOS LaunchAgent aus der App) hält: SQLite (`~/.bergfried/bergfried.db`), den Dateinamen-Index,
   die Sockets je Session, den SSH-Broker, die HTTP/JSON-Schnittstelle der App und den **Monitor**.
4. **Identität der Session = der Socket.** Je laufender Session lauscht der Daemon auf `~/.lima/<vm>/sock/bergfried.sock` (gRPC `SessionAPI`)
   und `ssh-agent.sock`; Lima leitet beide per `reverse`-Portforward in die VM (`/run/user/<uid>/bergfried.sock`, `bergfried-agent.sock`).
   Der Agent-Name kommt nur als gRPC-Metadata (in der VM behauptet, keine harte Grenze – gleicher Linux-User).
5. **`bergfried-mcp`** (Go, stdio-MCP-Server, offizielles go-sdk) läuft in der VM; Tools: `list_files`, `search_files`, `request_file`,
   `request_folder`, `request_ssh`. Dazu MCP-Instructions und `~/.claude/bergfried.md` (damit „such auf meinem Computer“ über Bergfried läuft).
   Derselbe Binary hat den Unterbefehl `ssh-hook` (siehe unten).
6. **Dateien:** Index über das Home **ohne** Sperrliste-Pfade (`~/.ssh`, `~/Library`, `~/.config`, `~/.mcp-einfach123-secrets`, `.env*`,
   Schlüssel …). Anfragen auf gesperrte Pfade lehnt der Daemon ohne Rückfrage ab. Kopie nach `shared/in/<session>/…`.
7. **SSH:** Broker spricht das ssh-agent-Protokoll, signiert **upstream** mit dem echten Agent des Macs (`SSH_AUTH_SOCK` des Daemons).
   Signiert wird nur mit aktiver Freigabe (Zeitfenster, Key, Session) **und** passendem Ziel-Host-Key (`session-bind@openssh.com`,
   Fingerprint beim Freigeben aus `known_hosts`/`ssh-keyscan` festgehalten). Kein `session-bind` = ablehnen. Kein Schreiben in den Agent.
8. **`ssh` im Terminal der VM** löst über einen root-eigenen `Match exec`-Hook (`/etc/ssh/ssh_config.d/51-bergfried-hook.conf`) eine
   Anfrage aus; läuft ssh interaktiv (stderr = TTY), **wartet** es, bis entschieden ist. Ohne TTY nur Meldung, kein Warten.
9. **Control-App** (`app/`, SwiftUI, per `swiftc` gebaut) spricht per HTTP/JSON über `~/.bergfried/app.sock` (0600) mit dem Daemon. Der Daemon
   läuft für die App mit `--no-dialog`; Entscheidungen trifft die App. Anfragen erscheinen als macOS-Mitteilung mit Aktions-Buttons,
   Dock-Badge und Zähler in der Menüleiste.
10. **Agents:** je Agent ein tmux-Fenster (`tmux`-Session `bf`), ein Git-Worktree `~/work/<agent>` (`main` = Hauptverzeichnis),
    `COMPOSE_PROJECT_NAME=<session>-<agent>`, `claude --continue --remote-control '<session>/<agent>'`. Der **Monitor** fragt laufende VMs
    ab (Login, RC-Status samt Link, Plattenverbrauch), startet Agents nach dem Login neu, bestätigt Onboarding-Fragen mit Enter und
    aktiviert Remote Control (Rückfrage `y`, Neustart oder `/remote-control`, max. 3 Versuche).

## Wichtige Entscheidungen und Abweichungen von der Spezifikation

- **Claude-Login:** `claude setup-token` funktioniert **nicht** mit Remote Control („requires a full-scope login token“). Deshalb
  Einmal-Login je VM (`claude auth login`, Button in der App), Zugangsdaten liegen in der VM.
- **Kein Xcode, kein SwiftPM:** Auf dem Entwickler-Mac sind nur die Command Line Tools installiert; SwiftPM ist dort defekt, `xcodebuild`
  fehlt. App = `app/build.sh` (swiftc, ad-hoc signiert), **keine Drittanbieter-Abhängigkeiten**. `@State` ist in diesem SDK ein Makro ohne
  Plugin → Ersatz `@Local` (`app/Sources/Local.swift`). Sobald Xcode da ist, kann man ein Xcode-Projekt um dieselben Quellen legen.
- **App ↔ Daemon per HTTP/JSON statt gRPC für Swift** (grpc-swift braucht SwiftPM). gRPC bleibt für VM↔Daemon und CLI↔Daemon.
- **Bergfried-Dialoge per macOS-Mitteilung statt eigenem Popup** (Popup wurde auf Wunsch entfernt).
- **Import statt Mount** für bestehende Ordner (Prinzip „genau zwei Mounts“, Performance).
- **Basis-VM nach Inhalts-Hash** der Vorlage benannt; geänderte Vorlage = einmal neu bauen, alte Basis-VMs werden danach entfernt
  (nur Namen `^bergfried-base-([0-9a-f]{8}|v[0-9]+)$`).
- **Sessionnamen:** neue max. 24 Zeichen, `base` reserviert (Kollision mit Basis-VM-Namen, Unix-Socket-Pfadlänge).
- Go 1.26 (Docker-Image `golang:1.26`), reines Go (`modernc.org/sqlite`, kein cgo).

## Git-Push und CI (kurz)
Der **Mac pusht** für die VM (`request_git_push`: Bundle über `shared/out/.git-push/`, Prüfung, Bestätigung, Push nur nach `agent/…`, optional PR) und liest CI
(`ci_status` ohne Rückfrage für selbst gepushte Branches, `ci_logs`/`ci_rerun` nach Bestätigung). Details, Regeln und offene Punkte: [`DESIGN-GIT.md`](DESIGN-GIT.md).
Testwerkzeug: `tools/fakegithub` (GitHub-Ersatz mit echtem Git-HTTP-Server). Fallstricke aus dem Bau: leere Listen als `null` ausliefern bricht die Swift-Dekodierung
(`Detail.Normalize`); Namen wie `DB_PASSWORD` entgingen der Schwärzung (`\b` vor „password“ greift nach `_` nicht); `git` braucht `credential.helper=` leer, sonst legt macOS das Token im Schlüsselbund ab;
die Weiterleitung beim Log-Download zeigt bei GitHub auf eine andere Domain (Go entfernt den Authorization-Header).

## Sicherheitsinvarianten (nicht aufweichen, durch Tests abgesichert)

- Die VM sieht nie Home, Keys oder Host-Docker. Mounts: nur `shared/in` (ro) und `shared/out`.
- Der private SSH-Key verlässt den Mac nie; Signatur nur upstream, nur mit Freigabe + Host-Key-Bindung; Weiterleitung (`forwarding`) abgelehnt.
- Sperrliste: Vergleich case-insensitiv (macOS), `..`/absolute Pfade/Symlink-Ausbrüche scheitern (`index.Resolve`), Symlinks werden nie
  gefolgt oder kopiert. `PUT /v1/blocklist` darf die vier festen Einträge nicht entfernen.
- Agent-Text (Begründung, Name, Benutzer, Host) ist nur Anzeige-Daten: bereinigt, nie in Shell/AppleScript-Quelltext eingebaut; Hostnamen und
  Benutzer werden per Regex validiert, bevor sie in `~/.ssh/config`-Blöcke gelangen.
- Alle Sockets 0600 in 0700-Verzeichnissen; Datenverzeichnis `~/.bergfried` 0700.
- GitHub-Zugangsdaten verlassen den Mac nie (Schlüsselbund); die VM bekommt nur Ergebnisse. Gepusht wird nur nach `agent/…`, nur Fast-Forward, nie Force; Workflow-Änderungen und mögliche Geheimnisse brauchen eine zusätzliche Bestätigung.
- Alles wird protokolliert (`audit`-Tabelle; App „Protokoll“, CSV mit Formel-Schutz). Audit-Fehler werden nicht verschluckt.

## Fallstricke (haben Zeit gekostet – bitte nicht erneut)

- **zsh:** `log` ist eine Shell-Funktion → `/usr/bin/log show …` benutzen. BSD-`sed -i ''`.
- **limactl-Wrapper:** stdout und stderr trennen (Warnungen auf stderr zerstörten sonst die JSON-Ausgabe). `limactl validate` verlangt
  Dateinamen ohne führenden Punkt. Lima **pinnt den SSH-Host-Key** der VM; nach dem Neuerzeugen der Keys (`firstBoot`) muss
  `ssh.known_hosts` gelöscht werden (`lima.RepinHostKey`), sonst hängt jedes `start`.
- **Klone** erben Maschinen-ID und SSH-Host-Keys der Basis → `firstBoot` erneuert beides.
- **SQLite:** `INSERT` immer mit expliziter Spaltenliste (Spalten kommen per `ALTER TABLE` dazu; positionsabhängige Inserts brachen).
- **gofmt** richtet Struct-Felder neu aus → Python-String-Ersetzungen auf Struct-Definitionen schlagen danach fehl; Vorher `grep` auf die
  aktuelle Form.
- **Unix-Socket-Pfade** sind auf ~104 Zeichen begrenzt (Test-Agent-Sockets deshalb unter `/private/tmp/bft`).
- **Mitteilungs-Berechtigung:** App **nicht** beenden/neu starten, solange die macOS-Abfrage offen ist – das gilt als „abgelehnt“, danach
  fragt macOS nie wieder (nur Systemeinstellungen oder neue Bundle-ID). Aktuelle Bundle-ID: `de.einfach123.bergfried`.
- **Terminal-Start** per `.command`-Datei + `open -a` (AppleScript für iTerm scheiterte still an fehlenden Automationsrechten).
- **tmux-Standardfenster** (`bash`/`shell`) ist kein Agent – der Monitor beachtet nur registrierte Agents; „läuft Claude?“ wird über
  `ps -t <tty>` erkannt (`pane_current_command` zeigt wegen Subshell fälschlich `bash`).
- **Race beim Test:** direkt nach `create` ist der Session-Socket kurz nicht offen; ein `ssh` in dieser Sekunde löst keine Anfrage aus.
- **Docker-Go-Builds:** `make build` baut `bergfriedd` für **darwin/arm64** und `bergfried-mcp` für **linux/arm64** (für den VPS wird
  linux/amd64 nötig → Makefile erweitern).

## RC-Erkennung (wichtig für den Monitor)
- Der Banner „/remote-control is active“ steht am **Anfang** der Ausgabe und rutscht bei längerer Arbeit aus dem Sichtbereich. Der Monitor liest deshalb den
  **ganzen tmux-Scrollback** (`history-limit 20000`) und merkt sich aktive RC **je claude-PID** (`stickyRC`): läuft derselbe Prozess weiter, gilt RC als aktiv.
- `/remote-control` bei bereits aktiver RC **öffnet einen Dialog** („This session is available in the Claude mobile app …“, mit „Disconnect this session“).
  Der Monitor wertet ihn als „RC aktiv“ und schließt ihn mit **Escape** (niemals Enter). Ausweg in der App: Agent-Menü → „Remote Control läuft – als aktiv markieren“.
- Neue claude-PID (Neustart) setzt Zähler und „nicht aktivierbar“ zurück.

## Nicht geprüft / bekannte Lücken

- Die **Oberfläche** der App wurde vom Entwickler-Agent nie visuell gesehen (keine Bildschirmrechte): Layout, Menüleiste, Banner-Buttons
  („Erlauben/Ablehnen“ in der Mitteilung) bitte einmal ansehen. Technisch geprüft: Selftest `Bergfried --selftest`, API, Fensterliste.
- **RC-Automatik** gegen eine echte Claude-Version: Ob `/remote-control` im Leerlauf und die Rückfrage „Enable Remote Control?“ so
  funktionieren, ist nur über die Entscheidungslogik (Unit-Test) abgesichert; geprüft wurde der Weg „Login → Neustart → Onboarding-Enter → RC aktiv“.
- **„Jede Verbindung einzeln bestätigen“** (SSH-Freigabe-Option) gibt es nur im macOS-Dialog-Modus; in der App deaktiviert, der Daemon lehnt
  sie ohne Bestätigungsweg ab (sonst würde jede Verbindung scheitern). Sinnvoll: Bestätigung als eigene App-Anfrage.
- **Git/CI gegen echtes GitHub** ungeprüft (Token-Rechte Contents/Pull requests, `GIT_ASKPASS` mit echtem GitHub), `pr_feedback` nicht gebaut, Oberfläche des Prüfblatts nicht gesehen.
- Fehlt noch: Port-Anzeige je Session („Shop auf localhost:8080 öffnen“), Ressourcen-Limits/Idle-Auto-Stopp, `caffeinate`-Option,
  `propose_template_change` (Entwurf in `DESIGN-NOTES.md`), Aktualisieren von Claude Code in allen VMs,
  „Hosts“-Bereich in der App (kommt mit M5), launchd-Plist für den Daemon ohne App.
- Die VM-Nutzer haben passwortloses `sudo` (Lima-Standard): Grenze ist die VM, nicht der Linux-User.

## So macht man weiter

```bash
git clone git@github.com:DevertNet/bergfried.git && cd bergfried
make test                 # Go-Tests in Docker
make build                # bin/bergfriedd, bin/bergfried-mcp
app/build.sh              # app/build/Bergfried.app
```
Manuelle Tests: `docs/m1-test.md` … `docs/m4-test.md`. Testumgebung für SSH (eigener Key/Agent/sshd, nie die echten Keys!) siehe
`docs/m3-test.md` und den Abschnitt „Testen“ in `CLAUDE.md`.

Vorschlag für den Startprompt eines neuen Agents:

> Lies `docs/HANDOVER.md`, `CLAUDE.md` und `docs/M5-PLAN.md`. Starte mit M5 (VPS-Host). Arbeite plan-first: zeig mir kurz den Plan
> (Dateien, Befehle, Tests) und frag nach, bevor du größere Teile baust. Nichts raten bei externen Tools (Lima, OpenSSH, Claude Code).
