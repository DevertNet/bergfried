import SwiftUI

struct RequestsView: View {
    @EnvironmentObject var model: AppModel
    @Local private var history: [RequestInfo] = []
    @Local private var showHistory = false
    @Local private var sshFor: RequestInfo?
    @Local private var largeFor: RequestInfo?
    @Local private var gitFor: RequestInfo?

    var body: some View {
        List {
            Section("Offene Anfragen") {
                if model.requests.isEmpty { Text("Keine offenen Anfragen.").foregroundStyle(.secondary) }
                ForEach(model.requests) { r in PendingRow(r: r, decideSSH: { sshFor = r }, confirmLarge: { largeFor = r }, decideGit: { gitFor = r }) }
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
        .sheet(item: $gitFor) { r in GitHubTokenSheet(r: r) }
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
    let decideGit: () -> Void

    var icon: String {
        switch r.kind {
        case "ssh": return "key.fill"
        case "folder": return "folder"
        case "ghtoken": return "key.horizontal.fill"
        default: return "doc"
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Image(systemName: icon)
                Text("\(r.session) / \(r.agent)").font(.headline)
                Spacer()
                Text(Date(timeIntervalSince1970: TimeInterval(r.createdUnix)), style: .relative).font(.caption).foregroundStyle(.secondary)
            }
            if r.kind == "ssh" { Text("möchte per SSH auf **\(r.path)** zugreifen") }
            else if r.kind == "ghtoken" {
                if (r.github?.repo ?? "").isEmpty { Text("braucht ein **GitHub-Token** für Git und Actions") }
                else if r.github?.hasToken == true { Text("braucht Zugriff auf das Repo **\(r.path)** (das Token der Sandbox erreicht es noch nicht)") }
                else { Text("braucht ein **GitHub-Token** und Zugriff auf **\(r.path)**") }
            }
            else { Text("möchte \(r.kind == "folder" ? "den Ordner" : "die Datei") **~/\(r.path)** (\(r.files) Datei(en), \(formatBytes(r.bytes)))") }
            Text("Begründung des Agents (ungeprüft): \(r.reason.isEmpty ? "–" : r.reason)").font(.callout).foregroundStyle(.secondary)
            HStack {
                if r.kind == "ghtoken" {
                    Button("Einrichten …", action: decideGit).buttonStyle(.borderedProminent)
                    Button("Ablehnen", role: .destructive) { Task { await model.deny(r) } }
                } else if r.kind == "ssh" {
                    Button("Entscheiden …", action: decideSSH).buttonStyle(.borderedProminent)
                    Button("Ablehnen", role: .destructive) { Task { await model.deny(r) } }
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
            Image(systemName: ["ssh": "key", "ghtoken": "key.horizontal"][r.kind] ?? "doc").foregroundStyle(.secondary)
            Text("\(r.session)/\(r.agent)")
            Text(["ssh", "ghtoken"].contains(r.kind) ? r.path : "~/" + r.path).lineLimit(1).truncationMode(.middle)
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
                Text("Sandbox \(g.session) · \(g.agent.isEmpty ? "alle Agents" : "Agent " + g.agent) · \(g.perConnection ? "jede Verbindung einzeln" : "still im Zeitfenster")").font(.caption).foregroundStyle(.secondary)
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
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text("Benutzer").frame(width: 80, alignment: .leading)
                VStack(alignment: .leading, spacing: 2) {
                    TextField("", text: $user).textFieldStyle(.roundedBorder)
                    if let ru = r.ssh?.requestedUser, !ru.isEmpty {
                        Text(ru == user ? "Vom Agent angefragt." : "Vom Agent angefragt: \(ru)").font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
            Toggle("Für alle Agents der Sandbox", isOn: $allAgents)
            Text("Ohne Haken gilt die Freigabe zur Anzeige nur für diesen Agent; technisch bleibt die Grenze die Sandbox.").font(.caption).foregroundStyle(.secondary)
            Toggle("Jede Verbindung einzeln bestätigen (in der App noch nicht verfügbar)", isOn: $perConn).disabled(true)
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
        .padding(20).frame(width: 580)
        .task {
            // Vorrang: Wunsch des Agents, dann Standard des Hosts
            if let ru = r.ssh?.requestedUser, !ru.isEmpty { user = ru } else { user = r.ssh?.defaultUser ?? "claude" }
            minutes = [15, 60, 240, 1440].last(where: { $0 <= (r.ssh?.suggestedMinutes ?? 15) }) ?? 15
            if let resp: KeysResponse = try? await model.api.get("/v1/ssh/keys") {
                keys = resp.keys; keyErr = resp.error
                key = resp.keys.first(where: { $0.fingerprint == r.ssh?.defaultKey })?.fingerprint ?? resp.keys.first?.fingerprint ?? ""
            }
        }
    }
}

struct GitHubTokenSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    let r: RequestInfo
    @Local private var token = ""
    @Local private var error: String?
    @Local private var busy = false
    var d: GitHubDetail? { r.github }
    var hasToken: Bool { d?.hasToken ?? false }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("GitHub-Zugriff für \(r.session) / \(r.agent)").font(.headline)
            if let repo = d?.repo, !repo.isEmpty { Text("Repo: **\(repo)**") }
            Text("Begründung des Agents (ungeprüft): \(r.reason.isEmpty ? "–" : r.reason)").font(.callout).foregroundStyle(.secondary)
            if hasToken {
                Text("Die Sandbox hat schon ein Token. Es muss nur das Repo bekommen: bei GitHub unter Settings → Developer settings → Fine-grained tokens das Token **bergfried-\(r.session)** öffnen, „Edit“, unter „Repository access“ das Repo hinzufügen und speichern. Der Token-Wert bleibt gleich.").font(.callout)
                Button("Token-Liste bei GitHub öffnen …") { if let u = URL(string: "https://github.com/settings/personal-access-tokens") { NSWorkspace.shared.open(u) } }
                Text("Danach „Zugriff prüfen“ – oder ein neues Token einfügen, falls du lieber eins neu anlegst:").font(.caption).foregroundStyle(.secondary)
            } else {
                GitHubTokenSteps(link: d?.link ?? "", hint: d?.hint ?? "", token: $token)
            }
            if hasToken { SecureField("Optional: neues Token einfügen", text: $token).textFieldStyle(.roundedBorder) }
            if let error { Text(error).foregroundStyle(.red).font(.callout).textSelection(.enabled) }
            HStack {
                Spacer()
                Button("Abbrechen") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Ablehnen", role: .destructive) { Task { await model.deny(r); dismiss() } }
                Button(hasToken && token.isEmpty ? "Zugriff prüfen" : "Prüfen und speichern") {
                    busy = true
                    Task {
                        let e = await model.decideGitHub(id: r.id, token: token)
                        busy = false
                        if let e { error = e } else { dismiss() }
                    }
                }.disabled(busy || (!hasToken && token.trimmingCharacters(in: .whitespaces).isEmpty)).keyboardShortcut(.defaultAction)
            }
        }
        .padding(20).frame(width: 600)
    }
}

/// Gemeinsame Schritte: Link zum vorausgefüllten GitHub-Formular, Hinweis zur Repo-Auswahl, Feld für das Token.
struct GitHubTokenSteps: View {
    let link: String
    let hint: String
    @Binding var token: String
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("1. Token bei GitHub anlegen (Formular ist vorausgefüllt: Ablauf 1 Jahr, Rechte Contents, Pull requests, Actions):").font(.callout).fixedSize(horizontal: false, vertical: true)
            Button("Token bei GitHub anlegen …") { if let u = URL(string: link) { NSWorkspace.shared.open(u) } }.disabled(link.isEmpty)
            Label(hint.isEmpty ? "Unter „Repository access“ bitte „Only select repositories“ wählen und nur die benötigten Repos auswählen." : hint, systemImage: "exclamationmark.triangle").font(.callout).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
            Text("2. „Generate token“ klicken und den Wert hier einfügen (GitHub zeigt ihn nur einmal):").font(.callout).fixedSize(horizontal: false, vertical: true)
            SecureField("github_pat_…", text: $token).textFieldStyle(.roundedBorder)
            Text("Das Token liegt anschließend in der VM; der Agent kann es lesen und für alle Git-Aktionen und GitHub Actions nutzen – innerhalb der gewählten Repos und Rechte.").font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
    }
}
