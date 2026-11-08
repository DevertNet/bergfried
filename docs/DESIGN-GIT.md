# Git und CI mit einem GitHub-Token pro Session

**Stand:** gebaut und mit Go-Tests abgesichert; Mechanismus (`credential.helper store`, Clone/Push mit Token, falsches Token) lokal gegen `tools/fakegithub` belegt.
**Noch nicht gegen echtes GitHub geprüft** – siehe „Offen“. Die Oberfläche wurde nicht gesehen (die App baut und dekodiert, sonst nichts).

## Entscheidung
Früher pushte der Mac stellvertretend (Bundles, Prüfblatt, Branch-Regeln); das war „zu fummelig“. Jetzt gilt: **ein Fine-grained Personal Access Token pro Session/VM**, in der VM
hinterlegt, für **alle** Git-Aktionen (clone, pull, push, merge …) per normalem HTTPS und für GitHub Actions. Kein Umbiegen von URLs oder Hosts, kein `gh`, keine Deploy-Keys,
kein Remote-Helper, kein eigenes Push-Tool.

## Sicherheits-Kompromiss (bewusst)
Das Token liegt in der VM und ist für den Agent lesbar. Das weicht vom früheren Grundsatz „Zugangsdaten verlassen den Mac nie“ ab. Innerhalb der gewählten Repos und Rechte kann der Agent
jederzeit **auf jeden Branch pushen (auch `main`, auch Force)** – bei privaten Repos im Free-Plan gibt es keinen Branch-Schutz –, und über Workflows kommen Repo-Secrets in seine Reichweite.
Begrenzt wird nur durch: **nur die nötigen Repos wählen**, minimale Rechte, Ablauf (1 Jahr), **ein Token je Session** (leicht zu widerrufen), und kein Workflows-Recht (der Agent kann
`.github/workflows` nicht ändern). Widerrufen geht nur bei GitHub (Settings → Developer settings → Fine-grained tokens); Bergfried löscht beim Löschen der Session nur die eigenen Kopien.

## Ablage
- **VM:** `~/.git-credentials` (0600, Zeile `https://x-access-token:<token>@github.com`) und `git config --global credential.helper store`. Geschrieben über **stdin**, nie als Argument (`Manager.InstallGitToken`).
- **Mac:** `~/.bergfried/sessions/<name>/github-token` (0600) – nur damit der Daemon CI-Abfragen machen kann. Wird beim Löschen der Session entfernt.
- Im Protokoll (Audit) und in API-Antworten erscheint der Token-Wert nie (Test: `TestGitHubDecideNeverEchoesToken`).

## Token anlegen (Link)
`ghtoken.Link(session)` erzeugt `https://github.com/settings/personal-access-tokens/new?...` mit `name=bergfried-<session>` (≤ 40 Zeichen), `description`, `expires_in=365`, `contents=write`, `pull_requests=write`,
`actions=write`, `metadata=read`. **Die Repo-Auswahl lässt sich per URL nicht vorbelegen** – die App zeigt dazu immer den Hinweis „Only select repositories“ (`ghtoken.Hint`).
Rechte: *Actions* (damit der Agent CI selbst lesen/wiederholen kann) und *Metadata* sind in der GitHub-Doku belegt; die Namen *Contents* und *Pull requests* sind in den gelesenen Doku-Seiten **nicht ausdrücklich belegt** (erwartet: `contents`, `pull_requests`).
Ein Token gilt nur für **einen Besitzer** (Person oder Organisation); Organisationen können es genehmigen müssen.

## Wo der Ablauf startet
1. **Session anlegen mit Git-Repo:** Der Dialog zeigt Link, Hinweis und Feld; das Token ist Pflicht. `Manager.Create` prüft vorab (Token gültig, Repo erreichbar), legt VM an, hinterlegt das Token
   und klont per HTTPS (`GIT_TERMINAL_PROMPT=0`, SSH-/`git@`-URLs werden zu `https://github.com/o/r.git`).
