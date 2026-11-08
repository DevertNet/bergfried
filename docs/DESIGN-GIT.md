# Git-Push und CI über den Mac

**Stand:** gebaut und getestet (Go-Tests, echter Durchlauf mit lokalem GitHub-Ersatz). **Noch nicht gegen echtes GitHub geprüft** – siehe „Offen“.

## Ziel
Agents in der VM bearbeiten Projekte. Der Code soll nach GitHub (Branch, Pull Request), und der Agent soll die CI verfolgen und reparieren können
(„fix die CI“) – **ohne dass GitHub-Zugangsdaten in die VM gelangen**. Der Mac ist der einzige mit Zugang und tut alles stellvertretend.

## Warum der Mac pusht (statt Deploy-Key oder Token in der VM)
- Ein Deploy-Key über den SSH-Broker könnte im Zeitfenster auf **jeden** Branch pushen, auch `main` und per Force; ohne Branchschutz (Free-Plan) nicht begrenzbar.
- Ein Token in der VM wäre dort lesbar und bliebe nach der Freigabe gültig.
- Wenn der Mac pusht, erzwingt Bergfried die Regeln: nur `agent/…`, nur Fast-Forward, nie die Standard-/Release-Branches.

## Ablauf `request_git_push`
1. Die VM (MCP-Tool) ermittelt Projekt, `origin`-URL und Branch, legt `git bundle create … refs/heads/<branch> --not --remotes` unter
   `shared/out/.git-push/` und ruft `RequestGitPush` (Daemon-Socket der Session) auf.
2. Der Daemon (`gitpush.Service.Request`): URL parsen (**nur github.com**, keine Zugangsdaten in URLs), Branch prüfen, Bundle **annehmen**
   (Pfad muss unter `shared/out/.git-push/` liegen, nach Auflösung von Symlinks; normale Datei; ≤ 200 MB; ohne Symlink-Folgen in ein privates Verzeichnis kopiert).
3. `gitpush.Pusher.Prepare`: Spiegel-Klon `~/.bergfried/repos/<owner>__<repo>.git` (Remote-URL immer vom Daemon gesetzt), `git fetch` des Standard-Branches und
   des Ziel-Branches, `git bundle verify`, Bundle mit `transfer.fsckObjects=true` in eine Hilfsreferenz holen, **Fast-Forward** gegenüber dem Remote prüfen (sonst ablehnen), Commits/Diffstat/Dateien/Workflows/Geheimnisse analysieren.
4. Es entsteht eine Anfrage (`kind=gitpush`) mit den Details (`requests.extra`, JSON). Die App zeigt das **Prüfblatt**: Commits, Diffstat, Auslöser, Warnungen, Schalter „Pull Request anlegen“.
5. Nach Freigabe: `git push origin <sha>:refs/heads/<branch>` (kein Force), optional Pull Request (`POST /repos/{o}/{r}/pulls`, Basis = Standard-Branch), Vermerk in `git_pushes`, Audit.
6. Die VM zieht `refs/remotes/origin/<branch>` nach, damit spätere Bundles nur neue Commits enthalten.

### Regeln (in `gitpush.CheckBranch`, durch Tests abgesichert)
Erlaubt: `agent/<name>[/…]` aus `A-Za-z0-9._-`. Abgelehnt: `main`, `master`, `trunk`, `develop`, `dev`, `release*`, `production`, `prod`, `staging`, `gh-pages`, alles ohne
`agent/`-Präfix, `..`, `//`, `/.`, Endung `.lock`, Länge > 100. Kein Force, keine Tags, kein Löschen.

### Zusatzbestätigungen im Prüfblatt
- **Workflow-Änderung** (`.github/workflows/**` im Diff) → eigener Haken. Grund: Ein Push startet Workflows mit dem Code des Agents, die je nach Konfiguration auf Repo-Secrets zugreifen (umgeht die VM-Grenze).
- **Mögliche Geheimnisse im Diff** (Muster für Schlüssel, GitHub-/AWS-/Slack-/API-Token, Zugangsdaten in URLs, `…PASSWORD=…`) → eigener Haken. Angezeigt werden nur Datei:Zeile und Art, nie der Wert.
- Angezeigt wird außerdem, **welche Workflows durch Push/PR starten** (inkl. `pull_request_target ⚠`), ausgewertet aus der Version des Agents (HEAD), mit Branch-Filtern.

