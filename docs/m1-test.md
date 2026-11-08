# M1 – manueller Test

Voraussetzungen: Docker läuft, `brew install lima`. Go wird nicht lokal gebraucht.

```bash
make test build            # Go-Tests und bin/bergfriedd (macOS arm64), alles in Docker
export BERGFRIED_TEMPLATE=vm/bergfried.yaml
./bin/bergfriedd base build   # einmalig, einige Minuten (Image, apt, Docker, Claude Code)
```

## Ablauf

1. `./bin/bergfriedd create shop` – klont die Basis-VM (Sekunden), startet sie, erneuert Maschinen-ID und SSH-Host-Keys.
   Mit Repo: `--repo https://github.com/<user>/<repo>.git` (landet in `~/work/main`).
2. `./bin/bergfriedd login shop` – **einmal pro VM**: `claude auth login` (claude.ai-Abo). Remote Control geht nicht mit `setup-token`.
3. `./bin/bergfriedd agent add shop main` – tmux-Fenster, Worktree, `COMPOSE_PROJECT_NAME=shop-main`, `claude --remote-control 'shop/main'`.
4. In Claude-App (Handy) oder claude.ai/code die Session `shop/main` öffnen und etwas schreiben.
5. `./bin/bergfriedd stop shop`, danach `start shop`: Agents starten per `claude --continue` neu, der Verlauf ist da.
6. `./bin/bergfriedd delete shop` – warnt bei ungepushten Commits (`--force` überspringt).
7. `./bin/bergfriedd list`, `./bin/bergfriedd audit` – Status und Protokoll.

## Prüfpunkte

- In der VM: `mount | grep shared` zeigt genau zwei virtiofs-Mounts, `in` mit `ro`; `ls /Users` zeigt nichts von deinem Home.
- `docker ps` in der VM funktioniert; auf dem Mac ist kein VM-Docker-Socket weitergeleitet.
- Zwei Sessions haben verschiedene `/etc/machine-id`.

## Bekannte Punkte

- Der erste Start von Claude in einer frisch geklonten VM zeigt das Onboarding (Theme), bis `login` gelaufen ist.
- Der VM-User hat passwortloses `sudo` (Lima-Standard). Die Grenze ist die VM, nicht der Linux-User.
- Lima pinnt den SSH-Host-Key beim ersten Start; `create` löscht die Pin-Datei nach der Neuerzeugung der Keys.
