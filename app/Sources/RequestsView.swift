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
            Section("Open requests") {
                if model.requests.isEmpty { Text("No open requests.").foregroundStyle(.secondary) }
                ForEach(model.requests) { r in PendingRow(r: r, decideSSH: { sshFor = r }, confirmLarge: { largeFor = r }, decideGit: { gitFor = r }) }
            }
            Section("Active SSH grants") {
                if model.grants.isEmpty { Text("No active grants.").foregroundStyle(.secondary) }
                ForEach(model.grants) { g in GrantRow(g: g) }
            }
            Section {
                Toggle("Show finished requests", isOn: $showHistory)
                if showHistory { ForEach(history) { r in HistoryRow(r: r) } }
            }
        }
        .onChange(of: showHistory) { _, on in if on { Task { await loadHistory() } } }
        .onChange(of: model.requests) { _, _ in if showHistory { Task { await loadHistory() } } }
        .sheet(item: $sshFor) { r in SSHDecisionSheet(r: r) }
        .sheet(item: $gitFor) { r in GitHubTokenSheet(r: r) }
        .confirmationDialog("Large approval", isPresented: Binding(get: { largeFor != nil }, set: { if !$0 { largeFor = nil } })) {
            Button(L("Yes, copy %@", largeFor.map { formatBytes($0.bytes) } ?? "")) { if let r = largeFor { Task { await model.decideFile(r, allow: true, confirmLarge: true) } } }
        } message: { Text(L("%lld files, %@. Really copy?", largeFor?.files ?? 0, formatBytes(largeFor?.bytes ?? 0))) }
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
                Text("\(r.sandbox) / \(r.agent)").font(.headline)
                Spacer()
                Text(Date(timeIntervalSince1970: TimeInterval(r.createdUnix)), style: .relative).font(.caption).foregroundStyle(.secondary)
            }
            if r.kind == "ssh" { LT("wants SSH access to **%@**", r.path) }
            else if r.kind == "ghtoken" {
                if (r.github?.repo ?? "").isEmpty { LT("needs a **GitHub token** for git and Actions") }
                else if r.github?.hasToken == true { LT("needs access to the repo **%@** (the sandbox's token does not reach it yet)", r.path) }
                else { LT("needs a **GitHub token** and access to **%@**", r.path) }
            }
            else { LT(r.kind == "folder" ? "wants the folder **~/%@** (%lld file(s), %@)" : "wants the file **~/%@** (%lld file(s), %@)", r.path, r.files, formatBytes(r.bytes)) }
            Text(L("Agent's reason (unchecked): %@", r.reason.isEmpty ? "–" : r.reason)).font(.callout).foregroundStyle(.secondary)
            HStack {
                if r.kind == "ghtoken" {
                    Button("Set up …", action: decideGit).buttonStyle(.borderedProminent)
                    Button("Deny", role: .destructive) { Task { await model.deny(r) } }
                } else if r.kind == "ssh" {
                    Button("Decide …", action: decideSSH).buttonStyle(.borderedProminent)
                    Button("Deny", role: .destructive) { Task { await model.deny(r) } }
                } else {
                    Button("Allow") { if r.large { confirmLarge() } else { Task { await model.decideFile(r, allow: true) } } }.buttonStyle(.borderedProminent)
                    Button("Deny", role: .destructive) { Task { await model.decideFile(r, allow: false) } }
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
            Text("\(r.sandbox)/\(r.agent)")
            Text(["ssh", "ghtoken"].contains(r.kind) ? r.path : "~/" + r.path).lineLimit(1).truncationMode(.middle)
            Spacer()
            Text(LocalizedStringKey(r.status)).foregroundStyle(color)
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
                Text(L("Sandbox %@ · %@ · %@", g.sandbox, g.agent.isEmpty ? L("all agents") : L("Agent %@", g.agent), g.perConnection ? L("every connection individually") : L("silent within the window"))).font(.caption).foregroundStyle(.secondary)
                Text("Key \(g.keyFingerprint)").font(.caption2).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
            }
            Spacer()
            TimelineView(.periodic(from: .now, by: 1)) { ctx in
                let left = max(0, Date(timeIntervalSince1970: TimeInterval(g.expiresUnix)).timeIntervalSince(ctx.date))
                Text(String(format: "%d:%02d:%02d", Int(left) / 3600, Int(left) % 3600 / 60, Int(left) % 60)).monospacedDigit().foregroundStyle(left < 300 ? .red : .primary)
            }
            Menu("Extend") {
                Button("+ 15 minutes") { Task { await model.extend(g, minutes: 15) } }
                Button("+ 1 hour") { Task { await model.extend(g, minutes: 60) } }
                Button("+ 4 hours") { Task { await model.extend(g, minutes: 240) } }
            }.fixedSize()
            Button("Revoke", role: .destructive) { Task { await model.revoke(g) } }
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
            Text(L("SSH access for %@/%@", r.sandbox, r.agent)).font(.headline)
            LT("Target: **%@**", r.path)
            Text(L("Agent's reason (unchecked): %@", r.reason.isEmpty ? "–" : r.reason)).font(.callout).foregroundStyle(.secondary)
            if unknownHost {
                VStack(alignment: .leading, spacing: 4) {
                    Label("Host key is new (not in your known_hosts)", systemImage: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                    ForEach(r.ssh?.hostKeyFingerprints ?? [], id: \.self) { Text($0).font(.caption.monospaced()).textSelection(.enabled) }
                    Toggle("I know this host key", isOn: $trust)
                }
            }
            Picker("Duration", selection: $minutes) {
                Text("15 minutes").tag(15); Text("1 hour").tag(60); Text("4 hours").tag(240); Text("24 hours").tag(1440)
            }
            Picker("Key", selection: $key) {
                ForEach(keys) { Text("\($0.comment.isEmpty ? $0.type : $0.comment) – \($0.fingerprint.suffix(12))").tag($0.fingerprint) }
            }
            if let keyErr { Text(keyErr).foregroundStyle(.red).font(.caption) }
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text("User").frame(width: 80, alignment: .leading)
                VStack(alignment: .leading, spacing: 2) {
                    TextField("", text: $user).textFieldStyle(.roundedBorder)
                    if let ru = r.ssh?.requestedUser, !ru.isEmpty {
                        Text(ru == user ? L("Requested by the agent.") : L("Requested by the agent: %@", ru)).font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
            Toggle("For all agents of the sandbox", isOn: $allAgents)
            Text("Without the checkmark the grant applies to this agent for display only; technically the boundary is the sandbox.").font(.caption).foregroundStyle(.secondary)
            Toggle("Confirm every connection (not yet available in the app)", isOn: $perConn).disabled(true)
            if let error { Text(error).foregroundStyle(.red) }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Deny", role: .destructive) {
                    Task { _ = await model.decideSSH(SSHDecideBody(id: r.id, allow: false, minutes: 0, keyFingerprint: "", user: "", allAgents: false, perConnection: false, trustUnknownHost: false)); dismiss() }
                }
                Button("Approve") {
                    Task {
                        let e = await model.decideSSH(SSHDecideBody(id: r.id, allow: true, minutes: minutes, keyFingerprint: key, user: user, allAgents: allAgents, perConnection: perConn, trustUnknownHost: trust))
                        if let e { error = e } else { dismiss() }
                    }
                }.disabled(!canAllow).keyboardShortcut(.defaultAction)
            }
        }
        .padding(20).frame(width: 580)
        .task {
            // Precedence: the agent's wish, then the host default
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
            Text(L("GitHub access for %@ / %@", r.sandbox, r.agent)).font(.headline)
            if let repo = d?.repo, !repo.isEmpty { LT("Repo: **%@**", repo) }
            Text(L("Agent's reason (unchecked): %@", r.reason.isEmpty ? "–" : r.reason)).font(.callout).foregroundStyle(.secondary)
            if hasToken {
                LT("The sandbox already has a token. It only needs the repo: on GitHub open Settings → Developer settings → Fine-grained tokens, open the token **bergfried-%@**, \"Edit\", add the repo under \"Repository access\" and save. The token value stays the same.", r.sandbox).font(.callout)
                Button("Open the token list on GitHub …") { if let u = URL(string: "https://github.com/settings/personal-access-tokens") { NSWorkspace.shared.open(u) } }
                Text("Then \"Check access\" – or paste a new token if you prefer to create a new one:").font(.caption).foregroundStyle(.secondary)
            } else {
                GitHubTokenSteps(link: d?.link ?? "", hint: d?.hint ?? "", token: $token)
            }
            if hasToken { SecureField("Optional: paste a new token", text: $token).textFieldStyle(.roundedBorder) }
            if let error { Text(error).foregroundStyle(.red).font(.callout).textSelection(.enabled) }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Deny", role: .destructive) { Task { await model.deny(r); dismiss() } }
                Button(hasToken && token.isEmpty ? "Check access" : "Check and save") {
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

/// Shared steps: link to the pre-filled GitHub form, hint about the repository selection, field for the token.
struct GitHubTokenSteps: View {
    let link: String
    let hint: String
    @Binding var token: String
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("1. Create the token on GitHub (the form is pre-filled: expiry 1 year, permissions Contents, Pull requests, Actions):").font(.callout).fixedSize(horizontal: false, vertical: true)
            Button("Create token on GitHub …") { if let u = URL(string: link) { NSWorkspace.shared.open(u) } }.disabled(link.isEmpty)
            Label("Under \"Repository access\" choose \"Only select repositories\" and select only the repos this sandbox needs.", systemImage: "exclamationmark.triangle").font(.callout).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
            Text("2. Click \"Generate token\" and paste the value here (GitHub shows it only once):").font(.callout).fixedSize(horizontal: false, vertical: true)
            SecureField("github_pat_…", text: $token).textFieldStyle(.roundedBorder)
            Text("The token is then stored in the VM; the agent can read it and use it for all git actions and GitHub Actions – within the selected repos and permissions.").font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
    }
}
