# M4 – Control-App (Plan und Abweichungen)

**Ziel:** Alles aus M1–M3 ohne Terminal bedienbar.

## Umgebung (wichtig)
Auf dem Entwickler-Mac ist **kein Xcode** installiert, nur die Command Line Tools (Swift 6.4.2). Dort ist SwiftPM defekt
(selbst ein leeres Paket scheitert beim Linken des Manifests), `xcodebuild` fehlt. Deshalb:

- Die App hat **keine Drittanbieter-Abhängigkeiten** und wird mit `swiftc` gebaut (`app/build.sh` → `app/build/Bergfried.app`,
  ad-hoc signiert). Sobald Xcode da ist, lässt sich dasselbe als Xcode-Projekt aufsetzen (Quellen unverändert).
- **App ↔ Daemon:** zweiter Socket `~/.bergfried/app.sock` (0600) mit **HTTP/JSON** (Go `net/http`, Swift `Network.framework`).
  Abweichung von „gRPC für Swift“, weil grpc-swift ohne SwiftPM nicht baubar ist. Die gRPC-Schnittstellen für VM und CLI bleiben.
- Der Datei-Editor (CodeEditSourceEditor) und „In VS Code öffnen“ gehören laut Roadmap zu M6.

## Umfang M4
- Daemon: `internal/api` (Sandboxes, Agents, Anfragen, SSH-Freigaben, Protokoll inkl. CSV, Sperrliste, Status, Jobs für lange Aktionen).
- App: Menüleisten-Icon mit Zähler offener Anfragen, Fenster mit Sandboxes / Anfragen / Protokoll / Einstellungen,
  Mitteilungen mit Aktions-Buttons (Erlauben/Ablehnen), Daemon als LaunchAgent installieren/starten, Terminal öffnen.
- Der Daemon der App läuft mit `--no-dialog`; Entscheidungen trifft die App.
