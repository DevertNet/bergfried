# M4 – Control-App (macOS)

Gebaut ohne Xcode (siehe [M4-PLAN.md](M4-PLAN.md)):

```bash
make build            # bin/bergfriedd, bin/bergfried-mcp (Docker)
app/build.sh          # app/build/Bergfried.app (swiftc, ad-hoc signiert, Binaries + Vorlage im Bundle)
open app/build/Bergfried.app
```

1. **Einstellungen → „Daemon installieren und starten“** legt einen LaunchAgent an (`~/Library/LaunchAgents/de.einfach123.bergfried.plist`,
   `bergfriedd serve --no-dialog`, Log `~/.bergfried/daemon.log`). Alternativ im Terminal `bergfriedd serve --no-dialog`.
2. **Einstellungen → Basis-VM → „Jetzt bauen“** (einmalig, einige Minuten).
3. **Sessions → „Neue Session“**, danach „Agent +“, „Terminal → Claude-Login“ (einmal pro VM), Start/Stopp, Löschen
   (Warnung bei ungepushten Commits, „Trotzdem löschen“ nach Bestätigung).
4. **Anfragen:** Datei-/Ordner-Anfragen erlauben/ablehnen (große Ordner mit zweiter Bestätigung). SSH-Anfragen: „Entscheiden …“
   → Dauer, Key, Benutzer, Geltung, Einzelbestätigung; bei neuem Host-Key muss der Fingerprint bestätigt werden.
   Aktive Freigaben mit Countdown, Verlängern, Entziehen.
5. **Protokoll:** Filter und CSV-Export (Formeln im CSV werden entschärft).
6. **Menüleiste:** Schild-Symbol mit Zähler offener Anfragen; Datei-Anfragen direkt dort erlauben/ablehnen.
7. **Mitteilungen** mit Aktions-Buttons (Erlauben/Ablehnen, bei SSH „Erlauben (Standard)“ nur bei bekanntem Host und Standard-Key).
   Beim ersten Start fragt macOS nach der Mitteilungs-Erlaubnis.

## Ergänzungen
- **Vorlage bearbeiten:** Einstellungen → „VM-Vorlage“. Gespeichert wird nur, was `limactl validate` besteht und die Isolation wahrt
  (keine Mounts/Home, kein `forwardAgent`, kein Docker-Socket, beide Bergfried-Sockets bleiben). Die Basis-VM heißt
  `bergfried-base-<Inhalts-Hash>`: geänderter Inhalt = neue Basis (einmal bauen), Zurücksetzen nutzt die vorhandene wieder.
  Bestehende Sessions bleiben unverändert; ältere Basis-VMs werden nach einem Neubau entfernt. Datei: `~/.bergfried/template/bergfried.yaml`.
- **Bestehenden Ordner nutzen:** „Neue Session“ → Quelle „Bestehenden Ordner kopieren“ (CLI: `create <name> --import ORDNER`).
  Der Ordner wird **kopiert** (nicht gemountet) nach `~/work/main` der VM: nur unterhalb des Homes, nie das Home selbst, nie gesperrte Pfade;
  ohne `.env`/Schlüssel/Symlinks/`node_modules`/Caches; Zugangsdaten in `.git/config` werden entfernt. Vorschau zeigt Dateien und Größe vorab.
  Ergebnisse kommen per `git push` oder über `shared/out` zurück.
- **Wo liegen Sessions?** VM-Platte `~/.lima/bf-<name>`, Austausch `~/.bergfried/sessions/<name>/shared/{in,out}`, Code in der VM `~/work/main`.
- **Icon:** `app/icon/make-icon.swift` zeichnet es, `app/icon/AppIcon.icns` liegt im Repo.

## Statusanzeige und Login
- **Neue Session:** erscheint sofort als Zeile mit Fortschritt (klonen → starten → Erstkonfiguration → Hilfsprogramm …).
- **Übersicht je Session:** „Claude eingeloggt“ bzw. Button „jetzt einloggen“; je Agent „RC aktiv“ (mit Link öffnen/kopieren), „wartet auf Login“,
  „RC startet …“ oder „kein Fenster“; Platte: auf dem Mac **real belegte** Bytes (sparse) und Belegung in der VM.
  Der Daemon fragt laufende VMs alle 5 s (instabil) bzw. 20 s (alles grün) ab.
- **Login:** Button öffnet iTerm (falls installiert, sonst Terminal) mit `claude auth login`. Erkennt der Daemon danach den Login, startet er die
  Agents der Session automatisch neu (`--continue --remote-control`); beantwortet dabei die Theme-Frage mit der Vorauswahl. Hängt ein Agent,
  hilft „Agent neu starten“ im Agent-Menü.

## Technik
- Die App spricht per **HTTP/JSON über `~/.bergfried/app.sock`** (0600) mit dem Daemon. `Bergfried --selftest` prüft die Verbindung.
- Auf Systemen nur mit Command Line Tools ist `@State` ein Makro ohne Plugin; die App nutzt dafür den Ersatz `@Local` (`Local.swift`).

## Nicht geprüft / offen
- Die Oberfläche wurde **nicht visuell getestet** (keine Bildschirm-/Bedienungshilfen-Rechte in der Entwicklungsumgebung):
  kompiliert, startet, spricht mit dem Daemon (Selftest, Lebenszyklus per API). Layout-Feinheiten, Mitteilungs-Buttons und
  Menüleiste bitte einmal ansehen.
- Ports je Session („Shop auf localhost:8080 öffnen“), Ressourcen-Auslastung (nur konfigurierte Werte), Hosts-Bereich
  (VPS, M5) und „In VS Code öffnen“ (M6) fehlen noch.
- Mitteilungen brauchen ein ad-hoc signiertes Bundle; ohne Apple-Entwickler-Zertifikat kann macOS sie strenger behandeln.
