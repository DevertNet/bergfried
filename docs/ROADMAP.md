# Roadmap

Stand: 2026-11-08. Übergabe-Hinweise: [`HANDOVER.md`](HANDOVER.md), Ursprungsauftrag: [`SPEC.md`](SPEC.md).

| # | Meilenstein | Fertig, wenn … | Stand |
|---|---|---|---|
| M1 | Fundament: Lima-Vorlage, `bergfriedd` als CLI (create/start/stop/delete/list) | Sandbox per CLI starten und Claude per Remote Control vom Handy steuern | **fertig** ([m1-test](m1-test.md)) |
| M2 | Datei-Anfragen: Index, `bergfried-mcp`, Kopie nach `shared/in` | `request_file` wird bestätigt und die Datei liegt in der VM | **fertig** ([m2-test](m2-test.md)) |
| M3 | SSH-Broker: Agent-Socket, Freigabe mit Ablauf, Host und Key | `ssh` klappt nur während der Freigabe | **fertig** ([m3-test](m3-test.md)) |
| M4 | Control-App (SwiftUI) | Alles aus M1–M3 ohne Terminal bedienbar | **fertig**, ohne Xcode gebaut ([m4-test](m4-test.md)) |
| M5 | VPS-Host (Debian, SSH-Rückkanal, KVM-Check) | Sandbox auf dem VPS mit Datei- und SSH-Freigabe vom Mac | **zurückgestellt** – in einigen Wochen; Plan: [`M5-PLAN.md`](M5-PLAN.md) |
| M6 | Datei-Browser, Editor, „In VS Code öffnen“ | Dateien einer Sandbox ansehen/bearbeiten | offen |
| M7 | Handy-Freigaben | – | zurückgestellt (laut Spezifikation) |

## Git-Push und CI über den Mac
Gebaut auf Branch `agent/git-push-ci` (siehe [`DESIGN-GIT.md`](DESIGN-GIT.md)); **offen:** erster Test gegen echtes GitHub, `pr_feedback`.

## Weitere offene Punkte (nicht an einen Meilenstein gebunden)
Port-Anzeige je Sandbox, Ressourcen-Limits und Idle-Auto-Stopp, `caffeinate`-Option, `propose_template_change`
([`DESIGN-NOTES.md`](DESIGN-NOTES.md)), Claude Code in allen VMs aktualisieren, „jede SSH-Verbindung einzeln bestätigen“ in der App,
Egress-Proxy-Hook (Grundprinzip 6), App als Xcode-Projekt, visuelle Prüfung der Oberfläche. Vollständige Liste: [`HANDOVER.md`](HANDOVER.md).
