import SwiftUI

struct SettingsView: View {
    @EnvironmentObject var model: AppModel
    @Local private var paths = ""
    @Local private var names = ""
    @Local private var info: String?
    @Local private var daemonInstalled = DaemonController.installed
    @Local private var termChoice = Terminal.choice
    @Local private var notifStatus = "wird geprüft …"
    @Local private var tpl: TemplateInfo?
    @Local private var tplText = ""
    @Local private var tplMsg: String?
    @Local private var tplErr: String?

    var body: some View {
        Form {
            Section("Daemon") {
                LabeledContent("Status") {
                    HStack { Circle().fill(model.online ? .green : .red).frame(width: 10, height: 10); Text(model.online ? "läuft (v\(model.status?.version ?? "?"))" : "nicht erreichbar") }
                }
                if let s = model.status {
                    LabeledContent("Index") { Text(s.indexReady ? "bereit" : "wird aufgebaut …") }
                    LabeledContent("SSH-Upstream-Agent") { Text(s.upstreamAgent ? "gefunden" : "fehlt (SSH-Freigaben gehen nicht)") }
                }
                HStack {
                    Button(daemonInstalled ? "Daemon neu starten" : "Daemon installieren und starten") {
                        do { info = try DaemonController.install(); daemonInstalled = true } catch { info = error.localizedDescription }
                    }
                    if daemonInstalled { Button("Daemon entfernen", role: .destructive) { info = DaemonController.uninstall(); daemonInstalled = false } }
                }
                Text("Der Daemon läuft als LaunchAgent (Start beim Login). Log: ~/.bergfried/daemon.log").font(.caption).foregroundStyle(.secondary)
                if let info { Text(info).font(.caption) }
            }
            Section("Basis-VM") {
                if let s = model.status {
                    LabeledContent("Vorlage") { Text("v\(s.templateVersion) – \(s.baseReady ? "gebaut" : "noch nicht gebaut")") }
                    HStack {
                        Button(s.baseReady ? "Neu bauen" : "Jetzt bauen") {
                            Task { if let e = await model.runJob("base", "Basis-VM wird gebaut (mehrere Minuten) …", "/v1/base/build" + (s.baseReady ? "?rebuild=1" : ""), NameBody(name: "x")) { model.setBanner(e) } }
                        }.disabled(model.running["base"] != nil)
                        if let l = model.running["base"] { ProgressView().controlSize(.small); Text(l).font(.caption) }
                    }
                    Text("Neue Sandboxes werden aus dieser VM geklont (Sekunden). Neu bauen gilt nur für neue Sandboxes.").font(.caption).foregroundStyle(.secondary)
                }
            }
            Section("Benachrichtigungen bei Anfragen") {
                LabeledContent("macOS-Mitteilungen") { Text(notifStatus).textSelection(.enabled) }
                Text("Letzte Berechtigungsanfrage: \(Notifier.shared.lastAuth)").font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                HStack {
                    Button("Testmitteilung senden") { Notifier.shared.postTest() }
                    Button("Mitteilungs-Einstellungen öffnen") {
                        if let u = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension") { NSWorkspace.shared.open(u) }
                    }
                    Button("Status aktualisieren") { Task { notifStatus = await Notifier.shared.status() } }
                }
                Text("Erscheinen keine Banner: In den Systemeinstellungen → Mitteilungen → Bergfried den Mitteilungsstil auf „Banner“ oder „Hinweise“ stellen und Mitteilungen erlauben.").font(.caption).foregroundStyle(.secondary)
                Text("Zusätzlich springt das Dock-Symbol, und das Menüleisten-Symbol sowie das Dock-Badge zeigen die Zahl offener Anfragen.").font(.caption).foregroundStyle(.secondary)
            }
            Section("GitHub") {
                Text("Das GitHub-Token gehört zur Sandbox: Es wird beim Anlegen einer Sandbox mit Git-Repo abgefragt oder auf Anfrage des Agents (Reiter „Anfragen“) eingerichtet und liegt in der VM. Es gibt hier keine globale Einstellung.").font(.callout).foregroundStyle(.secondary)
            }
            Section("Terminal") {
                Picker("Terminal-App für „Terminal“ und „Login“", selection: $termChoice) {
                    Text("Automatisch (iTerm, falls installiert)").tag("auto")
                    Text("iTerm").tag("iterm")
                    Text("Terminal").tag("terminal")
                }.onChange(of: termChoice) { _, v in UserDefaults.standard.set(v, forKey: Terminal.key) }
                Text("Aktuell wird \(Terminal.appName) verwendet\(Terminal.iTermInstalled ? "" : " (iTerm ist nicht installiert)").").font(.caption).foregroundStyle(.secondary)
            }
            Section("VM-Vorlage (bergfried.yaml)") {
                if let t = tpl {
                    LabeledContent("Basis") { Text("\(t.baseName) – \(t.baseReady ? "gebaut" : "noch nicht gebaut")") }
                    LabeledContent("Änderungen") { Text("Revision \(t.revision)") }
                }
                TextEditor(text: $tplText).font(.body.monospaced()).frame(height: 260).border(.secondary.opacity(0.4))
                Text("Gespeichert wird nur, was `limactl validate` besteht und die Isolation wahrt: keine Mounts (Home!), kein forwardAgent, kein Docker-Socket, die beiden Bergfried-Sockets bleiben. Nach dem Speichern wird eine neue Basis-VM fällig (oben „Jetzt bauen“); bestehende Sandboxes bleiben unverändert.").font(.caption).foregroundStyle(.secondary)
                if let tplErr { Label(tplErr, systemImage: "xmark.octagon.fill").foregroundStyle(.red).font(.callout).textSelection(.enabled) }
                if let t = tpl { ForEach(t.warnings, id: \.self) { Label($0, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.orange).font(.callout) } }
                if let tplMsg { Text(tplMsg).font(.callout) }
                HStack {
                    Button("Prüfen und speichern") { Task { await saveTemplate() } }.disabled(tpl == nil || tplText == tpl?.yaml)
                    Button("Änderungen verwerfen") { tplText = tpl?.yaml ?? ""; tplErr = nil; tplMsg = nil }.disabled(tplText == tpl?.yaml)
                    Button("Auf mitgelieferte Vorlage zurücksetzen", role: .destructive) { Task { await resetTemplate() } }
                }
            }
            Section("Wo liegen Sandboxes?") {
                Text("VM-Platte (Code, Docker-Daten): ~/.lima/bergfried-<name>\nAustausch-Ordner: ~/.bergfried/sandboxes/<name>/shared/in (read-only in der VM) und shared/out\nCode in der VM: ~/work/main (Git-Repo oder importierter Ordner), weitere Agents als Worktrees ~/work/<agent>\nDaten des Daemons (DB, Sperrliste, Vorlage): ~/.bergfried").font(.callout).textSelection(.enabled)
            }
            Section("Sperrliste (nie im Index, nie freigebbar)") {
                Text("Pfade relativ zum Home, eine pro Zeile. Die ersten drei Standardeinträge (.ssh, Library, .config) sind fest.").font(.caption).foregroundStyle(.secondary)
                TextEditor(text: $paths).font(.body.monospaced()).frame(height: 120).border(.secondary.opacity(0.4))
                Text("Namensmuster (Glob auf Dateinamen, z. B. .env.*)").font(.caption).foregroundStyle(.secondary)
                TextEditor(text: $names).font(.body.monospaced()).frame(height: 70).border(.secondary.opacity(0.4))
                HStack {
                    Button("Speichern") { Task { await save() } }
                    Button("Neu laden") { Task { await load() } }
                }
            }
            Section("Hinweis zu SSH-Freigaben") {
                Text("Während einer Freigabe könnte ein Agent einen eigenen Key in authorized_keys des Ziels eintragen. Für wichtige Server einen eingeschränkten User „claude“ nutzen, dessen authorized_keys root gehört (AuthorizedKeysFile in sshd_config).").font(.callout)
            }
        }
        .formStyle(.grouped)
        .task { await load(); await loadTemplate(); notifStatus = await Notifier.shared.status() }
    }

