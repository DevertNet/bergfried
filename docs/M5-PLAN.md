# M5 – VPS-Host (zurückgestellt, Plan für später)

**Status:** zurückgestellt (fällig in einigen Wochen, Stand 2026-11-08). Dieser Plan ist **ein Entwurf**: Vor dem Bauen mit dem Nutzer abstimmen
(Abschnitt „Fragen an den Nutzer“) und alles Externe (Lima auf Debian, QEMU, OpenSSH-Optionen) in der aktuellen Doku prüfen – nichts raten.

**Fertig, wenn:** Eine Session auf dem Debian-VPS läuft, die Datei- und SSH-Freigaben kommen vom Mac (Anfragen in der Mac-App), und der Mac sie
über die Leitung beantwortet. (Aus [`SPEC.md`](SPEC.md): „Session auf dem VPS mit Datei- und SSH-Freigabe vom Mac“.)

## Ausgangslage (was schon da ist und wiederverwendet wird)

- Der Daemon `bergfriedd` ist **ein Binary für beide Hosts** (Go, pure Go, cgo-frei) – es braucht nur `GOOS=linux GOARCH=amd64` (oder arm64) im Makefile.
- `internal/api` (HTTP/JSON, Sessions/Agents/Jobs/Anfragen/…) ist die Schnittstelle der App; sie kann **unverändert über einen weitergeleiteten
  Unix-Socket** auch den VPS-Daemon steuern.
- `internal/session` (Lima-Wrapper, Basis-VM, Klonen, Agents, Monitor) ist host-agnostisch, außer `vmType: vz`/`virtiofs` in `vm/bergfried.yaml`.
- Broker (`internal/broker`) trennt schon **Upstream** (`Upstream`-Interface: `List`, `Sign`) vom Rest: dort setzt die Fernsignatur an.
- Index/Freigaben/Sperrliste (`internal/index`, `internal/access`) liegen **auf dem Mac** und bleiben dort (Home und Keys verlassen den Mac nie).

## Zu klärende Punkte (zuerst prüfen)

1. **KVM auf `vmd48888`:** `ls -l /dev/kvm`, `grep -cE '(vmx|svm)' /proc/cpuinfo`, `systemd-detect-virt`. Ein VPS braucht verschachtelte Virtualisierung.
   Ohne KVM: nur Fallback (Incus-Container, laut Spezifikation „erst später“) oder QEMU ohne KVM (zu langsam, vermutlich unbrauchbar).
2. **Debian-Version, Architektur, Nutzer, Ressourcen** (RAM/Platte reichen für mehrere VMs je ~6 GB / 60 GB sparse?).
3. **Lima auf Debian:** Installation (Release-Tarball statt Brew), Version, `qemu-system-x86`, `/dev/kvm`-Rechte (Gruppe `kvm`), `limactl start`
   ohne GUI. In der Lima-Doku prüfen: `vmType: qemu`, **welcher `mountType`** für QEMU (virtiofs ist dort ggf. nicht verfügbar → 9p/reverse-sshfs),
   `portForwards` mit `guestSocket … reverse: true` (funktioniert bei QEMU über SSH-Forwarding; Option `LIMA_SSH_PORT_FORWARDER`?), `limactl clone`
   (Kopie statt `clonefile` auf ext4 – Dauer messen; ggf. qcow2-Backing-File).
4. **Welcher SSH-Agent des Macs** hält die Keys (Schlüsselbund/1Password/ssh-agent) – bisher: `SSH_AUTH_SOCK` des Daemons bzw. `--upstream-agent`.

## Architektur-Vorschlag

### Leitung Mac ⇄ VPS
Der Mac-Daemon baut **ausgehend** eine SSH-Verbindung auf (ein überwachter `ssh`-Prozess, Wiederverbindung mit Backoff,
`ServerAliveInterval`, `ExitOnForwardFailure=yes`, kein ControlMaster) mit zwei Weiterleitungen:

