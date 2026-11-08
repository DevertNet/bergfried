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
- **Wo liegen Sessions?** VM-Platte `~/.lima/bergfried-<name>` (ältere Sessions behalten `bf-<name>`), Austausch `~/.bergfried/sessions/<name>/shared/{in,out}`, Code in der VM `~/work/main`.
- **Icon:** `app/icon/make-icon.swift` zeichnet es, `app/icon/AppIcon.icns` liegt im Repo.

## Statusanzeige und Login
- **Neue Session:** erscheint sofort als Zeile mit Fortschritt (klonen → starten → Erstkonfiguration → Hilfsprogramm …).
- **Übersicht je Session:** „Claude eingeloggt“ bzw. Button „jetzt einloggen“; je Agent „RC aktiv“ (mit Link öffnen/kopieren), „wartet auf Login“,
  „RC startet …“ oder „kein Fenster“; Platte: auf dem Mac **real belegte** Bytes (sparse) und Belegung in der VM.
  Der Daemon fragt laufende VMs alle 5 s (instabil) bzw. 20 s (alles grün) ab.
- **Login:** Button öffnet iTerm (falls installiert, sonst Terminal) mit `claude auth login`. Erkennt der Daemon danach den Login, startet er die
  Agents der Session automatisch neu (`--continue --remote-control`); beantwortet dabei die Theme-Frage mit der Vorauswahl. Hängt ein Agent,
  hilft „Agent neu starten“ im Agent-Menü.

## Terminal und Remote-Control-Überwachung
- **Terminal-App:** Einstellungen → Terminal (Automatisch = iTerm, falls installiert). Gestartet wird über eine temporäre `.command`-Datei
  (`open -a iTerm|Terminal`, löscht sich selbst) – ohne AppleScript-Automationsrechte. Fehler erscheinen als Hinweis in der App.
  iTerm öffnet dabei einen neuen Tab im bestehenden Fenster.
- **Statusabfrage erst wenn die VM bereit ist:** Während Start/Stopp/Erstellen wird die Session nicht abgefragt; ein Fehler wird erst nach
  mehreren fehlgeschlagenen Abfragen (ca. 30 s) als dezenter Hinweis gezeigt.
- **RC wird überwacht und aktiviert:** Ist Claude eingeloggt und ein Agent hat nach ca. 15–20 s keine aktive Remote Control, handelt der Daemon:
  offene Rückfrage „Enable Remote Control?“ → mit `y` beantworten; Claude läuft nicht (nur Shell) → Agent neu starten;
  Claude im Leerlauf → `/remote-control` eintippen. Je Agent höchstens 3 Versuche (Abstand 20 s), danach „RC nicht aktivierbar – neu starten“.
  Alles steht im Protokoll (`agent.rc_enable`). Ein Agent, der gerade arbeitet, wird nicht gestört.

## Agents, erster Agent und RC-Automatik (Stand 2)
- **Erster Agent:** Eine neue Session startet automatisch den Agent `main` (tmux-Fenster, Remote Control). Ältere Sessions ohne Agent bekommen
  `main` einmalig nachträglich (danach nie wieder automatisch – auch nicht, wenn du alle Agents entfernst).
- **Login → RC:** Nach dem Login erkennt der Daemon „eingeloggt“ und startet die Agents neu; Onboarding-Fragen (Theme,
  „Press Enter to continue“) bestätigt er mit Enter. Wartet Claude auf etwas anderes, zeigt die App „wartet vermutlich auf eine Eingabe – tmux öffnen“.
- **Nur registrierte Agents** werden überwacht (kein tmux-Standardfenster „bash“); „läuft Claude?“ wird über den Prozess erkannt, nicht über die Shell.
- **Agents anzeigen/entfernen:** Je Agent eine Zeile mit Status, Verzeichnis, „RC-Link öffnen“ und Menü (Link kopieren, Neu starten, tmux, Entfernen …).
  Entfernen: „nur Agent beenden“ (Verzeichnis bleibt) oder „Agent und Verzeichnis löschen“ (Worktree, Branch `agent/<name>`, Docker-Stack) –
  mit Prüfung auf ungepushte/ungesicherte Arbeit und separater Bestätigung; das Verzeichnis von `main` wird nie gelöscht.
- **VM-Namen:** neue Sessions heißen `bergfried-<name>` (max. 24 Zeichen; `base` ist reserviert). Die Aufräum-Logik löscht nur Basis-VMs mit Hash-Namen.

