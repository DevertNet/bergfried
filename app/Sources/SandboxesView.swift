import SwiftUI
import AppKit

struct SandboxesView: View {
    @EnvironmentObject var model: AppModel
    @Local private var showCreate = false
    @Local private var addAgentFor: SandboxInfo?
    @Local private var deleteFor: SandboxInfo?
    @Local private var forceDeleteMsg: String?
    @Local private var forceDeleteFor: SandboxInfo?

    /// Sandboxes, die gerade erstellt werden und noch nicht in der Liste stehen (sofortiges Feedback).
    var creating: [(name: String, step: String)] {
        model.running.compactMap { k, v in
            guard k.hasPrefix("create-") else { return nil }
            let n = String(k.dropFirst("create-".count))
            return model.sandboxes.contains(where: { $0.name == n }) ? nil : (n, v)
        }.sorted { $0.name < $1.name }
    }

    var body: some View {
        VStack(spacing: 0) {
            if let s = model.status, !s.baseReady {
                Label("Basis-VM fehlt – sie wird beim ersten Erstellen gebaut (einige Minuten).", systemImage: "info.circle")
                    .padding(8).frame(maxWidth: .infinity, alignment: .leading).background(.yellow.opacity(0.15))
            }
            if model.sandboxes.isEmpty && creating.isEmpty {
                ContentUnavailableView("Keine Sandboxes", systemImage: "shield", description: Text("Lege mit „Neue Sandbox“ eine isolierte VM an."))
            } else {
                List {
                    ForEach(creating, id: \.name) { c in
                        HStack(spacing: 10) {
                            ProgressView().controlSize(.small)
                            Text(c.name).font(.headline)
                            Text(c.step).foregroundStyle(.secondary).lineLimit(1)
                            Spacer()
                        }.padding(.vertical, 10).frame(minHeight: 44).fixedSize(horizontal: false, vertical: true)
                    }
                    ForEach(model.sandboxes) { s in
                        SandboxRow(s: s, addAgent: { addAgentFor = s }, delete: { deleteFor = s })
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .transaction { $0.animation = nil }   // Zeilen sollen beim Erscheinen nicht animiert „einfahren“ (wirkte oben abgeschnitten)
            }
        }
        .toolbar { Button { showCreate = true } label: { Label("Neue Sandbox", systemImage: "plus") } }
        .sheet(isPresented: $showCreate) { CreateSandboxSheet() }
        .sheet(item: $addAgentFor) { s in AddAgentSheet(sandbox: s) }
        .confirmationDialog("Sandbox „\(deleteFor?.name ?? "")“ löschen?", isPresented: Binding(get: { deleteFor != nil }, set: { if !$0 { deleteFor = nil } })) {
            Button("Löschen", role: .destructive) {
                if let s = deleteFor { Task { await delete(s, force: false) } }
            }
        } message: { Text("Die VM und ihre Platte werden entfernt. Vorher wird auf ungepushte Commits geprüft.") }
        .alert("Ungesicherte Arbeit in der VM", isPresented: Binding(get: { forceDeleteMsg != nil }, set: { if !$0 { forceDeleteMsg = nil } })) {
            Button("Trotzdem löschen", role: .destructive) { if let s = forceDeleteFor { Task { await delete(s, force: true) } } }
            Button("Abbrechen", role: .cancel) {}
        } message: { Text(forceDeleteMsg ?? "") }
    }

    func delete(_ s: SandboxInfo, force: Bool) async {
        let err = await model.runJob("del-\(s.name)", "\(s.name) wird gelöscht …", "/v1/sandboxes/delete", NameBody(name: s.name, force: force))
        if let err {
            if err.contains("ungepushte") { forceDeleteFor = s; forceDeleteMsg = err } else { model.setBanner(err) }
        }
    }
}

struct SandboxRow: View {
    @EnvironmentObject var model: AppModel
    let s: SandboxInfo
    let addAgent: () -> Void
    let delete: () -> Void

    var busy: String? { model.running.first(where: { $0.key.hasSuffix(s.name) })?.value }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Circle().fill(s.running ? .green : .gray).frame(width: 10, height: 10)
                Text(s.name).font(.headline)
                Text(s.status).foregroundStyle(.secondary)
                if s.running { loginBadge }
                Spacer()
                if let b = busy { ProgressView().controlSize(.small); Text(b).font(.caption).foregroundStyle(.secondary) }
                else { actions }
            }
            HStack(spacing: 14) {
                Label("\(s.cpus) CPU", systemImage: "cpu")
                Label(formatBytes(s.memory) + " RAM", systemImage: "memorychip")
                diskLabel
                if s.activeGrants > 0 { Label("\(s.activeGrants) SSH-Freigabe(n)", systemImage: "key.fill").foregroundStyle(.orange) }
                if s.pendingRequests > 0 { Label("\(s.pendingRequests) offen", systemImage: "bell.fill").foregroundStyle(.red) }
                Text("Vorlage r\(s.templateVersion)")
            }.font(.caption).foregroundStyle(.secondary)
            if s.probeError != nil, s.running { Text("Status der VM aktuell nicht abrufbar (die VM startet möglicherweise noch).").font(.caption).foregroundStyle(.secondary) }
            if !s.agents.isEmpty {
                // Agents gehören zur Sandbox: eingerückt, mit Führungslinie und eigener Fläche.
                HStack(alignment: .top, spacing: 0) {
                    RoundedRectangle(cornerRadius: 1).fill(Color.accentColor.opacity(0.5)).frame(width: 3).padding(.leading, 4)
                    VStack(alignment: .leading, spacing: 0) {
                        Label("Agents (\(s.agents.count))", systemImage: "person.2.fill").font(.caption.bold()).foregroundStyle(.secondary).padding(.bottom, 4)
                        ForEach(Array(s.agents.enumerated()), id: \.element.name) { i, a in
                            if i > 0 { Divider() }
                            AgentRow(s: s, a: a)
                        }
                    }
                    .padding(.horizontal, 12).padding(.vertical, 8)
                    .background(Color.secondary.opacity(0.07), in: RoundedRectangle(cornerRadius: 8))
                    .padding(.leading, 10)
                }
                .padding(.leading, 22).padding(.top, 4)
            }
        }.padding(.vertical, 4)
    }

    @ViewBuilder var loginBadge: some View {
        switch s.claudeLoggedIn {
        case .some(true):
            Label("Claude eingeloggt", systemImage: "checkmark.seal.fill").font(.caption).foregroundStyle(.green)
        case .some(false):
            Button { report(Terminal.login(vm: s.vm)) } label: { Label("Claude nicht eingeloggt – jetzt einloggen", systemImage: "person.crop.circle.badge.exclamationmark") }
                .buttonStyle(.borderedProminent).tint(.orange).controlSize(.small)
        default:
            Label("Claude-Status wird geprüft …", systemImage: "hourglass").font(.caption).foregroundStyle(.secondary)
        }
    }

    @ViewBuilder var diskLabel: some View {
        let host = formatBytes(s.hostDiskUsed)
        if s.guestDiskSize > 0 {
            Label("Platte: \(host) belegt (in der VM \(formatBytes(s.guestDiskUsed)) von \(formatBytes(s.guestDiskSize)))", systemImage: "internaldrive")
                .help("Auf dem Mac belegt die VM-Platte \(host) (sparse, maximal \(formatBytes(s.disk))). Innerhalb der VM sind \(formatBytes(s.guestDiskUsed)) belegt.")
        } else {
            Label("Platte: \(host) belegt (max. \(formatBytes(s.disk)))", systemImage: "internaldrive")
        }
    }

    @ViewBuilder var actions: some View {
        if s.running {
            Button("Stoppen") { Task { report(await model.runJob("stop-\(s.name)", "Stoppt …", "/v1/sandboxes/stop", NameBody(name: s.name))) } }
            Button("Agent +", action: addAgent)
            Menu {
                Button("Shell in der VM (\(Terminal.appName))") { report(Terminal.shell(vm: s.vm)) }
                Button("SSH-Befehl kopieren") {
                    if let c = Terminal.sshCommand(vm: s.vm) { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(c, forType: .string) }
                }
                Divider()
                Button("Claude-Login (einmal pro VM)") { report(Terminal.login(vm: s.vm)) }
            } label: { Label("Shell / SSH", systemImage: "terminal") }
        } else {
            Button("Starten") { Task { report(await model.runJob("start-\(s.name)", "Startet …", "/v1/sandboxes/start", NameBody(name: s.name))) } }
        }
        Button(role: .destructive, action: delete) { Image(systemName: "trash") }
    }

    func report(_ e: String?) { if let e { model.setBanner(e) } }
}