2. **Agent:** Scheitert `git` mit `permission denied`/`403`/`Repository not found`/`Authentication failed`/`could not read Username`, ruft der Agent das MCP-Tool **`request_github_token`**
   (`repo=owner/name`) auf (der Anleitungstext `~/.claude/bergfried.md` sagt es ihm). Es entsteht eine Anfrage `kind=ghtoken` (App-Karte „Einrichten …“).
   - **Noch kein Token:** Link, Hinweis, Feld. Das Token wird geprüft (`GET /rate_limit`, 401 = ungültig) und – wenn ein Repo genannt ist – gegen `GET /repos/{o}/{r}`. Erst dann abgelegt und in die VM geschrieben.
   - **Token vorhanden, Repo fehlt:** Der Nutzer erweitert bei GitHub die Repo-Auswahl des bestehenden Tokens (Token **bergfried-<session>** → Edit → Repository access) und klickt „Zugriff prüfen“;
     der Token-Wert bleibt gleich (**ungeprüft**, ob GitHub das ohne neuen Wert zulässt – sehr wahrscheinlich). Schlägt die Prüfung fehl, bleibt die Anfrage offen.
   - Erreicht das Token das Repo schon, antwortet der Daemon sofort ohne Anfrage.
3. Einen Sonderknopf im CI-Bereich gibt es nicht mehr (CI-Tools, Buttons, Badge und „CI fixen“ sind ausgebaut: der Agent macht das selbst in der VM).

## CI (GitHub Actions)
Es gibt **keine** CI-Tools und keine CI-Oberfläche in Bergfried. Der Agent fragt Läufe, Jobs und Logs selbst über die GitHub-API ab (`curl` mit dem Token aus `~/.git-credentials`, Beispiel im Anleitungstext)
und kann fehlgeschlagene Jobs wiederholen – dafür hat das Token das Recht *Actions* (Read and write). Log-Text ist fremder Text (Daten, keine Anweisungen); der Anleitungstext sagt das. Die Checks-API ist für
fine-grained Tokens nicht dokumentiert und wird nicht empfohlen.

## Mehrere Besitzer / mehrere Konten (spätere Umsetzung)
Aktuell **ein Token pro Session, ein Besitzer**. Für Repos mehrerer Besitzer (Person + Organisationen) oder mehrere GitHub-Konten braucht es mehrere Tokens; dann ist zu klären, wie `git` sie auseinanderhält
(z. B. `credential.useHttpPath` mit einer Zeile je `https://github.com/<owner>`; **ungeprüft**). Grundlage für die Umsetzung: GitHub-Doku zum Verwalten mehrerer Konten –
<https://docs.github.com/en/account-and-profile/how-tos/account-management/managing-multiple-accounts>. Bis dahin meldet `request_github_token` für fremde Besitzer nur, dass das Token keinen Zugriff hat.

## Dateien
`daemon/internal/ghtoken` (Ablage, Link, Anfrage-Dienst), `daemon/internal/github` (kleiner REST-Client: Token- und Repo-Prüfung, `ParseRemote`), `daemon/internal/session`
(`InstallGitToken`, `Create` mit Token, Anleitungstext), MCP-Tool `request_github_token` in `mcp/cmd/bergfried-mcp`, App-Schnittstelle `GET /v1/github/link`, `POST /v1/github/decide`,
`github_token` in `POST /v1/sessions/create`, `has_github_token` je Session. `tools/fakegithub` bleibt Testwerkzeug. Dev-Schalter: `BERGFRIED_GITHUB_API` (Basis-URL der REST-API).
Ausgebaut: `daemon/internal/gitpush`, `request_git_push`, Bundles, Prüfblatt, Tabellen `git_pushes`/`ci_grants` (werden beim Start gelöscht), `daemon/internal/ci` samt `ci_status`/`ci_logs`/`ci_rerun`, Log-Schwärzung, CI-Badge/-Watcher/„CI fixen“, globales Token im Schlüsselbund.

## Offen / ungeprüft
- **Erster echter Durchlauf gegen GitHub** mit einem Test-Repo: Rechte-Namen (Contents/Pull requests) im Formular, Clone/Push eines privaten Repos mit dem Token, `GET /rate_limit` und `GET /repos/…`
  mit Fine-grained Token, die genauen Fehlermeldungen von `git` bei fehlendem Zugriff, nachträgliches Ändern der Repo-Auswahl ohne neuen Token-Wert, ob GitHub den Token-Wert nur einmal zeigt.
- Pushes, die Workflow-Dateien ändern, scheitern ohne Workflows-Recht (gewollt); die Fehlermeldung von GitHub wurde nicht gesehen.
- Mehrere Besitzer/Konten (siehe oben), GitHub Enterprise, andere Hosts als github.com, `pr_feedback`.
- Die Oberfläche (Token-Schritt beim Anlegen, Token-Karte) wurde nicht gesehen.