## CI (GitHub Actions) – `ci_status`, `ci_logs`, `ci_rerun`
| Tool | Wirkung | Bestätigung |
|---|---|---|
| `ci_status` | Läufe des **neuesten Commits** eines Branches (Name, Ergebnis, Link; bei Fehlern Jobs und fehlgeschlagener Schritt) | nein – aber nur für Branches, die **diese Session über Bergfried gepusht hat** |
| `ci_logs(job_id)` | Auszug des Logs (alle `##[error]`-Stellen mit Kontext plus Ende; geschwärzt; ≤ 48 KB) | ja: „nur dieses Mal“ oder „für diese Session (4 h)“ (`ci_grants`) |
| `ci_rerun(run_id)` | `rerun-failed-jobs` | **immer** |

Jede Anfrage prüft über die API, dass Job/Lauf zu einem gepushten Branch der Session gehört. Logs gelten als **fremder Text** (Daten, keine Anweisungen) und sind so gekennzeichnet.
Die Checks-API wird bewusst **nicht** benutzt (für fine-grained Tokens nicht dokumentiert); es reicht die Actions-API.

**App:** CI-Badge je Agent (grün/rot/läuft, Link; Beobachter fragt alle 30 s Branches der letzten 24 h), Mitteilung bei Wechsel auf rot, Button **„Agent: CI fixen“**
(tippt einen **festen** Text in das Agent-Fenster – nie Log-Inhalt – und nur, wenn Claude eingeloggt und im Leerlauf ist).

## GitHub-Zugang
Fine-grained Token im macOS-Schlüsselbund (`security`, Dienst `de.einfach123.bergfried.github`), Einstellungen → „GitHub“ prüft es mit `GET /user`. Benötigte Rechte laut GitHub-Doku
(Seite „Permissions required for fine-grained personal access tokens“): **Actions** (Read: Läufe/Logs, Write: Wiederholen), **Metadata** (Read). Voraussichtlich zusätzlich **Contents: Read and write**
(Push) und **Pull requests: Read and write** – diese beiden Namen sind in den gelesenen Doku-Seiten **nicht ausdrücklich belegt**. Nur die gewünschten Repos auswählen: das Token ist zugleich die Allowlist.
`git push` über HTTPS authentifiziert per `GIT_ASKPASS` (Benutzer = GitHub-Login oder `x-access-token`, Passwort = Token), `credential.helper=` (leer; sonst würde git das Token im Schlüsselbund ablegen).

## Dateien
`daemon/internal/github` (REST-Client, `Keychain`, `Redact`/`FailureExcerpt`), `daemon/internal/gitpush` (Pusher + Service), `daemon/internal/ci` (Service + Watcher),
MCP-Tools in `mcp/cmd/bergfried-mcp`, App-Schnittstelle in `daemon/internal/api` (`/v1/git/decide`, `/v1/ci/decide`, `/v1/github`, `/v1/agents/ci-fix`), `tools/fakegithub` (Testwerkzeug).
Dev-Schalter: `BERGFRIED_GITHUB_API`, `BERGFRIED_GITHUB_GIT` (Basis-URLs), `BERGFRIED_GITHUB_TOKEN` (statt Schlüsselbund) – nur für Entwicklung/Tests.

## Offen / ungeprüft
- **Erster echter Push gegen GitHub** mit einem Test-Repo: Token-Rechte (Contents/Pull requests), Authentifizierung per `GIT_ASKPASS` mit echtem GitHub (lokal gegen `git http-backend` mit Basic-Auth belegt), Verhalten bei privaten Repos.
- Fremde CI (nicht GitHub Actions) und die Checks-API; GitHub Enterprise; andere Hosts als github.com.
- `pr_feedback` (Review-Kommentare lesen) – im Entwurf genannt, **nicht gebaut**.
- Die Oberfläche (Prüfblatt, CI-Badge, Einstellungen) wurde nicht gesehen; getestet ist die Dekodierung realer Daten (`Bergfried --selftest`).
- Automatisches „CI fixen“ nach rotem Lauf ohne Klick gibt es nicht (bewusst: nur Mitteilung und Button).
