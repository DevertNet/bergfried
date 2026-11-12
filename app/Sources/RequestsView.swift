import SwiftUI

struct RequestsView: View {
    @EnvironmentObject var model: AppModel
    @Local private var history: [RequestInfo] = []
    @Local private var showHistory = false
    @Local private var sshFor: RequestInfo?
    @Local private var largeFor: RequestInfo?
    @Local private var gitFor: RequestInfo?
    @Local private var denyFor: RequestInfo?

    var body: some View {
        List {
            Section("Open requests") {
                if model.requests.isEmpty { Text("No open requests.").foregroundStyle(.secondary) }
                ForEach(model.requests, id: \.listID) { r in PendingRow(r: r, decideSSH: { sshFor = r }, confirmLarge: { largeFor = r }, decideGit: { gitFor = r }, deny: { denyFor = r }) }
            }
            Section("Active SSH grants") {
                if model.grants.isEmpty { Text("No active grants.").foregroundStyle(.secondary) }
                ForEach(model.grants, id: \.listID) { g in GrantRow(g: g) }
            }
            Section {
                Toggle("Show finished requests", isOn: $showHistory)
                if showHistory { ForEach(history, id: \.historyID) { r in HistoryRow(r: r) } }
            }
        }
        .onChange(of: showHistory) { _, on in if on { Task { await loadHistory() } } }
        .onChange(of: model.requests) { _, _ in if showHistory { Task { await loadHistory() } } }
        .sheet(item: $sshFor) { r in SSHDecisionSheet(r: r) }
        .sheet(item: $gitFor) { r in GitHubTokenSheet(r: r) }
        .sheet(item: $denyFor) { r in DenyRequestSheet(r: r, onDeny: { note in Task { await model.deny(r, note: note) } }) }
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
    let deny: () -> Void

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
                Text("\(r.sandbox) / \(r.folder)").font(.headline)
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
            Text(L("Assistant's reason (unchecked): %@", r.reason.isEmpty ? "–" : r.reason)).font(.callout).foregroundStyle(.secondary)
            HStack {
                if r.kind == "ghtoken" {
                    Button("Set up …", action: decideGit).buttonStyle(.borderedProminent)
                    Button("Deny", role: .destructive, action: deny)
                } else if r.kind == "ssh" {
                    Button("Decide …", action: decideSSH).buttonStyle(.borderedProminent)
                    Button("Deny", role: .destructive, action: deny)
                } else {
                    Button("Allow") { if r.large { confirmLarge() } else { Task { await model.decideFile(r, allow: true) } } }.buttonStyle(.borderedProminent)
                    Button("Deny", role: .destructive, action: deny)
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
            Text("\(r.sandbox)/\(r.folder)")
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
                Text(L("Task %lld · Sandbox %@ · %@", g.id, g.sandbox, g.folder.isEmpty ? L("all folders") : L("Folder %@", g.folder))
                    + (g.perConnection ? " · " + L("every connection individually") : "")).font(.caption).foregroundStyle(.secondary)
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
    @Local private var minutes = 10
    @Local private var user = ""
    @Local private var trust = false
    @Local private var passphrase = ""
    @Local private var note = ""
    @Local private var error: String?

    static let durationSteps: [(minutes: Int, label: String)] = [(10, "10 minutes"), (20, "20 minutes"), (30, "30 minutes"), (40, "40 minutes"), (50, "50 minutes"), (60, "1 hour"), (90, "90 minutes"), (120, "2 hours"), (240, "4 hours"), (480, "8 hours"), (1440, "24 hours")]

    func keyLabel(_ k: KeyInfo) -> String {
        let name = k.comment.isEmpty ? k.type : k.comment
        let tag = k.loaded == false ? (k.encrypted == true ? " " + L("(not loaded, passphrase)") : " " + L("(not loaded)")) : ""
        return "\(name) – \(k.fingerprint.suffix(12))\(tag)"
    }

    var unknownHost: Bool { (r.ssh?.source ?? "") != "known_hosts" }
    var selectedKey: KeyInfo? { keys.first { $0.fingerprint == key } }
    var notLoaded: Bool { selectedKey.map { $0.loaded == false } ?? false }
    var needsPassphrase: Bool { notLoaded && selectedKey?.encrypted == true }
    var canAllow: Bool { !key.isEmpty && (!unknownHost || trust) && (!needsPassphrase || !passphrase.isEmpty) }

    @ViewBuilder func section<Content: View>(_ title: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(LocalizedStringKey(title)).font(.callout.bold())
            content()
        }.frame(maxWidth: .infinity, alignment: .leading)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                VStack(alignment: .leading, spacing: 3) {
                    Text(L("SSH access for %@/%@", r.sandbox, r.folder)).font(.title3.bold())
                    LT("Target: **%@**", r.path).font(.callout).foregroundStyle(.secondary)
                }
                Spacer()
                if unknownHost {
                    Label("New host key", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption.bold()).foregroundStyle(.orange)
                }
            }
            Divider().padding(.vertical, 12)

            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    section("Request") {
                        Text(L("Assistant's reason (unchecked): %@", r.reason.isEmpty ? "–" : r.reason))
                            .font(.callout).foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
                    }

                    if unknownHost {
                        VStack(alignment: .leading, spacing: 6) {
                            Label("Host key is new (not in your known_hosts)", systemImage: "exclamationmark.triangle.fill")
                                .foregroundStyle(.orange)
                            ForEach(r.ssh?.hostKeyFingerprints ?? [], id: \.self) {
                                Text($0).font(.caption.monospaced()).textSelection(.enabled)
                            }
                            Toggle("I know this host key", isOn: $trust)
                            Text("Only tick this if the fingerprint matches what the server owner told you. Bergfried records it for this host after the grant.")
                                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                        }.padding(10).frame(maxWidth: .infinity, alignment: .leading)
                            .background(.orange.opacity(0.1), in: RoundedRectangle(cornerRadius: 8))
                    }

                    section("Access") {
                        LabeledContent("Duration") {
                            Picker("Duration", selection: $minutes) {
                                ForEach(Self.durationSteps.indices, id: \.self) { i in
                                    Text(LocalizedStringKey(Self.durationSteps[i].label)).tag(Self.durationSteps[i].minutes)
                                }
                            }.labelsHidden().fixedSize()
                        }
                        LabeledContent("User") {
                            VStack(alignment: .leading, spacing: 3) {
                                TextField("", text: $user).textFieldStyle(.roundedBorder).frame(maxWidth: .infinity)
                                if let ru = r.ssh?.requestedUser, !ru.isEmpty {
                                    Text(ru == user ? L("Requested by the assistant.") : L("Requested by the assistant: %@", ru))
                                        .font(.caption).foregroundStyle(.secondary)
                                }
                            }.frame(maxWidth: .infinity, alignment: .leading)
                        }
                        LabeledContent("Key") {
                            VStack(alignment: .leading, spacing: 3) {
                                Picker("Key", selection: $key) {
                                    ForEach(keys) { Text(keyLabel($0)).tag($0.fingerprint) }
                                }.labelsHidden().frame(maxWidth: .infinity, alignment: .leading)
                                if let k = selectedKey {
                                    Text(k.fingerprint).font(.caption.monospaced()).foregroundStyle(.secondary)
                                        .textSelection(.enabled).lineLimit(1).truncationMode(.middle)
                                }
                            }.frame(maxWidth: .infinity, alignment: .leading)
                        }
                        if let keyErr { Text(keyErr).foregroundStyle(.red).font(.caption).fixedSize(horizontal: false, vertical: true) }
                        if needsPassphrase {
                            SecureField("Passphrase of this key", text: $passphrase).textFieldStyle(.roundedBorder)
                        }
                    }

                    if notLoaded {
                        VStack(alignment: .leading, spacing: 5) {
                            Label("Key not loaded in your ssh-agent", systemImage: "key.slash")
                                .font(.caption.bold()).foregroundStyle(.orange)
                            Text("Bergfried decrypts it from its file in ~/.ssh and keeps it in memory only until this grant ends (expiry or revoke). It is not loaded into your ssh-agent, never written anywhere, and the passphrase is not stored. A new grant needs the passphrase again.")
                                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                        }.padding(10).frame(maxWidth: .infinity, alignment: .leading)
                            .background(.orange.opacity(0.08), in: RoundedRectangle(cornerRadius: 8))
                    }

                    section("Note for the assistant (optional)") {
                        TextEditor(text: $note)
                            .font(.callout)
                            .frame(minHeight: 62, maxHeight: 90)
                            .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.secondary.opacity(0.3)))
                        Text("Shown to the assistant with the answer – useful when denying, e.g. \"use the staging host instead\" or \"ask me again tomorrow\". Max. 500 characters.")
                            .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    }
                }.padding(.trailing, 6)
            }

            if let error {
                Text(error).foregroundStyle(.red).font(.callout)
                    .fixedSize(horizontal: false, vertical: true).padding(.top, 8)
            }
            Divider().padding(.vertical, 12)
            HStack(spacing: 10) {
                Text("Deny and approve both send the note to the assistant.")
                    .font(.caption).foregroundStyle(.secondary)
                Spacer()
                Button("Cancel") { passphrase = ""; dismiss() }.keyboardShortcut(.cancelAction)
                Button("Deny", role: .destructive) {
                    passphrase = ""
                    let n = note
                    Task { _ = await model.decideSSH(SSHDecideBody(id: r.id, allow: false, minutes: 0, keyFingerprint: "", user: "", trustUnknownHost: false, note: n)); dismiss() }
                }
                Button("Approve") {
                    let pw = passphrase
                    passphrase = ""   // do not keep it in the view; after a wrong passphrase it has to be typed again
                    let n = note
                    Task {
                        let e = await model.decideSSH(SSHDecideBody(id: r.id, allow: true, minutes: minutes, keyFingerprint: key, user: user, trustUnknownHost: trust, passphrase: pw, note: n))
                        if let e { error = e } else { dismiss() }
                    }
                }.disabled(!canAllow).keyboardShortcut(.defaultAction)
            }
        }
        .padding(20).frame(width: 680, height: 720)
        .task {
            // Precedence: the request's wish, then the host default
            if let ru = r.ssh?.requestedUser, !ru.isEmpty { user = ru } else { user = r.ssh?.defaultUser ?? "claude" }
            minutes = Self.durationSteps.map { $0.minutes }.last(where: { $0 <= (r.ssh?.suggestedMinutes ?? 10) }) ?? 10
            if let resp: KeysResponse = try? await model.api.get("/v1/ssh/keys") {
                keys = resp.keys; keyErr = resp.error
                key = resp.keys.first(where: { $0.fingerprint == r.ssh?.defaultKey })?.fingerprint ?? resp.keys.first?.fingerprint ?? ""
            }
        }
    }
}