## Benachrichtigungen, Hinweise für Claude, SSH-Benutzer (Stand 3)
- **Mitteilungen:** macOS lieferte die Mitteilungen zwar aus, zeigte sie aber mit Stil „Ohne“ (ad-hoc signierte App; die Berechtigungsanfrage
  schlägt mit `UNErrorDomain 1` fehl). Das lässt sich nur in den Systemeinstellungen ändern. Einstellungen → „Benachrichtigungen bei Anfragen“
  zeigt den Stand, sendet eine Testmitteilung und öffnet die Systemeinstellungen. **Unabhängig davon** erscheint bei jeder offenen Anfrage ein
  eigenes Popup-Fenster oben rechts (Erlauben/Ablehnen; SSH und große Ordner öffnen die App), dazu springt das Dock-Symbol und es gibt ein Badge.
- **Hinweise für Claude (damit „such auf meinem Computer“ über Bergfried läuft):** Beim Erstellen und Starten einer Session legt der Daemon
  `~/.claude/bergfried.md` in der VM an (per `@`-Import in `~/.claude/CLAUDE.md`; eigener Inhalt dort bleibt erhalten) und der MCP-Server liefert
  dieselben Hinweise als MCP-Instructions. Sie erklären: „mein Computer/Mac/Dateien“ = Mac außerhalb der VM, erst `list_files`/`search_files`,
  dann `request_file`, SSH nur über `request_ssh`, Ergebnisse nach `shared/out`. **Bereits laufende Agents** lesen das erst nach einem Neustart
  (Agent-Menü → Neu starten).
- **SSH-Benutzer vom Agent:** `request_ssh` hat jetzt den Parameter `user`. Der Dialog füllt das Feld mit dem Wunsch des Agents vor („Vom Agent
  angefragt“); eine Eingabe des Nutzers hat Vorrang, sonst gilt der Standard des Hosts. Ungültige Namen (Großbuchstaben, Sonderzeichen) werden abgelehnt.
- **Anzeige:** Agents stehen eingerückt in einer eigenen Fläche mit Führungslinie unter der Session; `main` erscheint sofort mit „wird gestartet …“.

## SSH-Versuche im Terminal, Shell, Mitteilungs-Berechtigung (Stand 4)
- **`ssh` im Terminal löst eine Anfrage aus:** In jeder VM liegt `/etc/ssh/ssh_config.d/51-bergfried-hook.conf` (root-eigen) mit einem
  `Match exec`, das bei jedem ssh-Start `bergfried-mcp ssh-hook <host> <port> <user>` aufruft. Der Hook meldet den Versuch dem Daemon, der –
  falls es keine aktive Freigabe und keine offene Anfrage für den Host gibt – im Hintergrund eine Anfrage anlegt (Quelle „terminal“, Benutzer
  aus dem Befehl). `ssh` selbst läuft unverändert weiter und wird abgelehnt; im Terminal steht: „SSH-Freigabe für … angefragt – bitte in der App
  bestätigen, danach den Befehl wiederholen.“ Nach einer Ablehnung wird derselbe Host 60 s lang nicht erneut gemeldet. Laufende VMs bekommen den
  Hook einmal je Daemon-Start nachgezogen (`vm.refresh` im Protokoll).
- **Shell/SSH in die VM:** Session-Zeile → „Shell / SSH“: normale Shell (`limactl shell`) in iTerm/Terminal, „SSH-Befehl kopieren“
  (`ssh -F ~/.lima/<vm>/ssh.config lima-<vm>`, auch für VS Code Remote-SSH nutzbar). Den Claude-Login gibt es dort ebenfalls.
- **Terminal-Button pro Agent:** In der Agent-Zeile öffnet „iTerm: Claude-Fenster“ das tmux-Fenster genau dieses Agents.
- **Mitteilungs-Berechtigung:** Der Auslöser ist `requestAuthorization` beim App-Start. Die frühere Kennung war bei macOS als „abgelehnt“
  gespeichert, dann fragt macOS nie wieder. Die App nutzt deshalb die Bundle-ID `de.einfach123.bergfried`; beim ersten Start erscheint
  die macOS-Abfrage „Bergfried möchte Mitteilungen senden“ (mit „Erlauben“ bestätigen). Der Stand steht in den Einstellungen.

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