    func load() async {
        if let b: Blocklist = try? await model.api.get("/v1/blocklist") {
            paths = b.paths.joined(separator: "\n"); names = b.names.joined(separator: "\n")
        }
    }

    func loadTemplate() async {
        if let t: TemplateInfo = try? await model.api.get("/v1/template") { tpl = t; tplText = t.yaml }
    }

    func saveTemplate() async {
        tplErr = nil; tplMsg = nil
        do {
            let t: TemplateInfo = try await model.api.send("PUT", "/v1/template", YAMLBody(yaml: tplText))
            tpl = t; tplText = t.yaml
            tplMsg = t.baseReady ? "Gespeichert." : "Gespeichert. Die Basis-VM muss neu gebaut werden (Abschnitt „Basis-VM“)."
            await model.refresh()
        } catch { tplErr = error.localizedDescription }
    }

    func resetTemplate() async {
        tplErr = nil; tplMsg = nil
        do {
            let t: TemplateInfo = try await model.api.send("POST", "/v1/template/reset", Optional<String>.none)
            tpl = t; tplText = t.yaml; tplMsg = "Mitgelieferte Vorlage wiederhergestellt."
            await model.refresh()
        } catch { tplErr = error.localizedDescription }
    }

    func save() async {
        let b = Blocklist(paths: paths.split(separator: "\n").map(String.init), names: names.split(separator: "\n").map(String.init))
        do { let _: Blocklist = try await model.api.send("PUT", "/v1/blocklist", b); info = "Sperrliste gespeichert, Index wird neu aufgebaut."; await load() }
        catch { info = error.localizedDescription }
    }
}
