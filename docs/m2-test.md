# M2 – manueller Test (Datei-Anfragen)

```bash
make build
export BERGFRIED_TEMPLATE=vm/bergfried.yaml
./bin/bergfriedd base build        # Vorlage v2 (Socket-Forward), einmalig
./bin/bergfriedd serve             # Terminal 1: Daemon (Index, Sockets, Freigabe-Dialog)
./bin/bergfriedd create shop       # Terminal 2: installiert bergfried-mcp in der VM
./bin/bergfriedd login shop        # einmal pro VM
./bin/bergfriedd agent add shop main
```

Im Agent (Handy/claude.ai/code): „Liste meine Dateien in Documents auf und hol mir <Datei>“.

- `list_files` / `search_files` liefern nur Namen, Größe, Datum. `~/.ssh`, `~/Library`, `~/.config`, `.env` & Co. tauchen nicht auf.
- `request_file` zeigt einen macOS-Dialog (**Ablehnen** ist Standard). Alternativ ohne Dialog: `serve --no-dialog` und
  `./bin/bergfriedd requests list` → `requests allow <id>` / `requests deny <id>`.
- Nach Zustimmung liegt die Kopie in der VM unter `~/.bergfried/sessions/<session>/shared/in/<session>/…` (read-only).
- Gesperrte Pfade werden ohne Dialog abgelehnt (`verdict: blocked`) und erscheinen im Protokoll: `./bin/bergfriedd audit`.
- Ordner über 100 MB / 500 Dateien verlangen eine zweite Bestätigung (`requests allow <id> --confirm-large`).

Sperrliste anpassen: `~/.bergfried/blocklist.json` (Pfade relativ zum Home, Namensmuster). Fehlt die Datei, gelten die Standardwerte
(Spezifikation plus `.gnupg .aws .kube .docker .netrc .git-credentials .claude .bergfried .lima`). `serve` neu starten.

## Bekannte Grenzen

- Der Agent-Name im Dialog ist eine Angabe aus der VM (gleicher Linux-User) – kein Beweis. Die **Session** ergibt sich aus dem Socket.
- Zwischen Prüfung und Kopie liegt ein kurzes Zeitfenster (TOCTOU) – jede Datei wird beim Kopieren erneut aufgelöst und geprüft, Symlinks werden nie kopiert.
- Eine Anfrage wartet bis zu 10 Minuten; ob Claude Code so lange auf ein MCP-Tool wartet, ist noch nicht geprüft.