struct AgentRow: View {
    @EnvironmentObject var model: AppModel
    let s: SandboxInfo
    let a: AgentInfo
    @Local private var removeOpen = false
    @Local private var forceMsg: String?

    var state: (color: Color, text: String) {
        if a.rc { return (.green, "Remote Control aktiv") }
        if !a.window && sandboxBusy { return (.orange, "wird gestartet …") }
        if s.claudeLoggedIn == false { return (.orange, "wartet auf Claude-Login") }
        if a.rcAction == "failed" { return (.red, "RC nicht aktivierbar – bitte neu starten") }
        if a.rcAction == "activating" { return (.orange, "aktiviere Remote Control …") }
        if a.rcAction == "waiting" { return (.orange, "Claude wartet vermutlich auf eine Eingabe – tmux öffnen") }
        if !a.window { return (.gray, "kein tmux-Fenster") }
        return (.orange, "Remote Control startet …")
    }

    var busy: String? { model.running.first(where: { $0.key.hasSuffix("-\(s.name)-\(a.name)") })?.value }
    /// Für die Sandbox läuft gerade ein Job (Erstellen, Starten, Agent hinzufügen …).
    var sandboxBusy: Bool { model.running.keys.contains { $0.hasSuffix(s.name) } }

    var body: some View {
        HStack(spacing: 10) {
            Circle().fill(state.color).frame(width: 9, height: 9)
            VStack(alignment: .leading, spacing: 1) {
                Text(a.name).font(.callout.bold())
                Text(state.text).font(.caption).foregroundStyle(.secondary)
            }
            Text(a.worktree).font(.caption.monospaced()).foregroundStyle(.tertiary).lineLimit(1).truncationMode(.middle)
            Spacer()
            if let b = busy {
                ProgressView().controlSize(.small); Text(b).font(.caption).foregroundStyle(.secondary)
            } else {
                if a.rc, let url = URL(string: a.rcUrl), !a.rcUrl.isEmpty {
                    Button { NSWorkspace.shared.open(url) } label: { Label("RC-Link öffnen", systemImage: "arrow.up.right.square") }.controlSize(.small)
                }
                Button { if let e = Terminal.attach(vm: s.vm, agent: a.name) { model.setBanner(e) } } label: { Label("\(Terminal.appName): Claude-Fenster", systemImage: "terminal") }
                    .controlSize(.small).disabled(!s.running || !a.window)
                    .help("Öffnet das tmux-Fenster dieses Agents in \(Terminal.appName)")
                Menu {
                    if a.rc, !a.rcUrl.isEmpty {
                        Button("Link kopieren") { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(a.rcUrl, forType: .string) }
                        Divider()
                    }
                    if !a.rc {
                        Button("Remote Control läuft – als aktiv markieren") {
                            Task {
                                do { try await model.api.post("/v1/agents/rc-ok", AgentBody(sandbox: s.name, agent: a.name)); await model.refresh() }
                                catch { model.setBanner(error.localizedDescription) }
                            }
                        }.disabled(!s.running || !a.window)
                    }
                    Button("Neu starten") {
                        Task { if let e = await model.runJob("restart-\(s.name)-\(a.name)", "startet neu …", "/v1/agents/restart", AgentBody(sandbox: s.name, agent: a.name)) { model.setBanner(e) } }
                    }.disabled(!s.running)

                    Divider()
                    Button("Entfernen …", role: .destructive) { removeOpen = true }.disabled(!s.running)
                } label: { Image(systemName: "ellipsis.circle") }
                .menuStyle(.borderlessButton).fixedSize()
            }
        }
        .padding(.vertical, 5)
        .sheet(isPresented: $removeOpen) { RemoveAgentSheet(s: s, a: a, onForce: { msg in forceMsg = msg }) }
        .alert("Ungesicherte Arbeit im Agent-Verzeichnis", isPresented: Binding(get: { forceMsg != nil }, set: { if !$0 { forceMsg = nil } })) {
            Button("Trotzdem löschen", role: .destructive) { Task { await remove(deleteDir: true, force: true) } }
            Button("Abbrechen", role: .cancel) {}
        } message: { Text(forceMsg ?? "") }
    }

