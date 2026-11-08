# Bergfried – ursprüngliche Aufgabenstellung

> Wörtlich übernommen aus dem Startprompt. Spätere Abweichungen und Entscheidungen stehen in [`HANDOVER.md`](HANDOVER.md).

# Projekt: Bergfried

Du hilfst mir, **Bergfried** zu bauen: ein System, das Claude-Code-Agents isoliert in VMs startet – auf meinem Mac oder auf einem Debian-VPS. Zugriff auf Dateien und SSH gibt es nur nach einzelner Freigabe durch mich. Gesteuert werden die Agents per Claude Code Remote Control (Claude Desktop, claude.ai/code, Handy-App).

Lies diesen Prompt komplett, lege das Repo wie beschrieben an und starte mit **Meilenstein M1**. Arbeite plan-first: Bevor du größere Teile baust, zeig mir kurz den Plan für den Meilenstein und frag nach, wenn etwas unklar ist.

---

## Grundprinzipien (nicht verhandelbar)

1. **Isoliert per Default:** Die VM sieht weder mein Home-Verzeichnis noch meine Keys noch den Host-Docker. Alles, was hineinkommt, ist eine bewusste Freigabe.
2. **Metadaten ja, Inhalte nein:** Der Agent darf Dateinamen aus meinem Home sehen (Index, mit Sperrliste). Inhalte kommen nur nach meiner Bestätigung in einen gemounteten Austauschordner.
3. **Der private SSH-Key verlässt nie den Mac.** Die VM bekommt nur einen Agent-Socket, über den während einer aktiven Freigabe signiert wird.
4. **Alles wird protokolliert:** Jede Anfrage, Freigabe, Ablehnung, jeder Start/Stopp landet im Audit-Log.
5. **Ein Host-Typ, viele Orte:** Mac und Debian-VPS nutzen denselben Daemon und dieselbe VM-Vorlage.
6. **Netzwerk vorerst offen.** Ein Egress-Filter kommt später; plane einen HTTP(S)-Proxy-Hook ein, der vorerst alles durchlässt.

---

## Architektur

```
Claude Desktop / Handy ──Remote Control──▶ Claude Code (in VM)
                                                │ MCP (stdio)
Mac                                             ▼
├─ Bergfried.app (SwiftUI)  ◀─gRPC─▶  bergfriedd  ◀─Unix-Socket (Lima-Forwarding)─▶  bergfried-mcp (in VM)
│                                     │   ├─ Dateinamen-Index + Datei-Kopie nach shared/in
│                                     │   ├─ SSH-Broker (ssh-agent-Protokoll)
│                                     │   └─ Audit-Log (SQLite)
└─ Home + SSH-Keys (verlassen den Mac nie)

Debian-VPS: eigener bergfriedd + Session-VMs aus derselben Vorlage.
Verbindung: Der Mac baut ausgehend eine SSH-Verbindung zum VPS auf und leitet einen
Unix-Socket zurück (ssh -R). Darüber laufen Status, Befehle, Datei- und Signier-Anfragen
in beide Richtungen. (Tailscale + WebSocket später als Alternative.)
```

### Komponenten & Stack

| Komponente | Technik |
|---|---|
| Host-Daemon `bergfriedd` | Go, ein Binary, launchd (Mac) / systemd (Debian). Steuert `limactl`. SSH-Broker mit `golang.org/x/crypto/ssh/agent`. |
| VM-Verwaltung | Lima + YAML-Vorlage `bergfried.yaml`. Mac: `vmType: vz`, virtiofs. Debian: QEMU + KVM (Fallback ohne KVM: Incus-Container – erst später). |
| MCP-Server in der VM `bergfried-mcp` | Go, stdio-MCP, in Claude Code als lokaler Server eingetragen. Spricht über einen per Lima weitergeleiteten Unix-Socket mit `bergfriedd`. |
| App ↔ Daemon | gRPC, Protobuf-Definitionen im Repo; Typen für Go und Swift werden generiert. Lokal über Unix-Socket. |
| Control-App | Nur macOS, nativ: SwiftUI (+ AppKit), `MenuBarExtra`, `UserNotifications` mit Aktions-Buttons, Editor: CodeEditSourceEditor. Gebaut per `xcodebuild`. |
| Datei-Browser | Daemon liest/schreibt VM-Dateien per SFTP über die Lima-SSH-Verbindung. Keine Diff-Ansicht – Reviews über „In VS Code öffnen“ (Remote-SSH). |
| Persistenz | SQLite im Daemon (Sessions, Agents, Freigaben, Audit-Log). |
| Secrets | macOS-Schlüsselbund (Claude-Token, Git-Token). Auf Debian `systemd-creds` oder `pass`. Nie Klartext-Dateien, nie im Repo. |

---

## Sessions & Agents

