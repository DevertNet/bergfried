import SwiftUI

/// Requests page: what needs a decision is pinned on top, running grants and full repo access sit directly below,
/// finished requests are a collapsed section at the end (docs/design/requests-redesign.md).
struct RequestsView: View {
    @EnvironmentObject var model: AppModel
    @Local private var history: [RequestInfo] = []
    @Local private var showHistory = false
    @Local private var historyFilter = ""
    @Local private var sandboxFilter = ""   // "" = all sandboxes
    @Local private var sshFor: RequestInfo?
    @Local private var largeFor: RequestInfo?
    @Local private var gitFor: RequestInfo?
    @Local private var gitGrantFor: RequestInfo?
    @Local private var skillFor: RequestInfo?
    @Local private var denyFor: RequestInfo?
    @Local private var denyAllFor: String?
    @Local private var openWindow = false

    func visible(_ sandbox: String) -> Bool { sandboxFilter.isEmpty || sandbox == sandboxFilter }
    /// Oldest first: the one that lapses first is on top.
    var openRequests: [RequestInfo] { model.requests.filter { visible($0.sandbox) }.sorted { $0.createdUnix < $1.createdUnix } }
    var activeItems: [ActiveItem] {
        (model.gitGrants.filter { visible($0.sandbox) }.map { ActiveItem.git($0) } + model.grants.filter { visible($0.sandbox) }.map { ActiveItem.ssh($0) })
            .sorted { ($0.sandbox, $0.expiresUnix) < ($1.sandbox, $1.expiresUnix) }
    }
    var sandboxNames: [String] {
        Array(Set(model.sandboxes.map(\.name) + model.requests.map(\.sandbox) + model.gitGrants.map(\.sandbox) + model.grants.map(\.sandbox))).sorted()
    }
    var filteredHistory: [RequestInfo] {
        let q = historyFilter.trimmingCharacters(in: .whitespaces).lowercased()
        return history.filter { visible($0.sandbox) && (q.isEmpty || [$0.sandbox, $0.folder, $0.path, $0.kind, $0.reason].contains { $0.lowercased().contains(q) }) }
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                HStack {
                    Text(L("%lld waiting for you · %lld active", openRequests.count, activeItems.count)).foregroundStyle(.secondary)
                    Spacer()
                    Picker("Sandbox", selection: $sandboxFilter) {
                        Text("All sandboxes").tag("")
                        ForEach(sandboxNames, id: \.self) { Text($0).tag($0) }
                    }.fixedSize().labelsHidden()
                }
                openBlock
                activeBlock
                historyBlock
            }
            .padding(20)
        }
        .task { await loadHistory() }
        .onChange(of: model.requests) { _, _ in Task { await loadHistory() } }
        .sheet(item: $sshFor) { r in SSHDecisionSheet(r: r) }
        .sheet(item: $gitFor) { r in GitHubTokenSheet(r: r) }
        .sheet(item: $gitGrantFor) { r in GitGrantSheet(r: r) }
        .sheet(isPresented: $openWindow) { GitWindowSheet() }
        .sheet(item: $skillFor) { r in SkillProposalSheet(r: r) }
        .sheet(item: $denyFor) { r in DenyRequestSheet(r: r, onDeny: { note in Task { await model.deny(r, note: note) } }) }
        .confirmationDialog("Large approval", isPresented: Binding(get: { largeFor != nil }, set: { if !$0 { largeFor = nil } })) {
            Button(L("Yes, copy %@", largeFor.map { formatBytes($0.bytes) } ?? "")) { if let r = largeFor { Task { await model.decideFile(r, allow: true, confirmLarge: true) } } }
        } message: { Text(L("%lld files, %@. Really copy?", largeFor?.files ?? 0, formatBytes(largeFor?.bytes ?? 0))) }
        .confirmationDialog(L("Deny all open requests of %@?", denyAllFor ?? ""), isPresented: Binding(get: { denyAllFor != nil }, set: { if !$0 { denyAllFor = nil } })) {
            Button("Deny all", role: .destructive) {
                if let sb = denyAllFor { let rs = model.requests.filter { $0.sandbox == sb }; Task { for r in rs { await model.deny(r) } } }
            }
        }
    }

    // MARK: Open

    @ViewBuilder var openBlock: some View {
        if openRequests.isEmpty {
            HStack {
                Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                Text("Nothing needs your decision").bold()
                Spacer()
                Text("New requests appear here and as a notification").font(.caption).foregroundStyle(.secondary)
            }
            .padding(12)
            .background(Color.green.opacity(0.08), in: RoundedRectangle(cornerRadius: 12))
        } else {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 8) {
                    Text("Needs your decision").font(.headline)
                    CountBadge(count: openRequests.count, color: .red)
                    Spacer()
                    Label("Quotes are the assistant's words, not checked", systemImage: "info.circle").font(.caption).foregroundStyle(.secondary)
                    Menu {
                        ForEach(Array(Set(openRequests.map(\.sandbox))).sorted(), id: \.self) { sb in
                            Button(L("Deny all from %@ …", sb)) { denyAllFor = sb }
                        }
                    } label: { Image(systemName: "ellipsis.circle") }.menuStyle(.borderlessButton).fixedSize()
                }
                ForEach(openRequests, id: \.listID) { r in
                    PendingRow(r: r, decideSSH: { sshFor = r }, confirmLarge: { largeFor = r }, decideGit: { gitFor = r }, decideGitGrant: { gitGrantFor = r }, reviewSkill: { skillFor = r }, deny: { denyFor = r })
                }
            }
            .padding(12)
            .background(Color.red.opacity(0.06), in: RoundedRectangle(cornerRadius: 14))
            .overlay(RoundedRectangle(cornerRadius: 14).strokeBorder(Color.red.opacity(0.18)))
        }
    }

    // MARK: Active

    var activeBlock: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Text("Active").font(.headline)
                CountBadge(count: activeItems.count, color: .secondary)
                Spacer()
                Button { openWindow = true } label: { Label("Grant full repo access …", systemImage: "plus") }.buttonStyle(.link)
            }
            if activeItems.isEmpty { Text("No active grants.").foregroundStyle(.secondary).padding(.vertical, 4) }
            ForEach(activeItems, id: \.listID) { item in
                switch item {
                case .git(let g): GitGrantRow(g: g)
                case .ssh(let g): GrantRow(g: g)
                }
            }
        }
    }

    // MARK: History

    var historyBlock: some View {
        VStack(alignment: .leading, spacing: 8) {
            Divider()
            HStack {
                Button { showHistory.toggle() } label: {
                    HStack(spacing: 6) {
                        Image(systemName: showHistory ? "chevron.down" : "chevron.right").font(.caption.bold()).frame(width: 12)
                        Text("History").font(.headline)
                    }
                }.buttonStyle(.plain)
                Spacer()
                if showHistory {
                    TextField("Filter by sandbox, repo, host", text: $historyFilter).textFieldStyle(.roundedBorder).frame(width: 240)
                } else {
                    Text(L("%lld finished requests", history.filter { visible($0.sandbox) }.count)).font(.caption).foregroundStyle(.secondary)
                }
            }
            if showHistory {
                if filteredHistory.isEmpty { Text("Nothing here.").foregroundStyle(.secondary) }
                ForEach(filteredHistory, id: \.historyID) { r in
                    HistoryRow(r: r)
                    Divider()
                }
            }
        }
    }

    func loadHistory() async {
        if let h: [RequestInfo] = try? await model.api.get("/v1/requests?all=1") { history = h.filter { !$0.pending } }
    }
}

