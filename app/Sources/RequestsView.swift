import SwiftUI

struct RequestsView: View {
    @EnvironmentObject var model: AppModel
    @Local private var history: [RequestInfo] = []
    @Local private var showHistory = false
    @Local private var sshFor: RequestInfo?
    @Local private var largeFor: RequestInfo?

    var body: some View {
        List {
            Section("Offene Anfragen") {
                if model.requests.isEmpty { Text("Keine offenen Anfragen.").foregroundStyle(.secondary) }
                ForEach(model.requests) { r in PendingRow(r: r, decideSSH: { sshFor = r }, confirmLarge: { largeFor = r }) }
            }
            Section("Aktive SSH-Freigaben") {
                if model.grants.isEmpty { Text("Keine aktiven Freigaben.").foregroundStyle(.secondary) }
                ForEach(model.grants) { g in GrantRow(g: g) }
            }
            Section {
                Toggle("Erledigte Anfragen anzeigen", isOn: $showHistory)
                if showHistory { ForEach(history) { r in HistoryRow(r: r) } }
            }
        }
        .onChange(of: showHistory) { _, on in if on { Task { await loadHistory() } } }
        .onChange(of: model.requests) { _, _ in if showHistory { Task { await loadHistory() } } }
        .sheet(item: $sshFor) { r in SSHDecisionSheet(r: r) }
        .confirmationDialog("Große Freigabe", isPresented: Binding(get: { largeFor != nil }, set: { if !$0 { largeFor = nil } })) {
            Button("Ja, \(largeFor.map { formatBytes($0.bytes) } ?? "") kopieren") { if let r = largeFor { Task { await model.decideFile(r, allow: true, confirmLarge: true) } } }
        } message: { Text("\(largeFor?.files ?? 0) Dateien, \(formatBytes(largeFor?.bytes ?? 0)). Wirklich kopieren?") }
    }

    func loadHistory() async {
        if let h: [RequestInfo] = try? await model.api.get("/v1/requests?all=1") { history = h.filter { !$0.pending } }
    }
}

struct PendingRow: View {
    @EnvironmentObject var model: AppModel
    let r: RequestInfo
    let decideSSH: () -> Void
    let confirmLarge: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Image(systemName: r.kind == "ssh" ? "key.fill" : (r.kind == "folder" ? "folder" : "doc"))
                Text("\(r.session) / \(r.agent)").font(.headline)
                Spacer()
                Text(Date(timeIntervalSince1970: TimeInterval(r.createdUnix)), style: .relative).font(.caption).foregroundStyle(.secondary)
            }
            if r.kind == "ssh" { Text("möchte per SSH auf **\(r.path)** zugreifen") }
            else { Text("möchte \(r.kind == "folder" ? "den Ordner" : "die Datei") **~/\(r.path)** (\(r.files) Datei(en), \(formatBytes(r.bytes)))") }
            Text("Begründung des Agents (ungeprüft): \(r.reason.isEmpty ? "–" : r.reason)").font(.callout).foregroundStyle(.secondary)
            HStack {
                if r.kind == "ssh" {
                    Button("Entscheiden …", action: decideSSH).buttonStyle(.borderedProminent)
                    Button("Ablehnen", role: .destructive) { Task { _ = await model.decideSSH(SSHDecideBody(id: r.id, allow: false, minutes: 0, keyFingerprint: "", user: "", allAgents: false, perConnection: false, trustUnknownHost: false)) } }
                } else {
                    Button("Erlauben") { if r.large { confirmLarge() } else { Task { await model.decideFile(r, allow: true) } } }.buttonStyle(.borderedProminent)
                    Button("Ablehnen", role: .destructive) { Task { await model.decideFile(r, allow: false) } }
                }
            }
        }.padding(.vertical, 4)
    }
}

struct HistoryRow: View {
    let r: RequestInfo
    var color: Color { r.status == "allowed" ? .green : (r.status == "blocked" ? .red : .secondary) }
    var body: some View {
        HStack {
            Image(systemName: r.kind == "ssh" ? "key" : "doc").foregroundStyle(.secondary)
            Text("\(r.session)/\(r.agent)")
            Text(r.kind == "ssh" ? r.path : "~/" + r.path).lineLimit(1).truncationMode(.middle)
            Spacer()
            Text(r.status).foregroundStyle(color)
            Text(Date(timeIntervalSince1970: TimeInterval(r.createdUnix)), style: .date).font(.caption).foregroundStyle(.secondary)
        }.font(.callout)
    }
}