struct DenyRequestSheet: View {
    let r: RequestInfo
    let onDeny: (String) -> Void
    @Environment(\.dismiss) var dismiss
    @Local private var note = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(L("Deny request")).font(.title3.bold())
            Text("\(r.sandbox) / \(r.folder)").font(.callout).foregroundStyle(.secondary)
            Text("Optional note for the assistant: why denied, what to do instead.").font(.callout)
            TextEditor(text: $note)
                .font(.callout)
                .frame(height: 80)
                .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.secondary.opacity(0.3)))
            Text("The assistant sees this note with the denial.").font(.caption).foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Deny", role: .destructive) { onDeny(note); dismiss() }.keyboardShortcut(.defaultAction)
            }
        }.padding(20).frame(width: 480)
    }
}

struct GitHubTokenSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    let r: RequestInfo
    @Local private var token = ""
    @Local private var denyNote = ""
    @Local private var error: String?
    @Local private var busy = false
    var d: GitHubDetail? { r.github }
    var hasToken: Bool { d?.hasToken ?? false }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(L("GitHub access for %@ / %@", r.sandbox, r.folder)).font(.headline)
            if let repo = d?.repo, !repo.isEmpty { LT("Repo: **%@**", repo) }
            Text(L("Assistant's reason (unchecked): %@", r.reason.isEmpty ? "–" : r.reason)).font(.callout).foregroundStyle(.secondary)
            if hasToken {
                LT("The sandbox already has a token. It only needs the repo: on GitHub open Settings → Developer settings → Fine-grained tokens, open the token **bergfried-%@**, \"Edit\", add the repo under \"Repository access\" and save. The token value stays the same.", r.sandbox).font(.callout)
                Button("Open the token list on GitHub …") { if let u = URL(string: "https://github.com/settings/personal-access-tokens") { NSWorkspace.shared.open(u) } }
                Text("Then click \"Check access\". No new token is needed – the existing token only needs the additional repository.").font(.caption).foregroundStyle(.secondary)
            } else {
                GitHubTokenSteps(link: d?.link ?? "", hint: d?.hint ?? "", token: $token)
            }
            VStack(alignment: .leading, spacing: 4) {
                Text("Note for the assistant if you deny (optional)").font(.caption).foregroundStyle(.secondary)
                TextEditor(text: $denyNote).font(.callout).frame(height: 54)
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.secondary.opacity(0.3)))
            }
            if let error { Text(error).foregroundStyle(.red).font(.callout).textSelection(.enabled) }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Deny", role: .destructive) { Task { await model.deny(r, note: denyNote); dismiss() } }
                Button(hasToken ? "Check access" : "Check and save") {
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
            Text("The token is then stored in the VM; the assistant can read it and use it for all git actions and GitHub Actions – within the selected repos and permissions.").font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
    }
}