/// An entry of the Active block: a git grant or full repo access, or an SSH grant.
enum ActiveItem {
    case git(GitGrantInfo), ssh(GrantInfo)
    var sandbox: String { switch self { case .git(let g): return g.sandbox; case .ssh(let g): return g.sandbox } }
    var expiresUnix: Int64 { switch self { case .git(let g): return g.expiresUnix; case .ssh(let g): return g.expiresUnix } }
    var listID: String { switch self { case .git(let g): return g.listID; case .ssh(let g): return g.listID } }
}

struct CountBadge: View {
    let count: Int
    let color: Color
    var body: some View {
        Text("\(count)").font(.caption2.bold()).foregroundStyle(.white)
            .padding(.horizontal, 6).padding(.vertical, 1).background(color, in: Capsule())
    }
}

/// Small rounded label (sandbox, folder, file size).
struct Chip: View {
    let text: String
    init(_ text: String) { self.text = text }
    var body: some View {
        Text(text).font(.caption).lineLimit(1)
            .padding(.horizontal, 8).padding(.vertical, 2)
            .background(Color(nsColor: .controlBackgroundColor), in: Capsule())
            .overlay(Capsule().strokeBorder(Color.secondary.opacity(0.3)))
    }
}

/// Remaining time, counted down every second.
struct TimeLeft: View {
    let expiresUnix: Int64
    var warnBelow: TimeInterval = 120
    var long = true   // h:mm:ss, else m:ss
    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { ctx in
            let left = max(0, Date(timeIntervalSince1970: TimeInterval(expiresUnix)).timeIntervalSince(ctx.date))
            Text(long ? String(format: "%d:%02d:%02d", Int(left) / 3600, Int(left) % 3600 / 60, Int(left) % 60) : String(format: "%d:%02d", Int(left) / 60, Int(left) % 60))
                .monospacedDigit().foregroundStyle(left < warnBelow ? .red : .primary)
        }
    }
}

