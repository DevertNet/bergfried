# Design-Ergänzungen

## Template-Vorschläge durch den Agent (`propose_template_change`)

Der Agent kann Änderungen an der VM-Vorlage nur **vorschlagen**, nie selbst anwenden.

- MCP-Tool `propose_template_change(summary, reason, patch)`: `patch` ist ein unified diff gegen die aktuelle `bergfried.yaml` (Version steht in der Anfrage).
- Dritte Anfrage-Art neben Datei und SSH, mit Sandbox- und Agent-Name. Landet im Anfragen-Bereich der App/CLI und im Audit-Log.
- Der Daemon validiert den Vorschlag **vor** der Anzeige: Patch lässt sich anwenden, `limactl validate` ist OK, und Sicherheitsregeln greifen. Mounts, `forwardAgent`, Socket-/Port-Forwards und Home-Pfade in einem Patch lösen eine deutliche Warnung aus. Vorschläge, die Home mounten, lehnt der Daemon automatisch ab.
- Anzeige für mich: Diff, Begründung und Warnungen. Antworten: **ablehnen**, **nur diese Sandbox** (Overlay, gilt bei Neuaufbau) oder **in die Vorlage übernehmen** (neue Versionsnummer, Vorlage im Repo/Datenordner).
- Angewendet wird nie in einer laufenden VM, sondern bei Rebuild bzw. neuen Sandboxes. Ein Rebuild erfolgt erst nach einer weiteren Bestätigung, weil die Platte dabei verloren gehen kann.
- Kleine Zusatzpakete (apt/npm) laufen im Alltag über Docker im Projekt. Das Template ist nur für VM-weite Änderungen gedacht.