struct GrantRow: View {
    @EnvironmentObject var model: AppModel
    let g: GrantInfo
    var body: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text("\(g.user)@\(g.host)").font(.headline)
                Text("Session \(g.session) · \(g.agent.isEmpty ? "alle Agents" : "Agent " + g.agent) · \(g.perConnection ? "jede Verbindung einzeln" : "still im Zeitfenster")").font(.caption).foregroundStyle(.secondary)
                Text("Key \(g.keyFingerprint)").font(.caption2).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
            }
            Spacer()
            TimelineView(.periodic(from: .now, by: 1)) { ctx in
                let left = max(0, Date(timeIntervalSince1970: TimeInterval(g.expiresUnix)).timeIntervalSince(ctx.date))
                Text(String(format: "%d:%02d:%02d", Int(left) / 3600, Int(left) % 3600 / 60, Int(left) % 60)).monospacedDigit().foregroundStyle(left < 300 ? .red : .primary)
            }
            Menu("Verlängern") {
                Button("+ 15 Minuten") { Task { await model.extend(g, minutes: 15) } }
                Button("+ 1 Stunde") { Task { await model.extend(g, minutes: 60) } }
                Button("+ 4 Stunden") { Task { await model.extend(g, minutes: 240) } }
            }.fixedSize()
            Button("Entziehen", role: .destructive) { Task { await model.revoke(g) } }
        }
    }
}

struct SSHDecisionSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    let r: RequestInfo
    @Local private var keys: [KeyInfo] = []
    @Local private var keyErr: String?
    @Local private var key = ""
    @Local private var minutes = 15
    @Local private var user = ""
    @Local private var allAgents = false
    @Local private var perConn = false
    @Local private var trust = false
    @Local private var error: String?

    var unknownHost: Bool { (r.ssh?.source ?? "") != "known_hosts" }
    var canAllow: Bool { !key.isEmpty && (!unknownHost || trust) }

    var body: some View {
        Form {
            Text("SSH-Zugriff für \(r.session)/\(r.agent)").font(.headline)
            Text("Ziel: **\(r.path)**")
            Text("Begründung des Agents (ungeprüft): \(r.reason.isEmpty ? "–" : r.reason)").font(.callout).foregroundStyle(.secondary)
            if unknownHost {
                VStack(alignment: .leading, spacing: 4) {
                    Label("Host-Key ist neu (nicht in deiner known_hosts)", systemImage: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                    ForEach(r.ssh?.hostKeyFingerprints ?? [], id: \.self) { Text($0).font(.caption.monospaced()).textSelection(.enabled) }
                    Toggle("Ich kenne diesen Host-Key", isOn: $trust)
                }
            }
            Picker("Dauer", selection: $minutes) {
                Text("15 Minuten").tag(15); Text("1 Stunde").tag(60); Text("4 Stunden").tag(240); Text("24 Stunden").tag(1440)
            }
            Picker("Key", selection: $key) {
                ForEach(keys) { Text("\($0.comment.isEmpty ? $0.type : $0.comment) – \($0.fingerprint.suffix(12))").tag($0.fingerprint) }
            }
            if let keyErr { Text(keyErr).foregroundStyle(.red).font(.caption) }
            TextField("Benutzer auf dem Ziel", text: $user)
            Toggle("Für alle Agents der Session (sonst nur Anzeige für diesen Agent)", isOn: $allAgents)
            Toggle("Jede Verbindung einzeln bestätigen", isOn: $perConn)
            if let error { Text(error).foregroundStyle(.red) }
            HStack {
                Spacer()
                Button("Abbrechen") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Ablehnen", role: .destructive) {
                    Task { _ = await model.decideSSH(SSHDecideBody(id: r.id, allow: false, minutes: 0, keyFingerprint: "", user: "", allAgents: false, perConnection: false, trustUnknownHost: false)); dismiss() }
                }
                Button("Freigeben") {
                    Task {
                        let e = await model.decideSSH(SSHDecideBody(id: r.id, allow: true, minutes: minutes, keyFingerprint: key, user: user, allAgents: allAgents, perConnection: perConn, trustUnknownHost: trust))
                        if let e { error = e } else { dismiss() }
                    }
                }.disabled(!canAllow).keyboardShortcut(.defaultAction)
            }
        }
        .padding(20).frame(width: 520)
        .task {
            user = r.ssh?.defaultUser ?? "claude"
            minutes = [15, 60, 240, 1440].last(where: { $0 <= (r.ssh?.suggestedMinutes ?? 15) }) ?? 15
            if let resp: KeysResponse = try? await model.api.get("/v1/ssh/keys") {
                keys = resp.keys; keyErr = resp.error
                key = resp.keys.first(where: { $0.fingerprint == r.ssh?.defaultKey })?.fingerprint ?? resp.keys.first?.fingerprint ?? ""
            }
        }
    }
}