/// Remaining-time bar: the share of the whole length (created → expires) that is left.
struct TimeBar: View {
    let createdUnix: Int64?
    let expiresUnix: Int64
    let color: Color
    var body: some View {
        TimelineView(.periodic(from: .now, by: 5)) { ctx in
            let total = Double(expiresUnix - (createdUnix ?? expiresUnix))
            let left = max(0, Double(expiresUnix) - ctx.date.timeIntervalSince1970)
            let frac = total > 0 ? min(1, left / total) : 0
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color.secondary.opacity(0.18))
                    Capsule().fill(color).frame(width: geo.size.width * frac)
                }
            }.frame(height: 5)
        }
    }
}

extension View {
    /// Card for kinds without their own colour (files, skills): neutral grey.
    @ViewBuilder func cardStyle(_ kind: KindStyle?) -> some View {
        if kind != nil { self.kindCard(kind) }
        else {
            self.padding(10)
                .background(Color.secondary.opacity(0.1), in: RoundedRectangle(cornerRadius: 10))
                .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(Color.secondary.opacity(0.35), lineWidth: 2))
        }
    }
}

extension RequestInfo {
    /// The assistant's tool sends "unknown" (or nothing) when it cannot name a folder: show only the sandbox then.
    var hasFolder: Bool { !folder.isEmpty && folder != "unknown" }
    var origin: String { hasFolder ? "\(sandbox) / \(folder)" : sandbox }
    /// When the request lapses without an answer (older daemons: ten minutes after creation).
    var lapsesUnix: Int64 { expiresUnix ?? createdUnix + 600 }
}

/// Compact card of an open request: what is asked, where from, the assistant's quote and the buttons.
struct PendingRow: View {
    @EnvironmentObject var model: AppModel
    let r: RequestInfo
    let decideSSH: () -> Void
    let confirmLarge: () -> Void
    let decideGit: () -> Void
    let decideGitGrant: () -> Void
    let reviewSkill: () -> Void
    let deny: () -> Void

    var icon: String {
        switch r.kind {
        case "ssh": return "key.fill"
        case "folder": return "folder"
        case "ghtoken": return "key.horizontal.fill"
        case "gitgrant": return "arrow.triangle.branch"
        case "ghaction": return "arrow.triangle.merge"
        case "skill": return "book.closed"
        default: return "doc"
        }
    }
    /// Minutes of the one-click "Allow": what the assistant asked for, at most 15.
    var quickMinutes: Int { min(15, max(1, r.git?.minutes ?? 15)) }

