# Test: Git und CI mit Session-Token

## Automatisch
`make test` (u. a. `ghtoken` mit lokalem GitHub-Ersatz, `ci`, `github`, `session` – Token nur über stdin –, `api` – Token nie in Antworten/Protokoll).

## Mechanismus lokal (ohne VM, ohne echtes GitHub)
Mit `tools/fakegithub` (REST + Git über HTTP, Basic-Auth):
```bash
make fakegithub
S=/private/tmp/bft; mkdir -p $S/repos/o $S/home
git init -q --bare $S/repos/o/r.git && git -C $S/repos/o/r.git config http.receivepack true
./bin/fakegithub -addr 127.0.0.1:8099 -root $S/repos -token github_pat_TEST0123456789abcdef &
# frisches HOME, damit weder ~/.gitconfig noch der Schlüsselbund berührt werden
export HOME=$S/home GIT_TERMINAL_PROMPT=0
printf 'http://x-access-token:github_pat_TEST0123456789abcdef@127.0.0.1:8099\n' > $HOME/.git-credentials && chmod 600 $HOME/.git-credentials
git config --global credential.helper store
git clone http://127.0.0.1:8099/o/r.git $S/work   # klappt; mit falschem Token: „Authentication failed“
```
Danach `fakegithub` beenden und `/private/tmp/bft` löschen. (Belegt am 2026-11-08: Clone und Push mit `store`-Helper, falsches Token scheitert.)

## Manuell mit echtem GitHub (einmalig, noch nicht gemacht)
1. Test-Repo anlegen (privat). Neue Session mit Git-Repo: Im Dialog „Token bei GitHub anlegen …“ – prüfen, dass das Formular vorausgefüllt ist (Name `bergfried-<session>`, Ablauf 1 Jahr,
   Rechte Contents/Pull requests/Actions/Metadata), „Only select repositories“ → nur das Test-Repo, Token einfügen, erstellen.
2. In der VM (`Shell in der VM`): `git -C ~/work/main push` auf einen Branch, `cat ~/.git-credentials` (0600), `ls -l`.
3. Weiteres Repo: im Agent `git clone https://github.com/<owner>/<anderes-repo>` → scheitert → Agent ruft `request_github_token` → Karte in „Anfragen“ → bei GitHub das Repo zum Token hinzufügen → „Zugriff prüfen“.
4. Workflow mit absichtlichem Fehler: `ci_status`, `ci_logs`, `ci_rerun` (ohne Rückfrage).
5. Rechte-Namen/Verhalten prüfen und `docs/DESIGN-GIT.md` korrigieren, falls etwas abweicht. Test-Token danach bei GitHub widerrufen.