    func remove(deleteDir: Bool, force: Bool) async {
        let err = await model.runJob("agentrm-\(s.name)-\(a.name)", "wird entfernt …", "/v1/agents/remove", AgentRemoveBody(sandbox: s.name, agent: a.name, deleteDir: deleteDir, force: force))
        if let err { model.setBanner(err) }
    }
}

struct RemoveAgentSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    let s: SandboxInfo
    let a: AgentInfo
    let onForce: (String) -> Void
    @Local private var deleteDir = false

    var body: some View {
        Form {
            Text("Agent „\(a.name)“ entfernen").font(.headline)
            Text("Das tmux-Fenster mit Claude (und seine Remote-Control-Session) wird beendet.").font(.callout)
            Picker("", selection: $deleteDir) {
                Text("Nur den Agent beenden – das Verzeichnis \(a.worktree) bleibt erhalten").tag(false)
                if a.name != "main" { Text("Agent beenden und sein Verzeichnis löschen (Git-Worktree samt Branch agent/\(a.name), Docker-Stack \(s.name)-\(a.name))").tag(true) }
            }.pickerStyle(.radioGroup).labelsHidden()
            if a.name == "main" { Text("Das Hauptverzeichnis „main“ wird nie gelöscht.").font(.caption).foregroundStyle(.secondary) }
            if deleteDir { Text("Vorher wird auf ungepushte Commits und ungesicherte Änderungen geprüft.").font(.caption).foregroundStyle(.secondary) }
            HStack {
                Spacer()
                Button("Abbrechen") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Entfernen", role: .destructive) {
                    let dd = deleteDir
                    dismiss()
                    Task {
                        let err = await model.runJob("agentrm-\(s.name)-\(a.name)", "wird entfernt …", "/v1/agents/remove", AgentRemoveBody(sandbox: s.name, agent: a.name, deleteDir: dd, force: false))
                        if let err { if err.contains("ungesicherte Arbeit") { onForce(err) } else { model.setBanner(err) } }
                    }
                }.keyboardShortcut(.defaultAction)
            }
        }.padding(20).frame(width: 520)
    }
}

