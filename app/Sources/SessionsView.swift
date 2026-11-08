import SwiftUI
import AppKit

struct SessionsView: View {
    @EnvironmentObject var model: AppModel
    @Local private var showCreate = false
    @Local private var addAgentFor: SessionInfo?
    @Local private var deleteFor: SessionInfo?
    @Local private var forceDeleteMsg: String?
    @Local private var forceDeleteFor: SessionInfo?

    var body: some View {
        VStack(spacing: 0) {
            if let s = model.status, !s.baseReady {
                Label("Basis-VM fehlt – sie wird beim ersten Erstellen gebaut (einige Minuten).", systemImage: "info.circle")
                    .padding(8).frame(maxWidth: .infinity, alignment: .leading).background(.yellow.opacity(0.15))
            }
            if model.sessions.isEmpty {
                ContentUnavailableView("Keine Sessions", systemImage: "shield", description: Text("Lege mit „Neue Session“ eine isolierte VM an."))
            } else {
                List(model.sessions) { s in
                    SessionRow(s: s, addAgent: { addAgentFor = s }, delete: { deleteFor = s })
                }
            }
        }
        .toolbar { Button { showCreate = true } label: { Label("Neue Session", systemImage: "plus") } }
        .sheet(isPresented: $showCreate) { CreateSessionSheet() }
        .sheet(item: $addAgentFor) { s in AddAgentSheet(session: s) }
        .confirmationDialog("Session „\(deleteFor?.name ?? "")“ löschen?", isPresented: Binding(get: { deleteFor != nil }, set: { if !$0 { deleteFor = nil } })) {
            Button("Löschen", role: .destructive) {
                if let s = deleteFor { Task { await delete(s, force: false) } }
            }
        } message: { Text("Die VM und ihre Platte werden entfernt. Vorher wird auf ungepushte Commits geprüft.") }
        .alert("Ungesicherte Arbeit in der VM", isPresented: Binding(get: { forceDeleteMsg != nil }, set: { if !$0 { forceDeleteMsg = nil } })) {
            Button("Trotzdem löschen", role: .destructive) { if let s = forceDeleteFor { Task { await delete(s, force: true) } } }
            Button("Abbrechen", role: .cancel) {}
        } message: { Text(forceDeleteMsg ?? "") }
    }

    func delete(_ s: SessionInfo, force: Bool) async {
        let err = await model.runJob("del-\(s.name)", "\(s.name) wird gelöscht …", "/v1/sessions/delete", NameBody(name: s.name, force: force))
        if let err {
            if err.contains("ungepushte") { forceDeleteFor = s; forceDeleteMsg = err } else { model.setBanner(err) }
        }
    }
}

struct SessionRow: View {
    @EnvironmentObject var model: AppModel
    let s: SessionInfo
    let addAgent: () -> Void
    let delete: () -> Void

    var busy: String? { model.running.first(where: { $0.key.hasSuffix(s.name) })?.value }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Circle().fill(s.running ? .green : .gray).frame(width: 10, height: 10)
                Text(s.name).font(.headline)
                Text(s.status).foregroundStyle(.secondary)
                Spacer()
                if let b = busy { ProgressView().controlSize(.small); Text(b).font(.caption).foregroundStyle(.secondary) }
                else { actions }
            }
            HStack(spacing: 14) {
                Label("\(s.cpus) CPU", systemImage: "cpu")
                Label(formatBytes(s.memory) + " RAM", systemImage: "memorychip")
                Label(formatBytes(s.disk) + " Platte", systemImage: "internaldrive")
                if s.activeGrants > 0 { Label("\(s.activeGrants) SSH-Freigabe(n)", systemImage: "key.fill").foregroundStyle(.orange) }
                if s.pendingRequests > 0 { Label("\(s.pendingRequests) offen", systemImage: "bell.fill").foregroundStyle(.red) }
                Text("Vorlage v\(s.templateVersion)")
            }.font(.caption).foregroundStyle(.secondary)
            if !s.agents.isEmpty {
                HStack { ForEach(s.agents, id: \.name) { a in Text(a.name).font(.caption).padding(.horizontal, 8).padding(.vertical, 2).background(.blue.opacity(0.15), in: Capsule()) } }
            }
        }.padding(.vertical, 4)
    }

    @ViewBuilder var actions: some View {
        if s.running {
            Button("Stoppen") { Task { report(await model.runJob("stop-\(s.name)", "Stoppt …", "/v1/sessions/stop", NameBody(name: s.name))) } }
            Button("Agent +", action: addAgent)
            Menu {
                Button("Terminal (tmux)") { Terminal.attach(vm: s.vm) }
                Button("Claude-Login (einmal pro VM)") { Terminal.login(vm: s.vm) }
            } label: { Label("Terminal", systemImage: "terminal") }
        } else {
            Button("Starten") { Task { report(await model.runJob("start-\(s.name)", "Startet …", "/v1/sessions/start", NameBody(name: s.name))) } }
        }
        Button(role: .destructive, action: delete) { Image(systemName: "trash") }
    }

    func report(_ e: String?) { if let e { model.setBanner(e) } }
}

