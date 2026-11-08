import SwiftUI

struct SettingsView: View {
    @EnvironmentObject var model: AppModel
    @Local private var paths = ""
    @Local private var names = ""
    @Local private var info: String?
    @Local private var daemonInstalled = DaemonController.installed

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
                    Text("Neue Sessions werden aus dieser VM geklont (Sekunden). Neu bauen gilt nur für neue Sessions.").font(.caption).foregroundStyle(.secondary)
                }
            }
            Section("Sperrliste (nie im Index, nie freigebbar)") {
                Text("Pfade relativ zum Home, eine pro Zeile. Die ersten vier Standardeinträge (.ssh, Library, .config, .mcp-einfach123-secrets) sind fest.").font(.caption).foregroundStyle(.secondary)
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
        .task { await load() }
    }

    func load() async {
        if let b: Blocklist = try? await model.api.get("/v1/blocklist") {
            paths = b.paths.joined(separator: "\n"); names = b.names.joined(separator: "\n")
        }
    }

    func save() async {
        let b = Blocklist(paths: paths.split(separator: "\n").map(String.init), names: names.split(separator: "\n").map(String.init))
        do { let _: Blocklist = try await model.api.send("PUT", "/v1/blocklist", b); info = "Sperrliste gespeichert, Index wird neu aufgebaut."; await load() }
        catch { info = error.localizedDescription }
    }
}