- **Eine Session = ein Projekt = eine Lima-VM** aus der gemeinsamen Vorlage.
- **Docker ist immer die Basis** in der VM (Docker + Docker Compose). Projekte laufen schon in der Entwicklung per `docker compose` wie später im Hosting. Projektspezifisches steckt in `compose.yaml` und `CLAUDE.md` im Projekt-Repo, nicht in der Vorlage.
- Vorlage installiert: Docker, Docker Compose, tmux, git, Claude Code, `bergfried-mcp`.
- **Mounts pro Session, genau zwei:** `shared/in` (read-only, freigegebene Dateien) und `shared/out` (beschreibbar, Ergebnisse für mich). Code kommt per `git clone` in die VM, nicht als Mount (Performance).
- **Mehrere Agents pro Session:** Jeder Agent bekommt ein eigenes tmux-Fenster, eine eigene Remote-Control-Session und ein eigenes Git-Worktree. Parallele Docker-Stacks bekommen je Worktree einen eigenen `COMPOSE_PROJECT_NAME`. Anfragen tragen Session- und Agent-Name. Hinweis: Innerhalb einer VM ist das keine harte Sicherheitsgrenze (gleicher Linux-User) – echte Trennung = zweite Session.
- **Lebenszyklus:** erstellen → starten (VM bootet, Docker startet, erster Agent in tmux mit Remote Control) → stoppen (Platte bleibt, `claude --continue` holt den Verlauf) → löschen (vorher Warnung bei ungepushten Commits).
- **Claude-Login pro VM:** langlebiges Token (`claude setup-token`) wird beim Start als Secret eingespielt. **Prüfen**, ob das zusammen mit Remote Control funktioniert, und wie Remote Control beim Start aktiviert wird (genauen CLI-Aufruf in der aktuellen Claude-Code-Doku nachsehen, nicht raten).

---

## Datei-Anfragen

MCP-Tools in `bergfried-mcp`:

- `list_files(path, pattern?)` → Namen, Größe, Änderungsdatum. **Keine Inhalte.**
- `search_files(query)` → Suche nach Dateinamen.
- `request_file(path, reason)` → Anfrage an mich; Antwort: Pfad in `shared/in/<session>/…` oder Ablehnung.
- `request_folder(path, reason)` → wie oben; ab Grenze (z. B. 100 MB / 500 Dateien) zweite Bestätigung.

Regeln:

- Der Index umfasst **das ganze Home** – außer der **Sperrliste**, deren Einträge gar nicht erst im Index auftauchen: `~/.ssh`, `~/Library`, `~/.config`, `~/.mcp-einfach123-secrets`, Schlüsselbünde, `.env`-Dateien. Sperrliste ist in der App pflegbar. Anfragen auf gesperrte Pfade lehnt der Daemon ohne Rückfrage ab.
- Der Index ist kein Mount, sondern wird nur über MCP ausgeliefert.
- Bei VPS-Sessions streamt der Mac die freigegebene Datei über die Leitung zum VPS-Daemon. Ist die Leitung weg, warten Datei-Anfragen, bis der Mac wieder da ist.

---

## SSH-Freigabe (Broker)

Entscheidung: **Zuerst der Broker**, kurzlebige SSH-Zertifikate (CA) später als Option pro Host.

- `bergfriedd` stellt einen eigenen ssh-agent-Socket bereit, der per Lima-Socket-Forwarding in die VM kommt (`SSH_AUTH_SOCK`).
- Signiert wird mit meinen echten Keys aus dem vorhandenen ssh-agent / Schlüsselbund / 1Password. Der private Key verlässt den Broker nie.
- **Zeitfenster:** Eine Freigabe gilt für Dauer + Host(s) + Key + Agent (optional: alle Agents der Session). Innerhalb des Fensters wird jeder Login still signiert und nur protokolliert. Ohne Freigabe: ablehnen + Anfrage in der App.
- **Option pro Host:** „jede Verbindung einzeln bestätigen“.
- MCP-Tool `request_ssh(host, minutes?, reason)`. `minutes` ist nur ein Vorschlag – im Anfrage-Dialog wähle ich Dauer (verlängerbar, auch später während der Freigabe), **Key** und Geltung. Die App merkt sich pro Host einen Standard-Key (z. B. eingeschränkter User `claude`). Der Broker zeigt der VM nur den gewählten Key; der Daemon schreibt den passenden `User` in `~/.ssh/config` der VM.
- Host-Prüfung: Ziel-Host muss zur Freigabe passen (Destination Constraints analog OpenSSH ≥ 8.9).
- Bei Ablauf: laufende SSH-Prozesse des Agents beenden; `ControlMaster` in der VM deaktiviert.
- Bei VPS-Sessions geht jede Signier-Anfrage über die Leitung zum Mac. Ist der Mac offline, scheitert der Login sofort.
- Doku-Hinweis in der App: Während einer Freigabe könnte ein Agent einen eigenen Key in `authorized_keys` eintragen → für wichtige Server eingeschränkten User nutzen, dessen `authorized_keys` root gehört (`AuthorizedKeysFile`).