struct CreateSessionSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    @Local private var name = ""
    @Local private var source = 0            // 0 leer, 1 Git-Repo, 2 Ordner
    @Local private var repo = ""
    @Local private var folder = ""
    @Local private var preview: ImportPreview?
    @Local private var previewError: String?
    var valid: Bool { name.range(of: #"^[a-z][a-z0-9-]{0,30}$"#, options: .regularExpression) != nil }
    var sourceOK: Bool { source != 2 || (preview != nil && previewError == nil) }

    var body: some View {
        Form {
            Text("Neue Session").font(.headline)
            TextField("Name (a–z, 0–9, -)", text: $name)
            Picker("Quelle für den Code", selection: $source) {
                Text("Leer starten").tag(0)
                Text("Git-Repo klonen").tag(1)
                Text("Bestehenden Ordner kopieren").tag(2)
            }
            if source == 1 { TextField("Git-URL (https://…)", text: $repo) }
            if source == 2 {
                HStack {
                    Text(folder.isEmpty ? "Kein Ordner gewählt" : folder).lineLimit(1).truncationMode(.middle).foregroundStyle(folder.isEmpty ? .secondary : .primary)
                    Spacer()
                    Button("Ordner wählen …") { chooseFolder() }
                }
                if let p = preview { Label("\(p.files) Dateien, \(formatBytes(p.bytes)) werden kopiert", systemImage: "doc.on.doc").font(.callout) }
                if let e = previewError { Label(e, systemImage: "xmark.octagon.fill").foregroundStyle(.red).font(.callout) }
                Text("Der Ordner wird **kopiert**, nicht eingebunden: Die VM sieht nie dein Home. Nicht kopiert werden Einträge der Sperrliste, .env-Dateien, Schlüssel, Symlinks sowie node_modules und Caches. Zugangsdaten in .git/config werden entfernt. Ergebnisse kommen per git push oder über shared/out zurück.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            Text("Wo liegt die Session? Die VM-Platte liegt unter ~/.lima/bf-<name> (Code in der VM unter ~/work/main), die Austausch-Ordner unter ~/.bergfried/sessions/<name>/shared/{in,out}.")
                .font(.caption).foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button("Abbrechen") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Erstellen") {
                    let n = name, r = source == 1 ? repo : "", f = source == 2 ? folder : ""
                    dismiss()
                    Task { if let e = await model.runJob("create-\(n)", "\(n) wird erstellt …", "/v1/sessions/create", NameBody(name: n, repo: r, importPath: f)) { model.setBanner(e) } }
                }.disabled(!valid || !sourceOK).keyboardShortcut(.defaultAction)
            }
        }.padding(20).frame(width: 520)
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
    let session: SessionInfo
    @Local private var name = ""
    var valid: Bool { name.range(of: #"^[a-z][a-z0-9-]{0,30}$"#, options: .regularExpression) != nil }

    var body: some View {
        Form {
            Text("Neuer Agent in „\(session.name)“").font(.headline)
            TextField("Agent-Name", text: $name)
            Text("Jeder Agent bekommt ein tmux-Fenster, eine Remote-Control-Session und ein Git-Worktree.").font(.caption).foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button("Abbrechen") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Hinzufügen") {
                    let n = name, s = session.name
                    dismiss()
                    Task { if let e = await model.runJob("agent-\(s)", "Agent \(n) startet …", "/v1/agents/add", AgentBody(session: s, agent: n)) { model.setBanner(e) } }
                }.disabled(!valid).keyboardShortcut(.defaultAction)
            }
        }.padding(20).frame(width: 440)
    }
}