    var body: some View {
        let kind = KindStyle(requestKind: r.kind)
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: icon).foregroundStyle(kind?.strong ?? .secondary)
                title
                Spacer()
                HStack(spacing: 4) { Image(systemName: "timer"); TimeLeft(expiresUnix: r.lapsesUnix, warnBelow: 60, long: false) }
                    .font(.caption.bold()).padding(.horizontal, 8).padding(.vertical, 2)
                    .background(Color(nsColor: .controlBackgroundColor), in: Capsule())
            }
            HStack(spacing: 6) {
                Chip(r.sandbox)
                if r.hasFolder { Chip(r.folder) }
                if r.kind == "folder" || r.kind == "file" { Chip(L("%lld file(s), %@", r.files, formatBytes(r.bytes))) }
                if r.kind == "skill" { Chip(L("%lld file(s)", r.files)) }
                KindPillIf(kind: kind)
            }
            if r.kind == "ghaction", let sum = r.git?.summary, !sum.isEmpty {
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(sum, id: \.self) { Text($0).font(.callout.bold()).textSelection(.enabled) }
                }
                .frame(maxWidth: .infinity, alignment: .leading).padding(8)
                .background(Color(nsColor: .textBackgroundColor), in: RoundedRectangle(cornerRadius: 8))
            }
            if r.kind == "gitgrant" && r.git?.workflows == true {
                Label("Workflow files run code on GitHub with the repository's secrets. Check the reason carefully.", systemImage: "exclamationmark.triangle.fill")
                    .font(.caption).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
            }
            HStack(alignment: .bottom, spacing: 12) {
                Text("“\(r.reason.isEmpty ? "–" : r.reason)”").font(.callout.italic()).foregroundStyle(.secondary)
                    .lineLimit(2).help(r.reason).textSelection(.enabled)
                Spacer(minLength: 8)
                buttons
            }
        }
        .cardStyle(kind)
    }

    @ViewBuilder var title: some View {
        Group {
            if r.kind == "ssh" { LT("wants SSH access to **%@**", r.path) }
            else if r.kind == "ghtoken" {
                if (r.github?.repo ?? "").isEmpty { LT("needs a **GitHub token** for git and Actions") }
                else if r.github?.hasToken == true { LT("needs access to the repo **%@** (the sandbox's token does not reach it yet)", r.path) }
                else { LT("needs a **GitHub token** and access to **%@**", r.path) }
            }
            else if r.kind == "gitgrant" {
                if r.git?.write == true && r.git?.workflows == true { LT("wants **git access (read and write, incl. workflow files)** to **%@**", r.path) }
                else if r.git?.write == true { LT("wants **git access (read and write)** to **%@**", r.path) }
                else { LT("wants **git access (read only)** to **%@**", r.path) }
            }
            else if r.kind == "ghaction" { LT("wants to do this on GitHub in **%@**:", r.git?.repo ?? r.path) }
            else if r.kind == "skill" { LT("wants to change the shared skill(s) **%@**", r.path) }
            else { LT(r.kind == "folder" ? "wants the folder **~/%@**" : "wants the file **~/%@**", r.path) }
        }.font(.callout).lineLimit(2)
    }

    @ViewBuilder var buttons: some View {
        HStack(spacing: 8) {
            if r.kind == "ghtoken" {
                Button("Set up …", action: decideGit).buttonStyle(.borderedProminent)
            } else if r.kind == "gitgrant" {
                if r.git?.workflows == true {
                    // workflow code runs with the repository's secrets: no one-click allow, the sheet shows the warning
                    Button("Decide …", action: decideGitGrant).buttonStyle(.borderedProminent)
                } else {
                    Button(L("Allow %lld min", quickMinutes)) { Task { _ = await model.allowGit(id: r.id, minutes: quickMinutes, write: r.git?.write ?? false) } }.buttonStyle(.borderedProminent)
                    Button("Decide …", action: decideGitGrant)
                }
            } else if r.kind == "ghaction" {
                Button("Allow once") { Task { _ = await model.allowGit(id: r.id, minutes: 0, write: false) } }.buttonStyle(.borderedProminent)
            } else if r.kind == "ssh" {
                Button("Decide …", action: decideSSH).buttonStyle(.borderedProminent)
            } else if r.kind == "skill" {
                Button("Review changes …", action: reviewSkill).buttonStyle(.borderedProminent)
            } else {
                Button("Allow") { if r.large { confirmLarge() } else { Task { await model.decideFile(r, allow: true) } } }.buttonStyle(.borderedProminent)
            }
            Button("Deny", role: .destructive, action: deny)
        }
    }
}