- `-L ~/.bergfried/hosts/<name>/api.sock:/run/bergfried/api.sock` – die **App/der Mac-Daemon steuert den VPS** (Sessions, Agents, Status, Jobs: dieselbe
  HTTP/JSON-`api` wie lokal).
- `-R /run/bergfried/uplink.sock:~/.bergfried/uplink.sock` – der **VPS-Daemon ruft den Mac** (Datei- und Signier-Anfragen). Auf dem VPS
  `StreamLocalBindUnlink yes` (alte Sockets bei Wiederverbindung) in der `sshd_config` prüfen.

Beide Sockets 0600/0700 wie bisher. Der SSH-Key für diese Leitung ist der normale Key des Nutzers (außerhalb des Brokers); idealerweise ein eigener,
eingeschränkter Key (`restrict,permitlisten`/Forced Command) – im Plan mit dem Nutzer klären.

### Neuer gRPC-Dienst `Uplink` (Mac-Seite, in `proto/`)
Aufrufe des VPS-Daemons an den Mac-Daemon, jeweils mit `host` (VPS-Name; der Mac leitet ihn aus dem Socket ab, nicht aus Nutzdaten) und `session`/`agent`:

- `ListFiles`, `SearchFiles` (Index liegt auf dem Mac; Sperrliste gilt unverändert).
- `RequestFile`/`RequestFolder`: Mac zeigt die Anfrage (App/Mitteilung), nach Zustimmung **streamt der Mac die Datei(en) in Chunks** zum VPS-Daemon
  (Server-Streaming); der VPS-Daemon schreibt nach `…/sessions/<s>/shared/in/<s>/…`. **Ist die Leitung weg, warten Anfragen**, bis der Mac wieder da
  ist (Warteschlange mit Zeitlimit, Status in der App sichtbar).
- `SSHList` / `SSHSign`: der VPS-Broker (`Conn`) leitet List/Sign **mit** `session-bind`-Daten (Host-Key-Fingerprint, Signaturdaten, Key-Fingerprint)
  an den Mac-Broker weiter. **Ist der Mac offline, scheitert der Login sofort.** Freigaben, Zeitfenster und Ablauf sind auf dem **Mac** maßgeblich
  (Uhrzeit des Macs).
- `RequestSSH`, `SSHAttempt` (ssh-Hook) analog.

### Datenmodell
- Tabelle `hosts` (Name, Typ `local|ssh`, SSH-Ziel, Status, KVM, Daemon-Version).
- `sessions`, `requests`, `ssh_grants`, `audit` bekommen eine `host`-Spalte (Migration per `ALTER TABLE` wie bisher; **explizite Spaltenlisten in INSERTs!**).
- Die Mac-App zeigt Sessions **aller Hosts** (Spalte „Host“) und verwaltet VPS-Sessions über die `api.sock`-Weiterleitung.

### VM-Vorlage für QEMU
Eigene Vorlagen-Variante (z. B. `vm/bergfried-qemu.yaml`) oder Parameter in `bergfried.yaml`: `vmType: qemu`, Debian 13 `x86_64`-Image, passender
`mountType`, gleiche Socket-Weiterleitungen und Provisionierung. `CheckTemplate` anpassen (erlaubt `qemu`; Isolationsregeln bleiben). `bergfried-mcp`
zusätzlich für `linux/amd64` bauen und je nach VM-Architektur installieren (`InstallMCP`).

### Betrieb auf Debian
- systemd-Unit für `bergfriedd serve` (Benutzerdienst mit `loginctl enable-linger`, oder Systemdienst mit eigenem Benutzer in den Gruppen `kvm`).
- Secrets (Claude-Login liegt wie bisher in der jeweiligen VM; falls der Daemon selbst Geheimnisse braucht): `systemd-creds` oder `pass`, nie Klartext im Repo.
- Installations-/Aktualisierungsskript (`deploy/debian/…`), das die App über SSH anstoßen kann („Host hinzufügen“).

