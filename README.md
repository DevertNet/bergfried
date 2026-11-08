# Bergfried

Startet Claude-Code-Agents isoliert in Lima-VMs – auf dem Mac oder einem Debian-VPS.
Zugriff auf Dateien und SSH gibt es nur nach einzelner Freigabe. Gesteuert per Claude Code Remote Control.

Siehe [docs/](docs/) für Architektur und Roadmap. Für die Weiterarbeit (auch mit einem neuen Agent): [`docs/HANDOVER.md`](docs/HANDOVER.md), [`CLAUDE.md`](CLAUDE.md), [`docs/ROADMAP.md`](docs/ROADMAP.md).

Stand: M1–M4 fertig, **M5 (VPS-Host) zurückgestellt** – Plan in [`docs/M5-PLAN.md`](docs/M5-PLAN.md).

## Schnellstart (M1)

Siehe [docs/m1-test.md](docs/m1-test.md). Gebaut wird Go in Docker (`make build`), Lima läuft nativ auf dem Mac.