struct KindPillIf: View {
    let kind: KindStyle?
    var body: some View { if let kind { KindPill(kind: kind) } }
}

struct HistoryRow: View {
    let r: RequestInfo
    @Local private var open = false
    var color: Color { r.status == "allowed" ? .green : (r.status == "blocked" || r.status == "failed" ? .red : .secondary) }
    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack {
                Image(systemName: ["ssh": "key", "ghtoken": "key.horizontal", "gitgrant": "arrow.triangle.branch", "ghaction": "arrow.triangle.merge", "skill": "book.closed"][r.kind] ?? "doc").foregroundStyle(KindStyle(requestKind: r.kind)?.strong ?? .secondary)
                if let k = KindStyle(requestKind: r.kind) { KindPill(kind: k) }
                Text(r.origin)
                Text(["ssh", "ghtoken", "gitgrant", "ghaction", "skill"].contains(r.kind) ? r.path : "~/" + r.path).lineLimit(1).truncationMode(.middle).foregroundStyle(.secondary)
                Spacer()
                Text(LocalizedStringKey(r.status)).foregroundStyle(color)
                Text(Date(timeIntervalSince1970: TimeInterval(r.createdUnix)), style: .date).font(.caption).foregroundStyle(.secondary)
            }.font(.callout)
            .contentShape(Rectangle()).onTapGesture { open.toggle() }
            if open {
                ForEach(r.git?.summary ?? [], id: \.self) { Text($0).font(.caption.bold()).foregroundStyle(.secondary) }
                if !r.reason.isEmpty {
                    Text("“\(r.reason)”").font(.caption.italic()).foregroundStyle(.secondary).textSelection(.enabled)
                }
            }
        }
    }
}

/// One row of the Active block: title with badge, sandbox chip, meta line, remaining-time bar, Extend and Revoke.
struct ActiveRow<Extend: View>: View {
    let kind: KindStyle
    let icon: String
    let title: String
    let badge: String
    let sandbox: String
    let meta: String
    let reason: String?
    let createdUnix: Int64?
    let expiresUnix: Int64
    let extend: Extend
    let revoke: () -> Void
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: icon).foregroundStyle(kind.strong)
                Text(title).font(.headline).foregroundStyle(kind.text).lineLimit(1).truncationMode(.middle)
                Text(badge).font(.caption2.bold()).foregroundStyle(.white).padding(.horizontal, 7).padding(.vertical, 2).background(kind.strong, in: Capsule())
                Spacer()
                extend
                Button("Revoke", role: .destructive, action: revoke)
            }
            HStack(spacing: 6) {
                Chip(sandbox)
                Text(meta).font(.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                Spacer()
                TimeLeft(expiresUnix: expiresUnix, warnBelow: 120).font(.callout.bold())
            }
            TimeBar(createdUnix: createdUnix, expiresUnix: expiresUnix, color: kind.strong)
            if let reason, !reason.isEmpty {
                Text("“\(reason)”").font(.caption.italic()).foregroundStyle(.secondary).lineLimit(2).help(reason).textSelection(.enabled)
            }
        }
        .kindCard(kind)
    }
}