struct CreateSandboxSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    @Local private var name = ""
    @Local private var source = 0            // 0 leer, 1 Git-Repo, 2 Ordner
    @Local private var repo = ""
    @Local private var token = ""
    @Local private var link = ""
    @Local private var hint = ""
    @Local private var folder = ""
    @Local private var preview: ImportPreview?
    @Local private var previewError: String?
    var valid: Bool { name.range(of: #"^[a-z][a-z0-9-]{0,23}$"#, options: .regularExpression) != nil && name != "base" && !name.hasPrefix("base-") }
    var sourceOK: Bool {
        switch source {
        case 1: return !repo.trimmingCharacters(in: .whitespaces).isEmpty && !token.trimmingCharacters(in: .whitespaces).isEmpty
        case 2: return preview != nil && previewError == nil
        default: return true
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Neue Sandbox").font(.title3.bold())
            VStack(alignment: .leading, spacing: 4) {
                Text("Name").font(.callout.bold())
                TextField("z. B. shop", text: $name).textFieldStyle(.roundedBorder)
                Text("a–z, 0–9 und -, höchstens 24 Zeichen").font(.caption).foregroundStyle(.secondary)
            }
            VStack(alignment: .leading, spacing: 6) {
                Text("Code-Quelle").font(.callout.bold())
                Picker("Code-Quelle", selection: $source) {
                    Text("Leer").tag(0)
                    Text("Git-Repo").tag(1)
                    Text("Ordner").tag(2)
                }.pickerStyle(.segmented).labelsHidden()
            }
            if source == 1 {
                GroupBox {
                    VStack(alignment: .leading, spacing: 10) {
                        TextField("https://github.com/owner/repo", text: $repo).textFieldStyle(.roundedBorder)
                        Divider()
                        GitHubTokenSteps(link: link, hint: hint, token: $token)
                    }.frame(maxWidth: .infinity, alignment: .leading).padding(4)
                }
            }
            if source == 2 {
                GroupBox {
                    VStack(alignment: .leading, spacing: 8) {
                        HStack {
                            Text(folder.isEmpty ? "Kein Ordner gewählt" : folder).lineLimit(1).truncationMode(.middle).foregroundStyle(folder.isEmpty ? .secondary : .primary)
                            Spacer()
                            Button("Ordner wählen …") { chooseFolder() }
                        }
                        if let p = preview { Label("\(p.files) Dateien, \(formatBytes(p.bytes)) werden kopiert", systemImage: "doc.on.doc").font(.callout) }
                        if let e = previewError { Label(e, systemImage: "xmark.octagon.fill").foregroundStyle(.red).font(.callout).fixedSize(horizontal: false, vertical: true) }
                        Text("Der Ordner wird **kopiert**, nicht eingebunden: Die VM sieht nie dein Home. Nicht kopiert werden Einträge der Sperrliste, .env-Dateien, Schlüssel, Symlinks sowie node_modules und Caches. Zugangsdaten in .git/config werden entfernt. Für Git braucht der Agent später ein GitHub-Token (er fragt danach).")
                            .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    }.frame(maxWidth: .infinity, alignment: .leading).padding(4)
                }
            }
            Text("Die VM-Platte liegt unter ~/.lima/bergfried-<name> (Code in der VM unter ~/work/main), die Austausch-Ordner unter ~/.bergfried/sandboxes/<name>/shared/{in,out}.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button("Abbrechen") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Erstellen") {
                    let n = name, r = source == 1 ? repo : "", f = source == 2 ? folder : ""
                    dismiss()
                    Task { if let e = await model.runJob("create-\(n)", "\(n) wird erstellt …", "/v1/sandboxes/create", NameBody(name: n, repo: r, importPath: f, githubToken: source == 1 ? token : "")) { model.setBanner(e) } }
                }.disabled(!valid || !sourceOK).keyboardShortcut(.defaultAction)
            }
        }.padding(20).frame(width: 560).fixedSize(horizontal: false, vertical: true)
        .task(id: name) {
            // Link mit dem Sandbox-Namen vorbelegen (Token heißt dann bergfried-<name>)
            let q = valid ? name : "neu"
            if let l: GitHubLink = try? await model.api.get("/v1/github/link?sandbox=\(q)") { link = l.url; hint = l.hint }
        }
    }

    func chooseFolder() {
        let p = NSOpenPanel()
        p.canChooseDirectories = true; p.canChooseFiles = false; p.allowsMultipleSelection = false
        p.directoryURL = URL(fileURLWithPath: NSHomeDirectory())
        p.prompt = "Auswählen"
        p.message = "Ordner im Home-Verzeichnis, der in die VM kopiert werden soll"
        guard p.runModal() == .OK, let url = p.url else { return }
        folder = url.path; preview = nil; previewError = nil
        Task {
            do { preview = try await model.api.send("POST", "/v1/import/preview", PathBody(path: url.path), as: ImportPreview.self) }
            catch { previewError = error.localizedDescription }
        }
    }
}

struct AddAgentSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    let sandbox: SandboxInfo
    @Local private var name = ""
    var valid: Bool { name.range(of: #"^[a-z][a-z0-9-]{0,30}$"#, options: .regularExpression) != nil }

    var body: some View {
        Form {
            Text("Neuer Agent in „\(sandbox.name)“").font(.headline)
            TextField("Agent-Name", text: $name)
            Text("Jeder Agent bekommt ein tmux-Fenster, eine Remote-Control-Session und ein Git-Worktree.").font(.caption).foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button("Abbrechen") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Hinzufügen") {
                    let n = name, s = sandbox.name
                    dismiss()
                    Task { if let e = await model.runJob("agent-\(s)", "Agent \(n) startet …", "/v1/agents/add", AgentBody(sandbox: s, agent: n)) { model.setBanner(e) } }
                }.disabled(!valid).keyboardShortcut(.defaultAction)
            }
        }.padding(20).frame(width: 440)
    }
}
