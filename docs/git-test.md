# Test: Git-Push und CI

## Automatisch
`make test` (u. a. `gitpush` gegen einen echten `git http-backend` mit Basic-Auth, `ci`, `github`, `api`).

## End-to-End ohne echtes GitHub (`tools/fakegithub`)
```bash
make build fakegithub
# Bare-Repo mit Startcommit
mkdir -p /private/tmp/bft/repos/o && git init -q --bare /private/tmp/bft/repos/o/r.git && git -C /private/tmp/bft/repos/o/r.git config http.receivepack true
git clone -q /private/tmp/bft/repos/o/r.git /private/tmp/bft/seed && cd /private/tmp/bft/seed && git checkout -b main && echo "# r" > README.md && git add . && git commit -qm init && git push -q origin main && cd -
./bin/fakegithub -addr 127.0.0.1:8099 -root /private/tmp/bft/repos -token e2e-token &
# Checkout für den Import: Remote-URL wie bei GitHub, damit der Daemon sie versteht
git clone -q /private/tmp/bft/repos/o/r.git ~/bergfried-e2e/proj && git -C ~/bergfried-e2e/proj remote set-url origin https://github.com/o/r.git
# Daemon gegen den Ersatz
BERGFRIED_GITHUB_API=http://127.0.0.1:8099 BERGFRIED_GITHUB_GIT=http://127.0.0.1:8099 BERGFRIED_GITHUB_TOKEN=e2e-token ./bin/bergfriedd serve --no-dialog
```
Dann: Session mit `import_path` anlegen (API `POST /v1/sessions/create`), in der VM `git checkout -b agent/e2e`, committen und `request_git_push` per JSON-RPC aufrufen
(Muster `mcpcall2.sh` in `docs/m2-test.md`, Agent-Name `<session>/main`, im Projektordner ausführen). Entscheiden per `POST /v1/git/decide`. CI-Zustand setzt man mit
`PUT /_fake/state` (Läufe/Jobs/Logs, `head_sha` = gepushter Commit), Ergebnisse stehen in `GET /_fake/log` (Pull Requests, Reruns).
Danach alles abbauen (Session löschen, `fakegithub` beenden, `~/bergfried-e2e`, `/private/tmp/bft`, `~/.bergfried/repos`) und den Daemon normal neu starten.

## Manuell mit echtem GitHub (einmalig)
1. Test-Repo anlegen, fine-grained Token (nur dieses Repo; Actions R/W, Contents R/W, Pull requests R/W, Metadata R) → App → Einstellungen → GitHub.
2. Session mit dem Repo (Ordner-Import eines lokalen Checkouts), Agent committet auf `agent/test`, `request_git_push` → Prüfblatt → „Pushen“ mit PR.
3. Prüfen: Branch und PR auf GitHub, `git log` der Remote-Seite, Audit-Log. Ein Workflow, der absichtlich fehlschlägt: `ci_status`, `ci_logs` (Bestätigung), „Agent: CI fixen“.
**Wenn Push oder PR an Rechten scheitert**, die Rechte-Namen in `docs/DESIGN-GIT.md` und der App-Einstellung korrigieren.