---

## Control-App (macOS, SwiftUI)

Bereiche:

- **Sessions:** alle VMs über alle Hosts – Name, Host, Status, CPU/RAM/Platte, Agents mit Status + Remote-Control-Link, aktive Freigaben. Aktionen: erstellen, starten, stoppen, löschen, Agent hinzufügen/beenden, Terminal öffnen (`limactl shell <vm> tmux attach`), in VS Code öffnen.
- **Anfragen:** offene/vergangene Datei- und SSH-Anfragen mit Begründung. Erlauben (einmal / Session / mit Dauer), ablehnen, Freigabe verlängern oder vorzeitig entziehen. Mitteilungen mit Aktions-Buttons.
- **Dateien:** Baum, Vorschau, nativer Editor, geänderte Dateien laut Git. Datei nach `shared/out` holen.
- **Protokoll:** Audit-Log, filterbar, CSV-Export.
- **Menüleisten-Icon** mit Zähler offener Anfragen.
- **Hosts:** hinzufügen (lokal / VPS per SSH), Status, KVM-Verfügbarkeit.

Freigaben vom Handy sind **zurückgestellt** – vorerst nur in der Mac-App.

---

## Weitere Anforderungen

- Lima-Port-Forwarding: App zeigt offene Ports je Session („Shop auf localhost:8080 öffnen“).
- Git-Push: Deploy-Key pro Repo oder Token pro Repo als eigene Freigabe-Art (`request_git_push`) – später.
- Ressourcen-Limits pro Session, Idle-Auto-Stopp.
- Mac-Ruhezustand: solange Sessions laufen, optional `caffeinate` halten.
- Claude Code in allen VMs aktualisieren; Vorlage versionieren.
- `NOTES.md` pro Agent für den Arbeitsstand.

---

## Repo-Struktur (Vorschlag)

```
bergfried/
├─ proto/                 # gRPC/Protobuf: App ↔ Daemon, Daemon ↔ Daemon, Daemon ↔ MCP
├─ daemon/                # Go: cmd/bergfriedd, internal/{lima,session,broker,index,files,audit,store,remote}
├─ mcp/                   # Go: cmd/bergfried-mcp
├─ vm/                    # bergfried.yaml + Provisioning-Skripte
├─ app/                   # SwiftUI-Xcode-Projekt (ab M4)
└─ docs/
```

Go-Module so schneiden, dass Daemon und MCP-Server sich Protokoll-Code teilen.

---

## Roadmap

| # | Meilenstein | Fertig, wenn … |
|---|---|---|
| M1 | Fundament: Lima-Vorlage, `bergfriedd` als CLI (create/start/stop/delete/list) | Session per CLI starten und Claude per Remote Control vom Handy steuern |
| M2 | Datei-Anfragen: Index, `bergfried-mcp`, Kopie nach `shared/in` | `request_file` wird bestätigt (vorerst per CLI/macOS-Mitteilung) und die Datei liegt in der VM |
| M3 | SSH-Broker: Agent-Socket, Freigabe mit Ablauf, Host und Key | `ssh` klappt nur während der Freigabe, danach Ablehnung |
| M4 | Control-App (SwiftUI) | Alles aus M1–M3 ohne Terminal bedienbar |
| M5 | VPS-Host: `bergfriedd` auf Debian, SSH-Rückkanal, KVM-Check | Session auf dem VPS mit Datei- und SSH-Freigabe vom Mac |
| M6 | Datei-Browser: Baum, nativer Editor, „In VS Code öffnen“ | Dateien einer Session ansehen, bearbeiten, in VS Code öffnen |
| M7 | Handy-Freigaben (zurückgestellt) | – |

M1–M3 sind der sichere Kern und sollen ohne App (CLI + einfache macOS-Mitteilungen) testbar sein.

---

## Offen / vor dem Bauen prüfen

- Unterstützt mein VPS (`vmd48888`, Debian) KVM? (`ls /dev/kvm`) – bestimmt Lima vs. Fallback.
- Welcher SSH-Agent hält meine Keys (Schlüsselbund, 1Password, ssh-agent)? Broker daran koppeln.
- Genaue Claude-Code-Befehle für Remote Control und `setup-token` in der aktuellen Doku verifizieren.
- Lima: Unix-Socket-Forwarding Host → Guest für Agent-Socket und Daemon-Socket; Snapshots bei `vz`.

## Arbeitsweise

- Starte mit M1. Zeig mir zuerst den Plan (Dateien, Befehle, Tests), dann bauen.
- Kleine, testbare Schritte; zu jedem Meilenstein eine kurze Anleitung zum manuellen Test in `docs/`.
- Sicherheitsrelevante Stellen (Broker, Sperrliste, Socket-Rechte) mit Tests absichern.
- Nichts raten bei externen Tools (Lima, Claude Code, OpenSSH): in der Doku nachsehen.
- Keine Secrets ins Repo.
