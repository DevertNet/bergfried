# Arbeitsregeln für Agents in diesem Repo

Projekt **Bergfried**: Claude-Code-Agents isoliert in Lima-VMs (Mac, später Debian-VPS), Freigaben für Dateien/SSH nur nach Bestätigung.
Der Nutzer schreibt Deutsch – Antworten, Doku und Commit-Nachrichten auf Deutsch, knapp und konkret.

**Zuerst lesen:** [`docs/HANDOVER.md`](docs/HANDOVER.md) (Stand, Entscheidungen, Fallstricke), dann [`docs/ROADMAP.md`](docs/ROADMAP.md);
Ursprungsauftrag: [`docs/SPEC.md`](docs/SPEC.md); nächster Schritt: [`docs/M5-PLAN.md`](docs/M5-PLAN.md).

## Vorgehen
- **Plan-first:** vor größeren Teilen kurz den Plan (Dateien, Befehle, Tests) zeigen und nachfragen, wenn etwas unklar ist.
- Kleine, testbare Schritte; zu jedem Meilenstein eine kurze Anleitung zum manuellen Test in `docs/mN-test.md`.
- **Nichts raten bei externen Tools** (Lima, OpenSSH, Claude Code, macOS): in der Doku/`--help`/Log nachsehen und Ergebnis festhalten.
- Sicherheitsrelevante Stellen (Broker, Sperrliste, Socket-Rechte, Importer, Vorlagenprüfung) **immer mit Tests** absichern.
- Ehrlich berichten, was **nicht** geprüft ist (z. B. Oberfläche nie gesehen) – Details in `HANDOVER.md`.

## Bauen und testen (alles Go läuft in Docker, lokal ist kein Go nötig)
```bash
make test          # go test ./... in golang:1.26
make vet && make fmt
make build         # bin/bergfriedd (darwin/arm64), bin/bergfried-mcp (linux/arm64)
make proto         # Protobuf/gRPC neu erzeugen (Docker); generierter Code liegt in proto/gen und ist eingecheckt
app/build.sh       # app/build/Bergfried.app (swiftc; kein Xcode, kein SwiftPM!)
```
Daemon lokal starten (Standard für die App; die App kann ihn auch als LaunchAgent installieren):
```bash
export BERGFRIED_HOME=$HOME/.bergfried BERGFRIED_TEMPLATE=$PWD/vm/bergfried.yaml
./bin/bergfriedd serve --no-dialog
```

## Testen mit echten VMs und SSH
- Test-Sessions mit eindeutigem Namen anlegen und **danach löschen** (`bergfriedd delete <name> --force` oder App/API). **Vorhandene Sessions des
  Nutzers nie verändern oder löschen** (nur lesen).
- SSH-Tests **nie mit den echten Keys des Nutzers:** eigener Schlüssel (`ssh-keygen`), eigener `ssh-agent` (Socket kurz halten, z. B.
  `/private/tmp/bft/agent.sock`, Unix-Limit ~104 Zeichen), Test-sshd in Docker (`lscr.io/linuxserver/openssh-server`, Port 2222, `PUBLIC_KEY`),
  `known_hosts`-Testdatei mit Eintrag `[host.lima.internal]:2222`, Daemon mit `--upstream-agent <Testsocket> --known-hosts <Testdatei>` starten.
  Ziel aus der VM: `host.lima.internal:2222`. Danach alles wieder abbauen und den Daemon normal neu starten.
- Interaktives `ssh` in der VM simulieren: `script -qec "ssh …" /dev/null` (stderr ist dann ein TTY).
- Aufrufe des MCP-Servers ohne Claude: JSON-RPC über stdio (siehe `docs/m2-test.md`), Identität = Socket.

## Stolpersteine (Kurzfassung – Details in HANDOVER.md)
- zsh: `/usr/bin/log` statt `log`; BSD-`sed -i ''`. gofmt richtet Structs neu aus (String-Ersetzungen vorher prüfen).
- INSERTs mit expliziter Spaltenliste; neue Spalten per `ALTER TABLE` (Migration in `store.migrate`).
- `limactl`: stdout/stderr trennen; Host-Key-Pin nach `firstBoot` löschen; Vorlagen-Dateiname ohne führenden Punkt.
- App: kein `@State` (Makro ohne Plugin) → `@Local`; **App nicht beenden/neu starten, während eine macOS-Berechtigungsabfrage offen ist.**
- Keine Secrets ins Repo; nichts committen, was `~/.bergfried`-Inhalte, Tokens oder Keys enthält.

## Konventionen
- Go: ein Modul (`github.com/DevertNet/bergfried`), `daemon/internal/*` (Daemon), `mcp/cmd/bergfried-mcp`, `proto/gen/…` (generiert, nicht von Hand ändern).
- Namen: neue VMs `bergfried-<session>` (≤ 24 Zeichen, `base` reserviert), Basis-VM `bergfried-base-<Hash>`, tmux-Session `bf`.
- Daten des Daemons: `~/.bergfried` (0700): DB, `blocklist.json`, `hosts.json`, `template/`, `sessions/<name>/shared/{in,out}`, `admin.sock`, `app.sock`.
- Commits: kurze deutsche Betreffzeile; Doku (`docs/`) im selben Commit aktualisieren, wenn sich Verhalten ändert.
