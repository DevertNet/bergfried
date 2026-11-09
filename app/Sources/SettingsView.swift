import SwiftUI

struct SettingsView: View {
    @EnvironmentObject var model: AppModel
    @Local private var paths = ""
    @Local private var names = ""
    @Local private var info: String?
    @Local private var daemonInstalled = DaemonController.installed
    @Local private var termChoice = Terminal.choice
    @Local private var notifStatus = L("checking …")
    @Local private var tpl: TemplateInfo?
    @Local private var tplText = ""
    @Local private var tplMsg: String?
    @Local private var tplErr: String?
    @Local private var upd: UpdateInfo?
    @Local private var updMsg: String?
    @Local private var confirmUpdate = false

    var body: some View {
        Form {
            Section("Daemon") {
                LabeledContent("Status") {
                    HStack { Circle().fill(model.online ? .green : .red).frame(width: 10, height: 10); Text(model.online ? L("running (v%@)", model.status?.version ?? "?") : L("not reachable")) }
                }
                if let s = model.status {
                    LabeledContent("Index") { Text(s.indexReady ? L("ready") : (s.indexWaiting == true ? L("waiting for your confirmation") : L("being built …"))) }
                    LabeledContent("SSH upstream agent") { Text(s.upstreamAgent ? L("found") : L("missing (SSH grants will not work)")) }
                }
                HStack {
                    Button(daemonInstalled ? "Restart daemon" : "Install and start daemon") { Task { await installDaemon() } }
                        .disabled(model.running["daemon"] != nil)
                    if daemonInstalled { Button("Remove daemon", role: .destructive) { Task { await removeDaemon() } }.disabled(model.running["daemon"] != nil) }
                    if let l = model.running["daemon"] { ProgressView().controlSize(.small); Text(l).font(.caption) }
                }
                Text("The daemon runs as a LaunchAgent (starts at login). Log: ~/.bergfried/daemon.log").font(.caption).foregroundStyle(.secondary)
                if let info { Text(info).font(.caption) }
            }
            Section("Updates") {
                LabeledContent("Version") { Text(model.status?.version ?? "?") }
                Picker("Update channel", selection: Binding(get: { upd?.channel ?? "stable" }, set: { v in Task { await setChannel(v) } })) {
                    Text("Stable").tag("stable")
                    Text("Dev (builds from the development branch)").tag("dev")
                }
                if upd?.channel == "dev" {
                    Text("Dev builds come from the development branch, may be broken and are not reviewed like stable releases. Switching back to stable only takes effect with the next stable release.").font(.caption).foregroundStyle(.orange)
                }
                if let u = upd {
                    if u.available {
                        LabeledContent("Available") { Text(u.latest) }
                        if !u.notes.isEmpty {
                            Text("Release notes").font(.caption.bold())
                            ScrollView { Text(u.notes).font(.caption).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading) }.frame(maxHeight: 120)
                        }
                    } else if u.error == nil {
                        Text("You are up to date.").font(.callout).foregroundStyle(.secondary)
                    }
                    if let e = u.error { Text(L("Last check failed: %@", e)).font(.caption).foregroundStyle(.orange).textSelection(.enabled) }
                    if !u.updatable, let r = u.reason { Text(L("Updates are not available in this setup: %@", r)).font(.caption).foregroundStyle(.secondary) }
                }
                HStack {
                    Button("Check now") { Task { await checkUpdate() } }
                    if let u = upd, u.available, u.updatable {
                        Button(L("Install version %@ …", u.latest)) { confirmUpdate = true }.buttonStyle(.borderedProminent).disabled(model.running["update"] != nil)
                    }
                    if let l = model.running["update"] { ProgressView().controlSize(.small); Text(l).font(.caption) }
                }
                if let updMsg { Text(updMsg).font(.caption).textSelection(.enabled) }
                Text("Updates come from GitHub releases. Releases are not signed: the download is only checked against its checksum.").font(.caption).foregroundStyle(.secondary)
                Text("If an update breaks Bergfried, run this in a terminal to restore the previous version:").font(.caption).foregroundStyle(.secondary)
                Text(Bundle.main.bundlePath + "/Contents/Resources/bergfriedd update rollback").font(.caption.monospaced()).textSelection(.enabled)
            }
            Section("Base VM") {
                if let s = model.status {
                    LabeledContent("Template") { Text(L("v%lld – %@", s.templateVersion, s.baseReady ? L("built") : L("not built yet"))) }
                    HStack {
                        Button(s.baseReady ? "Rebuild" : "Build now") {
                            Task { if let e = await model.runJob("base", L("Building base VM (several minutes) …"), "/v1/base/build" + (s.baseReady ? "?rebuild=1" : ""), NameBody(name: "x")) { model.setBanner(e) } }
                        }.disabled(model.running["base"] != nil)
                        if let l = model.running["base"] { ProgressView().controlSize(.small); Text(l).font(.caption) }
                    }
                    Text("New sandboxes are cloned from this VM (seconds). Rebuilding only affects new sandboxes.").font(.caption).foregroundStyle(.secondary)
                }
            }
            Section("Notifications for requests") {
                LabeledContent("macOS notifications") { Text(notifStatus).textSelection(.enabled) }
                Text(L("Last permission request: %@", Notifier.shared.lastAuth)).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                HStack {
                    Button("Send test notification") { Notifier.shared.postTest() }
                    Button("Open notification settings") {
                        if let u = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension") { NSWorkspace.shared.open(u) }
                    }
                    Button("Refresh status") { Task { notifStatus = await Notifier.shared.status() } }
                }
                Text("If no banners appear: in System Settings → Notifications → Bergfried set the notification style to \"Banners\" or \"Alerts\" and allow notifications.").font(.caption).foregroundStyle(.secondary)
                Text("In addition the dock icon bounces, and the menu bar icon and the dock badge show the number of open requests.").font(.caption).foregroundStyle(.secondary)
            }
            Section("GitHub") {
                Text("The GitHub token belongs to the sandbox: it is asked for when creating a sandbox with a git repo, or set up on the agent's request (\"Requests\" tab), and it lives in the VM. There is no global setting here.").font(.callout).foregroundStyle(.secondary)
            }
            Section("Terminal") {
                Picker("Terminal app for \"Terminal\" and \"Login\"", selection: $termChoice) {
                    Text("Automatic (iTerm if installed)").tag("auto")
                    Text("iTerm").tag("iterm")
                    Text("Terminal").tag("terminal")
                }.onChange(of: termChoice) { _, v in UserDefaults.standard.set(v, forKey: Terminal.key) }
                Text(L("Currently using %@%@.", Terminal.appName, Terminal.iTermInstalled ? "" : L(" (iTerm is not installed)"))).font(.caption).foregroundStyle(.secondary)
            }
            Section("VM template (bergfried.yaml)") {
                if let t = tpl {
                    LabeledContent("Base") { Text(L("%@ – %@", t.baseName, t.baseReady ? L("built") : L("not built yet"))) }
                    LabeledContent("Changes") { Text(L("Revision %lld", t.revision)) }
                }
                TextEditor(text: $tplText).font(.body.monospaced()).frame(height: 260).border(.secondary.opacity(0.4))
                Text("Only what passes `limactl validate` and keeps the isolation is saved: no mounts (home!), no forwardAgent, no Docker socket, both Bergfried sockets stay. After saving a new base VM is due (\"Build now\" above); existing sandboxes stay unchanged.").font(.caption).foregroundStyle(.secondary)
                if let tplErr { Label(tplErr, systemImage: "xmark.octagon.fill").foregroundStyle(.red).font(.callout).textSelection(.enabled) }
                if let t = tpl { ForEach(t.warnings, id: \.self) { Label($0, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.orange).font(.callout) } }
                if let tplMsg { Text(tplMsg).font(.callout) }
                HStack {
                    Button("Check and save") { Task { await saveTemplate() } }.disabled(tpl == nil || tplText == tpl?.yaml)
                    Button("Discard changes") { tplText = tpl?.yaml ?? ""; tplErr = nil; tplMsg = nil }.disabled(tplText == tpl?.yaml)
                    Button("Reset to the bundled template", role: .destructive) { Task { await resetTemplate() } }
                }
            }
            Section("Where do sandboxes live?") {
                Text("VM disk (code, Docker data): ~/.lima/bergfried-<name>\nExchange folders: ~/.bergfried/sandboxes/<name>/shared/in (read-only in the VM) and shared/out\nCode in the VM: ~/work/main (git repo or imported folder), further agents as worktrees ~/work/<agent>\nDaemon data (DB, blocklist, template): ~/.bergfried").font(.callout).textSelection(.enabled)
            }
            Section("Blocklist (never indexed, never shareable)") {
                Text("Paths relative to home, one per line. The first three default entries (.ssh, Library, .config) are fixed.").font(.caption).foregroundStyle(.secondary)
                TextEditor(text: $paths).font(.body.monospaced()).frame(height: 120).border(.secondary.opacity(0.4))
                Text("Name patterns (glob on file names, e.g. .env.*)").font(.caption).foregroundStyle(.secondary)
                TextEditor(text: $names).font(.body.monospaced()).frame(height: 70).border(.secondary.opacity(0.4))
                HStack {
                    Button("Save") { Task { await save() } }
                    Button("Reload") { Task { await load() } }
                }
            }
            Section("Note on SSH grants") {
                Text("During a grant an agent could add its own key to authorized_keys on the target. For important servers use a restricted user \"claude\" whose authorized_keys belongs to root (AuthorizedKeysFile in sshd_config).").font(.callout)
            }
        }
        .formStyle(.grouped)
        .confirmationDialog(L("Install version %@?", upd?.latest ?? ""), isPresented: $confirmUpdate) {
            Button("Install and restart", role: .destructive) { Task { await installUpdate() } }
        } message: { Text("Releases are not signed: the update is only checked against its checksum. Install only if you trust this repository and its maintainers. The daemon and the app restart; running sandboxes keep running.") }
        .task { await load(); await loadTemplate(); notifStatus = await Notifier.shared.status(); await checkUpdate(refresh: false) }
    }

    func checkUpdate(refresh: Bool = true) async {
        updMsg = nil
        do { upd = try await model.api.get("/v1/update" + (refresh ? "?refresh=1" : "")) } catch { updMsg = error.localizedDescription }
        await model.refresh()
    }

    func setChannel(_ v: String) async {
        updMsg = nil
        do { upd = try await model.api.send("PUT", "/v1/update/channel", ChannelBody(channel: v)) } catch { updMsg = error.localizedDescription }
        await model.refresh()
    }

    /// launchctl blocks for seconds: run it off the main thread (otherwise the beach ball appears) and show what happens.
    func installDaemon() async {
        info = nil
        model.running["daemon"] = L("Registering the daemon with macOS (launchctl) …")
        defer { model.running["daemon"] = nil }
        let result: Result<String, Error> = await Task.detached { Result { try DaemonController.install() } }.value
        switch result {
        case .failure(let e): info = e.localizedDescription; return
        case .success(let msg): info = msg; daemonInstalled = true
        }
        model.running["daemon"] = L("Waiting for the daemon to answer …")
        for _ in 0..<30 {
            await model.refresh()
            if model.online { info = L("Daemon installed and started.") ; return }
            try? await Task.sleep(nanoseconds: 1_000_000_000)
        }
        info = L("The daemon was started but does not answer yet. Check ~/.bergfried/daemon.log.")
    }

    func removeDaemon() async {
        info = nil
        model.running["daemon"] = L("Stopping and removing the daemon …")
        defer { model.running["daemon"] = nil }
        info = await Task.detached { DaemonController.uninstall() }.value
        daemonInstalled = false
    }

    func installUpdate() async {
        updMsg = nil
        let restartMsg = L("The update is being installed: the app and daemon restart now. If nothing happens within a minute, see ~/.bergfried/update/update.log.")
        if let e = await model.runJob("update", L("Starting the update …"), "/v1/update/install", NameBody(name: "update")) {
            // A lost connection after the "Installing:" step is expected: the update helper stops the daemon on purpose.
            updMsg = model.lastStep["update"]?.hasPrefix("Installing:") == true ? restartMsg : L("Update failed: %@", e)
        } else {
            updMsg = restartMsg
        }
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
            tplMsg = t.baseReady ? L("Saved.") : L("Saved. The base VM must be rebuilt (section \"Base VM\").")
            await model.refresh()
        } catch { tplErr = error.localizedDescription }
    }

    func resetTemplate() async {
        tplErr = nil; tplMsg = nil
        do {
            let t: TemplateInfo = try await model.api.send("POST", "/v1/template/reset", Optional<String>.none)
            tpl = t; tplText = t.yaml; tplMsg = L("Bundled template restored.")
            await model.refresh()
        } catch { tplErr = error.localizedDescription }
    }

    func save() async {
        let b = Blocklist(paths: paths.split(separator: "\n").map(String.init), names: names.split(separator: "\n").map(String.init))
        do { let _: Blocklist = try await model.api.send("PUT", "/v1/blocklist", b); info = L("Blocklist saved, the index is being rebuilt."); await load() }
        catch { info = error.localizedDescription }
    }
}