### Mac-App: Bereich „Hosts“
Hinzufügen (lokal / VPS per SSH), Status (Leitung verbunden?, Daemon-Version, **KVM verfügbar**, freie Ressourcen), Installieren/Aktualisieren,
Leitung trennen/verbinden. Sessions-Liste mit Host-Spalte; Anfragen tragen den Host.

## Schritte (jeweils klein und testbar)

1. **Voruntersuchung auf dem VPS** (KVM, Debian, Ressourcen) → Ergebnis in dieses Dokument schreiben; Entscheidung Lima/QEMU vs. Fallback mit dem Nutzer.
2. **Makefile:** `bergfriedd` und `bergfried-mcp` für `linux/amd64` (und ggf. arm64); Installation per `scp`/Skript.
3. **Lima/QEMU-Vorlage**, Basis-VM und Klon **von Hand auf dem VPS** zum Laufen bringen (Mounts, Socket-Forwards, Docker, Claude Code). Zeiten messen.
4. **`bergfriedd serve` auf dem VPS** (ohne Uplink): Sessions lokal auf dem VPS erstellen, Agents, Monitor. Ohne Datei-/SSH-Freigaben (Uplink fehlt → Anfragen
   warten bzw. SSH scheitert) – das ist bereits das gewünschte Verhalten bei fehlender Leitung.
5. **Leitung:** überwachter `ssh`-Prozess im Mac-Daemon (`internal/remote`), Wiederverbindung, Status; `-L`/`-R`-Sockets; Test mit zwei Daemons auf einem Rechner.
6. **`Uplink`-Proto + Server (Mac) + Client (VPS):** zuerst `ListFiles/SearchFiles`, dann `RequestFile` mit Streaming und Warteschlange, dann Ordner.
7. **Fern-Broker:** `SSHList/SSHSign` über die Leitung; Tests wie in `internal/broker` (mit gefälschtem Upstream), plus Offline-Verhalten (Login scheitert sofort).
8. **Datenmodell/App:** `hosts`-Tabelle, `host`-Spalten, Hosts-Bereich, Sessions aller Hosts.
9. **End-to-End:** Session auf dem VPS, `request_file` und `ssh` mit Freigabe am Mac, Leitung trennen/wiederherstellen; `docs/m5-test.md` schreiben.

## Tests
- Go-Tests für Leitung (Fake-`ssh`), `Uplink`-Server/-Client (Streaming, Abbruch, Wartezeit), Fern-Broker (kein `session-bind` → ablehnen; Mac offline → sofort ablehnen),
  Migrationen. Sicherheitsrelevantes (Sperrliste über die Leitung, Socket-Rechte, Host-Identität aus dem Socket) mit Tests absichern.
- Manueller Test auf dem echten VPS nach Schritt 9; Anleitung in `docs/m5-test.md`.

## Risiken
- Verschachtelte Virtualisierung (Leistung/Verfügbarkeit); Mac im Ruhezustand trennt die Leitung (`caffeinate`-Option der Anforderungen berücksichtigen);
  große Dateien (Chunking, Fortschritt, Abbruch); MCP-Tool-Timeouts bei langem Warten (bisher bis 10 Minuten – ob Claude Code so lange wartet, ist ungeprüft).
- Zeit/Uhr: Freigaben laufen nach der Uhr des Macs ab, nicht nach der des VPS.

## Fragen an den Nutzer (vor dem Bauen)
1. Zugang zu `vmd48888` (SSH-Nutzer, Key) und ob ich dort Pakete installieren darf (Lima, qemu-system).
2. Soll die Leitung einen **eigenen eingeschränkten SSH-Key** bekommen?
3. Reicht ein VPS, oder soll das Hosts-Modell schon mehrere VPS vorsehen? (Vorschlag: mehrere, kostet kaum Mehraufwand.)
4. Fallback ohne KVM (Incus) jetzt oder weiter später?