struct GrantRow: View {
    @EnvironmentObject var model: AppModel
    let g: GrantInfo
    var body: some View {
        ActiveRow(kind: .ssh, icon: "key.fill", title: "\(g.user)@\(g.host)", badge: "SSH", sandbox: g.sandbox,
                  meta: L("Task %lld", g.id) + (g.folder.isEmpty ? " · " + L("all folders") : " · " + g.folder) + (g.perConnection ? " · " + L("every connection individually") : "") + " · " + g.keyFingerprint,
                  reason: g.reason, createdUnix: g.createdUnix, expiresUnix: g.expiresUnix,
                  extend: Menu("Extend") {
                      Button("+ 15 minutes") { Task { await model.extend(g, minutes: 15) } }
                      Button("+ 1 hour") { Task { await model.extend(g, minutes: 60) } }
                      Button("+ 4 hours") { Task { await model.extend(g, minutes: 240) } }
                  }.fixedSize(),
                  revoke: { Task { await model.revoke(g) } })
    }
}

/// Git grant or full repo access.
struct GitGrantRow: View {
    @EnvironmentObject var model: AppModel
    let g: GitGrantInfo
    var badge: String {
        var parts = [g.window == true ? L("FULL ACCESS") : (g.write ? L("READ + WRITE") : L("READ ONLY"))]
        if g.window == true && g.merge == true { parts.append(L("MERGE")) }
        if g.workflows == true { parts.append(L("WORKFLOWS")) }
        return parts.joined(separator: " · ")
    }
    var body: some View {
        ActiveRow(kind: .github, icon: "arrow.triangle.branch", title: g.repo, badge: badge, sandbox: g.sandbox,
                  meta: L("Task %lld", g.id) + (g.folder.isEmpty || g.folder == "unknown" ? "" : " · " + g.folder),
                  reason: g.reason, createdUnix: g.createdUnix, expiresUnix: g.expiresUnix,
                  extend: Menu("Extend") {
                      Button("+ 15 minutes") { Task { await model.extendGit(g, minutes: 15) } }
                      Button("+ 1 hour") { Task { await model.extendGit(g, minutes: 60) } }
                      if g.window == true {
                          Button("+ 4 hours") { Task { await model.extendGit(g, minutes: 240) } }
                          Button("+ 8 hours") { Task { await model.extendGit(g, minutes: 480) } }
                      }
                  }.fixedSize(),
                  revoke: { Task { await model.revokeGit(g) } })
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
    @FocusState private var passphraseFocused: Bool
    @Local private var note = ""
    @Local private var error: String?

    static let durationSteps: [(minutes: Int, label: String)] = [(10, "10 minutes"), (20, "20 minutes"), (30, "30 minutes"), (40, "40 minutes"), (50, "50 minutes"), (60, "1 hour"), (90, "90 minutes"), (120, "2 hours"), (240, "4 hours"), (480, "8 hours"), (1440, "24 hours")]

    func keyLabel(_ k: KeyInfo) -> String {
        let name = k.comment.isEmpty ? k.type : k.comment
        let tag = k.loaded == false ? (k.encrypted == true ? " " + L("(not loaded, passphrase)") : " " + L("(not loaded)")) : ""
        return "\(name) – \(k.fingerprint.suffix(12))\(tag)"
    }

    var unknownHost: Bool { (r.ssh?.source ?? "") != "known_hosts" }
    var title: String { r.hasFolder ? L("SSH access for %@/%@", r.sandbox, r.folder) : L("SSH access for %@", r.sandbox) }
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

    var sheetContent: some View {
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
                        .focused($passphraseFocused)
                        .onAppear { passphraseFocused = true } // type right away when the field appears
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

    var body: some View {
        KindFrame(kind: .ssh, title: title) { form }.frame(width: 680).frame(maxHeight: 720)
    }

    var form: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                VStack(alignment: .leading, spacing: 3) {
                    LT("Target: **%@**", r.path).font(.title3).foregroundStyle(.primary)
                }
                Spacer()
                if unknownHost {
                    Label("New host key", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption.bold()).foregroundStyle(.orange)
                }
            }
            Divider().padding(.vertical, 12)

            ViewThatFits(in: .vertical) {
                sheetContent
                ScrollView { sheetContent }
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
        .padding(20)
        .onChange(of: error) { _, e in if e != nil && needsPassphrase { passphraseFocused = true } } // wrong passphrase: the field was cleared
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
            Text(r.origin).font(.callout).foregroundStyle(.secondary)
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
        KindFrame(kind: .github, title: r.hasFolder ? L("GitHub access for %@ / %@", r.sandbox, r.folder) : L("GitHub access for %@", r.sandbox)) { form }.frame(width: 600)
    }

    var form: some View {
        VStack(alignment: .leading, spacing: 10) {
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
        .padding(20)
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

/// The user's decision on a git grant: duration and level (never above what the assistant asked for).
struct GitGrantSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    let r: RequestInfo
    @Local private var minutes = 15
    @Local private var write = true
    @Local private var window = false
    @Local private var merge = false
    @Local private var workflows = false
    @Local private var windowMinutes = 120
    @Local private var error: String?

    static let steps: [(minutes: Int, label: String)] = [(5, "5 minutes"), (10, "10 minutes"), (15, "15 minutes"), (30, "30 minutes"), (45, "45 minutes"), (60, "1 hour")]
    /// Full repo access: up to 24 hours (the daemon enforces the limit).
    static let windowSteps: [(minutes: Int, label: String)] = [(60, "1 hour"), (120, "2 hours"), (180, "3 hours"), (240, "4 hours"), (480, "8 hours"), (1440, "24 hours")]
    var asked: Bool { r.git?.write ?? false }
    var availableSteps: [(minutes: Int, label: String)] { Self.steps.filter { $0.minutes <= (r.git?.maxMinutes ?? 60) } }
    /// The preselection must be one of the picker's steps: a tag that matches nothing (the assistant suggests 20 or 25 minutes)
    /// leaves the Picker empty. Take the smallest step that covers the suggestion, else the longest.
    var initialMinutes: Int {
        let want = r.git?.minutes ?? 15
        let steps = availableSteps.map { $0.minutes }
        return steps.first(where: { $0 >= want }) ?? steps.last ?? 15
    }
    var title: String { r.hasFolder ? L("Git access for %@/%@", r.sandbox, r.folder) : L("Git access for %@", r.sandbox) }

    var body: some View {
        KindFrame(kind: .github, title: title) { form }.frame(width: 480)
            .onAppear { minutes = initialMinutes; write = asked; workflows = r.git?.workflows ?? false }
    }

    var form: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let repos = r.git?.repos, repos.count > 1 {
                Text(L("%lld repositories, one decision for all:", repos.count)).font(.callout).foregroundStyle(.secondary)
                ForEach(repos, id: \.self) { Text($0).font(.title3.bold()).textSelection(.enabled) }
            } else {
                Text(r.path).font(.title3.bold()).textSelection(.enabled)
            }
            Text(L("Assistant's reason (unchecked): %@", r.reason.isEmpty ? "–" : r.reason)).font(.callout).foregroundStyle(.secondary)
            if window {
                Picker("Duration", selection: $windowMinutes) {
                    ForEach(Self.windowSteps, id: \.minutes) { Text(L($0.label)).tag($0.minutes) }
                }
                Toggle("Also merge pull requests", isOn: $merge)
                Text("Full repo access: push, pull requests and CI re-runs for the repositories above need no further approval until it ends. Not included unless ticked below: workflow files. Never included: repository settings, other repositories. You can revoke it at any time under Active.")
                    .font(.callout).fixedSize(horizontal: false, vertical: true)
            } else {
                Picker("Duration", selection: $minutes) {
                    ForEach(availableSteps, id: \.minutes) { Text(L($0.label)).tag($0.minutes) }
                }
                if asked {
                    Toggle("Allow push (write access)", isOn: $write)
                } else {
                    Text("Read only: clone, fetch and pull.").font(.callout)
                }
            }
            if window || (asked && write) {
                Toggle("Also allow changes to workflow files (.github/workflows)", isOn: $workflows)
                if workflows {
                    Text("Workflow files run code on GitHub with the repository's secrets. The assistant could add a step that sends secrets away or deploys. Tick this only if it really has to change workflows, keep the time short and check the result on GitHub.")
                        .font(.caption).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
                }
            }
            Toggle("Allow full repo access (everything for these repositories)", isOn: $window)
            if let others = r.git?.others, !others.isEmpty {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Other active git grants of this sandbox share the same token while they run:").font(.caption).foregroundStyle(.secondary)
                    ForEach(others, id: \.self) { Text($0).font(.caption) }
                }
            }
            Text("The token lives only for this grant, covers only this repository (and the other granted ones of the same owner) and is revoked on GitHub when the grant ends. Within the time the assistant can push to any branch of the repository, including main, unless GitHub protects it.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if let error { Text(error).foregroundStyle(.red).font(.callout) }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                Button("Allow") {
                    Task {
                        let e = window ? await model.allowGitWindow(id: r.id, minutes: windowMinutes, merge: merge, workflows: workflows) : await model.allowGit(id: r.id, minutes: minutes, write: asked && write, workflows: workflows && asked && write)
                        if let e { error = e } else { dismiss() }
                    }
                }.buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
    }
}

/// Grants full repo access on the user's own initiative: several per sandbox (one per repository) are possible.
struct GitWindowSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    @Local private var sandbox = ""
    @Local private var repo = ""
    @Local private var minutes = 120
    @Local private var merge = false
    @Local private var workflows = false
    @Local private var error: String?
    @Local private var busy = false

