# M3 – manueller Test (SSH-Broker)

```bash
make build
./bin/bergfriedd base build          # Vorlage v3 (zweiter Reverse-Socket + ssh_config.d)
./bin/bergfriedd serve               # nutzt deinen SSH_AUTH_SOCK als Upstream (--upstream-agent <socket>)
./bin/bergfriedd create shop
./bin/bergfriedd login shop && ./bin/bergfriedd agent add shop main
```

Im Agent: „Ich brauche SSH-Zugriff auf `server.example.com`, um X zu prüfen“ → Tool `request_ssh`.

1. Es erscheinen nacheinander: Freigabe-Dialog (mit Host-Key-Warnung, falls der Host nicht in deiner `known_hosts` steht),
   Dauer, **Key**, Geltung (Agent/alle Agents), Modus (still im Zeitfenster / jede Verbindung einzeln).
2. Ohne Dialog: `./bin/bergfriedd serve --no-dialog`, dann
   `./bin/bergfriedd requests list` und
   `./bin/bergfriedd ssh allow <id> --minutes 30 [--key SHA256:…] [--user claude] [--all-agents] [--per-connection] [--trust-host-key]`.
3. `./bin/bergfriedd ssh keys` (Keys deines Agents), `ssh grants [--all]`, `ssh extend <grant> --minutes 60`, `ssh revoke <grant>`.

## Prüfpunkte

- Ohne Freigabe: in der VM `ssh-add -l` mit `SSH_AUTH_SOCK=/run/user/$(id -u)/bergfried-agent.sock` zeigt keine Keys, `ssh host` scheitert.
- Während der Freigabe: `ssh <user>@<host>` geht, nur mit dem gewählten Key und nur zum freigegebenen Host (Host-Key wird geprüft).
- Anderer Host mit derselben Freigabe → abgelehnt (`ssh.sign_denied`, Grund im Protokoll).
- Nach Ablauf oder `revoke`: neue Verbindungen scheitern, laufende `ssh`-Prozesse in der VM werden beendet.
- `./bin/bergfriedd audit`: Anfrage, Freigabe, jede Signatur, Ablauf.

## Hinweise

- **Hoster-Seite:** Während einer Freigabe könnte ein Agent einen eigenen Key in `authorized_keys` des Ziels eintragen.
  Für wichtige Server einen eingeschränkten User `claude` verwenden, dessen `authorized_keys` root gehört
  (`AuthorizedKeysFile /etc/ssh/authorized_keys/%u` in `sshd_config`).
- **Agent-Geltung:** „Nur dieser Agent“ dient Anzeige und Protokoll; technisch gilt die Freigabe für die Session
  (gleicher Linux-User in der VM).
- Das Broker-Verhalten (kein session-bind → ablehnen, Weiterleitung → ablehnen, kein Schreiben in den Agent) ist durch
  Tests in `daemon/internal/broker` abgesichert.
- Die Vorlage nutzt `IdentityAgent /run/user/%i/bergfried-agent.sock` (systemweit) – ein vorhandener `SSH_AUTH_SOCK` der VM wird nicht benutzt.