    var body: some View {
        KindFrame(kind: .github, title: L("Grant full repo access")) { form }.frame(width: 480)
            .onAppear { if sandbox.isEmpty { sandbox = model.sandboxes.first(where: { $0.status == "running" })?.name ?? model.sandboxes.first?.name ?? "" } }
    }

    var form: some View {
        VStack(alignment: .leading, spacing: 12) {
            Picker("Sandbox", selection: $sandbox) {
                ForEach(model.sandboxes, id: \.name) { Text($0.name).tag($0.name) }
            }
            TextField("Repository (owner/name)", text: $repo).textFieldStyle(.roundedBorder).autocorrectionDisabled()
            Picker("Duration", selection: $minutes) {
                ForEach(GitGrantSheet.windowSteps, id: \.minutes) { Text(L($0.label)).tag($0.minutes) }
            }
            Toggle("Also merge pull requests", isOn: $merge)
            Toggle("Also allow changes to workflow files (.github/workflows)", isOn: $workflows)
            if workflows {
                Text("Workflow files run code on GitHub with the repository's secrets. The assistant could add a step that sends secrets away or deploys. Tick this only if it really has to change workflows, keep the time short and check the result on GitHub.")
                    .font(.caption).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
            }
            Text("For this time the sandbox can push to any branch of the repository (including main, unless GitHub protects it), open pull requests and re-run CI without asking. Merging only with the box ticked. Workflow files only with the box below ticked; repository settings never. The assistant cannot end it; you can revoke it at any time. New full access for the same repository replaces the old one.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if let error { Text(error).foregroundStyle(.red).font(.callout) }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                Button("Open window") {
                    busy = true
                    Task {
                        if let e = await model.openGitWindow(sandbox: sandbox, repo: repo.trimmingCharacters(in: .whitespaces), minutes: minutes, merge: merge, workflows: workflows) { error = e; busy = false } else { dismiss() }
                    }
                }.buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction).disabled(busy || sandbox.isEmpty || repo.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(20)
    }
}
