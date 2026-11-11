import SwiftUI
import AppKit

struct SandboxesView: View {
    @EnvironmentObject var model: AppModel
    @Local private var showCreate = false
        @Local private var deleteFor: SandboxInfo?
    @Local private var forceDeleteMsg: String?
    @Local private var forceDeleteFor: SandboxInfo?
    @Local private var baseBannerDismissed = false

    var updateBannerText: String {
        guard let st = model.status else { return "" }
        if let size = st.updateSize, size > 0 {
            return L("Update available: version %@ (%@)", st.updateVersion ?? "", formatBytes(size))
        }
        return L("Update available: version %@", st.updateVersion ?? "")
    }

    /// Sandboxes that are being created and are not in the list yet (immediate feedback).
    var creating: [(name: String, step: String)] {
        model.running.compactMap { k, v in
            guard k.hasPrefix("create-") else { return nil }
            let n = String(k.dropFirst("create-".count))
            return model.sandboxes.contains(where: { $0.name == n }) ? nil : (n, v)
        }.sorted { $0.name < $1.name }
    }

    var body: some View {
        VStack(spacing: 0) {
            if let s = model.status, !s.baseReady, !baseBannerDismissed {
                VStack(alignment: .leading, spacing: 6) {
                    HStack(spacing: 10) {
                        Label("Base VM missing – it is built when you create the first sandbox (a few minutes).", systemImage: "info.circle")
                        Spacer()
                        if let step = model.running["base"] {
                            ProgressView().controlSize(.small)
                            Text(step).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                        } else {
                            Button("Build now") {
                                Task { if let e = await model.runJob("base", L("Building base VM (several minutes) …"), "/v1/base/build", NameBody(name: "x")) { model.setBanner(e) } }
                            }.buttonStyle(.borderedProminent).controlSize(.small)
                        }
                        Button { baseBannerDismissed = true } label: { Image(systemName: "xmark") }
                            .buttonStyle(.plain).help(L("Dismiss"))
                    }
                    JobOutput(key: "base")
                }.padding(8).frame(maxWidth: .infinity, alignment: .leading).background(.yellow.opacity(0.15))
            }
            if let st = model.status, st.updateAvailable == true {
                HStack {
                    Label(updateBannerText, systemImage: "arrow.down.circle.fill")
                    Spacer()
                    Button("Open Settings") { model.pane = .settings }
                }.padding(8).frame(maxWidth: .infinity, alignment: .leading).background(.blue.opacity(0.12))
            }
            if model.sandboxes.isEmpty && creating.isEmpty {
                ContentUnavailableView("No sandboxes", systemImage: "shield", description: Text("Create an isolated VM with \"New sandbox\"."))
            } else {
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 14) {
                        ForEach(creating, id: \.name) { c in
                            VStack(alignment: .leading, spacing: 6) {
                                HStack(spacing: 10) {
                                    ProgressView().controlSize(.small)
                                    Text(c.name).font(.headline)
                                    Text(c.step).foregroundStyle(.secondary).lineLimit(1)
                                    Spacer()
                                }
                                JobOutput(key: "create-\(c.name)")
                            }
                            .padding(16)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 12))
                            .overlay(RoundedRectangle(cornerRadius: 12).stroke(Color.secondary.opacity(0.18)))
                        }
                        ForEach(model.sandboxes.sorted { $0.name < $1.name }) { s in
                            SandboxCard(s: s, delete: { deleteFor = s })
                        }
                    }.padding(16)
                }
                .transaction { $0.animation = nil }
            }
        }
        .toolbar { Button { showCreate = true } label: { Label("New sandbox", systemImage: "plus") } }
        .sheet(isPresented: $showCreate) { CreateSandboxSheet() }
                .confirmationDialog(L("Delete sandbox \"%@\"?", deleteFor?.name ?? ""), isPresented: Binding(get: { deleteFor != nil }, set: { if !$0 { deleteFor = nil } })) {
            Button("Delete", role: .destructive) {
                if let s = deleteFor { Task { await delete(s, force: false) } }
            }
        } message: { Text("The VM and its disk are removed. Unpushed commits are checked first.") }
        .alert("Unsaved work in the VM", isPresented: Binding(get: { forceDeleteMsg != nil }, set: { if !$0 { forceDeleteMsg = nil } })) {
            Button("Delete anyway", role: .destructive) { if let s = forceDeleteFor { Task { await delete(s, force: true) } } }
            Button("Cancel", role: .cancel) {}
        } message: { Text(forceDeleteMsg ?? "") }
    }

    func delete(_ s: SandboxInfo, force: Bool) async {
        let err = await model.runJob("del-\(s.name)", L("Deleting %@ …", s.name), "/v1/sandboxes/delete", NameBody(name: s.name, force: force))
        if let err {
            if err.contains("unpushed") { forceDeleteFor = s; forceDeleteMsg = err } else { model.setBanner(err) }
        }
    }
}

struct SandboxCard: View {
    @EnvironmentObject var model: AppModel
    let s: SandboxInfo
    let delete: () -> Void
    var busy: String? { model.running.first(where: { $0.key.hasSuffix(s.name) })?.value }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            header
            meta
            if let k = model.running.keys.first(where: { $0.hasSuffix(s.name) }) { JobOutput(key: k) }
            if s.probeError != nil, s.running {
                Text("VM status currently unavailable (the VM may still be starting).").font(.caption).foregroundStyle(.secondary)
            }
            Divider()
            servicesSection
        }
        .padding(16)
        .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(Color.secondary.opacity(0.18)))
    }

    var header: some View {
        HStack(spacing: 10) {
            Circle().fill(s.running ? .green : .gray).frame(width: 10, height: 10)
            Text(s.name).font(.title3.bold())
            Text(LocalizedStringKey(s.status)).font(.callout).foregroundStyle(.secondary)
            Spacer()
            if let b = busy {
                ProgressView().controlSize(.small)
                Text(b).font(.caption).foregroundStyle(.secondary).lineLimit(1)
            } else {
                actions
            }
        }
    }

    var meta: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 14) {
                Text(s.vm).font(.caption.monospaced())
                Label("\(s.cpus) CPU", systemImage: "cpu")
                Label(formatBytes(s.memory) + " RAM", systemImage: "memorychip")
                diskLabel
                Text(L("Template r%lld", s.templateVersion))
            }
            if s.activeGrants > 0 || s.pendingRequests > 0 {
                HStack(spacing: 14) {
                    if s.activeGrants > 0 { Label(L("%lld SSH grant(s)", s.activeGrants), systemImage: "key.fill").foregroundStyle(.orange) }
                    if s.pendingRequests > 0 { Label(L("%lld open", s.pendingRequests), systemImage: "bell.fill").foregroundStyle(.red) }
                }
            }
        }.font(.caption).foregroundStyle(.secondary)
    }

    var servicesSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            ClaudeServiceCard(s: s)
            OpenCodeServiceCard(s: s)
            Text(L("Claude and OpenCode can use the same folder, but not at the same time on the same files. For parallel work, add several Claude entries with different folders."))
                .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
    }

    @ViewBuilder var diskLabel: some View {
        let host = formatBytes(s.hostDiskUsed)
        Label(L("Disk %@", host), systemImage: "internaldrive")
            .help(L("On the Mac the VM disk uses %@ (sparse, at most %@). Inside the VM %@ are used.", host, formatBytes(s.disk), formatBytes(s.guestDiskUsed)))
    }

    @ViewBuilder var actions: some View {
        if s.running {
            Button("Stop") { Task { report(await model.runJob("stop-\(s.name)", L("Stopping …"), "/v1/sandboxes/stop", NameBody(name: s.name))) } }
            Menu {
                Button(L("Shell in the VM (%@)", Terminal.appName)) { report(Terminal.shell(vm: s.vm)) }
                Button("Copy SSH command") {
                    if let c = Terminal.sshCommand(vm: s.vm) { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(c, forType: .string) }
                }
            } label: { Label("Shell / SSH", systemImage: "terminal") }
        } else {
            Button("Start") { Task { report(await model.runJob("start-\(s.name)", L("Starting …"), "/v1/sandboxes/start", NameBody(name: s.name))) } }
        }
        Button(role: .destructive, action: delete) { Image(systemName: "trash") }
    }

    func report(_ e: String?) { if let e { model.setBanner(e) } }
}

struct RuntimeIcon: View {
    let letter: String
    let tint: Color
    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 8).fill(tint.opacity(0.12)).frame(width: 34, height: 34)
            Text(letter).font(.headline).foregroundStyle(tint)
        }
    }
}

struct ClaudeServiceCard: View {
    @EnvironmentObject var model: AppModel
    let s: SandboxInfo
    @Local private var showStart = false

    var canSignIn: Bool { s.running && s.claudeInstalled == true && s.claudeLoggedIn == false }
    var canAdd: Bool { s.running && s.claudeInstalled == true && s.claudeLoggedIn == true }
    var sessions: [ClaudeInfo] { s.claudeSessions.sorted { $0.folder < $1.folder } }

    var status: (String, Color) {
        if !s.running { return (L("not checked – start the sandbox"), .secondary) }
        switch s.claudeInstalled {
        case .some(false): return (L("not installed"), .secondary)
        case .some(true):
            switch s.claudeLoggedIn {
            case .some(true): return (L("installed · signed in"), .green)
            case .some(false): return (L("installed · not signed in"), .orange)
            default: return (L("installed"), .secondary)
            }
        default: return (L("checking …"), .secondary)
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .top, spacing: 10) {
                RuntimeIcon(letter: "C", tint: .purple)
                VStack(alignment: .leading, spacing: 3) {
                    Text("Claude Code").font(.callout.weight(.semibold))
                    Text(status.0).font(.caption).foregroundStyle(status.1)
                    Text(L("Runs in the folder you choose for this Claude session – no git worktree, no new branch."))
                        .font(.caption2).foregroundStyle(.secondary)
                    Text(L("Remote Control is available for every running Claude."))
                        .font(.caption2).foregroundStyle(.secondary)
                }
                Spacer(minLength: 4)
                if canSignIn {
                    Button("Sign in") { model.setBanner(Terminal.login(vm: s.vm, runtime: "claude")) }.controlSize(.small)
                }
                if canAdd {
                    Button { showStart = true } label: { Label("+  Claude", systemImage: "plus") }
                        .controlSize(.small).buttonStyle(.borderedProminent)
                }
            }
            Divider().padding(.top, 2)
            if sessions.isEmpty {
                Text(L("No Claude sessions yet. Choose “+ Claude” and select a folder."))
                    .font(.caption).foregroundStyle(.secondary)
            } else {
                HStack(spacing: 10) {
                    Text(L("Folder").uppercased()).frame(maxWidth: .infinity, alignment: .leading)
                    Text(L("Status").uppercased()).frame(width: 260, alignment: .leading)
                    Text(L("Actions").uppercased()).frame(width: 230, alignment: .trailing)
                }.font(.caption2).foregroundStyle(.secondary)
                ForEach(Array(sessions.enumerated()), id: \.element.name) { i, w in
                    if i > 0 { Divider() }
                    ClaudeRow(s: s, w: w)
                }
            }
            Text(L("Existing worktrees remain as folders – Bergfried does not create new git worktrees. Removing an entry keeps the folder."))
                .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.secondary.opacity(0.05), in: RoundedRectangle(cornerRadius: 9))
        .overlay(RoundedRectangle(cornerRadius: 9).stroke(Color.secondary.opacity(0.14)))
        .sheet(isPresented: $showStart) { StartClaudeSheet(sandbox: s) }
    }
}

struct ClaudeRow: View {
    @EnvironmentObject var model: AppModel
    let s: SandboxInfo
    let w: ClaudeInfo
    @Local private var removeOpen = false

    var running: Bool { w.running == true }
    var canStart: Bool { s.running && s.claudeInstalled == true && s.claudeLoggedIn == true }
    var label: String { w.folder.hasPrefix("work/") ? String(w.folder.dropFirst(5)) : w.folder }
    var busy: String? { model.running.first(where: { $0.key.hasSuffix("-\(s.name)-\(w.name)") })?.value }
    var sandboxBusy: Bool { model.running.keys.contains { $0.hasSuffix(s.name) } }

    var state: (color: Color, text: String) {
        if !s.running { return (.gray, L("Sandbox stopped")) }
        if running {
            if w.rc { return (.green, L("Claude runs · Remote Control active")) }
            if w.rcAction == "failed" { return (.red, L("Remote Control cannot be activated – please restart")) }
            if w.rcAction == "activating" { return (.orange, L("Activating Remote Control …")) }
            if w.rcAction == "waiting" { return (.orange, L("Claude is probably waiting for input – open %@", Terminal.appName)) }
            return (.orange, L("Claude runs · Remote Control starting …"))
        }
        if s.claudeInstalled == false { return (.gray, L("Claude is not installed")) }
        if s.claudeLoggedIn != true { return (.orange, L("Claude is not signed in")) }
        if sandboxBusy { return (.orange, L("starting …")) }
        return (.gray, L("not started"))
    }

    var body: some View {
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 1) {
                HStack(spacing: 6) {
                    Circle().fill(state.color).frame(width: 8, height: 8)
                    Text(label).font(.callout.bold()).lineLimit(1)
                }
                Text("~/"+w.folder).font(.caption2.monospaced()).foregroundStyle(.secondary)
                    .lineLimit(1).truncationMode(.middle)
            }.frame(maxWidth: .infinity, alignment: .leading)
            Text(state.text).font(.caption).foregroundStyle(state.color)
                .lineLimit(1).truncationMode(.tail)
                .frame(width: 260, alignment: .leading).help(state.text)
            actions.frame(width: 230, alignment: .trailing)
        }
        .padding(.vertical, 4)
        .confirmationDialog(L("Remove Claude entry for “%@”?", label), isPresented: $removeOpen) {
            Button(L("Remove entry"), role: .destructive) { remove() }
            Button(L("Cancel"), role: .cancel) {}
        } message: {
            Text(L("Claude is stopped. The folder ~/%@ and its contents remain.", w.folder))
        }
    }

    @ViewBuilder var actions: some View {
        if let b = busy {
            ProgressView().controlSize(.small)
            Text(b).font(.caption).foregroundStyle(.secondary).lineLimit(1)
        } else {
            HStack(spacing: 8) {
                if running {
                    Button("Stop Claude") { job("stop", L("Stopping Claude …"), "/v1/claude/stop") }.controlSize(.small)
                } else if canStart {
                    Button("Start Claude") { job("start", L("Starting Claude …"), "/v1/claude/start") }
                        .controlSize(.small).buttonStyle(.borderedProminent)
                }
                if running && w.window {
                    Button { if let e = Terminal.attach(vm: s.vm, window: w.name) { model.setBanner(e) } } label: { Label(Terminal.appName, systemImage: "terminal") }
                        .controlSize(.small)
                        .help(L("Opens the Claude window for ~/%@ in %@", w.folder, Terminal.appName))
                }
                if s.running {
                    Menu {
                    if running, w.rc, !w.rcUrl.isEmpty {
                        Button("Copy Remote Control link") { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(w.rcUrl, forType: .string) }
                        Divider()
                    }
                    if running, !w.rc {
                        Button(L("Remote Control is running – mark as active")) {
                            Task {
                                do { try await model.api.post("/v1/claude/rc-ok", ClaudeBody(sandbox: s.name, folder: w.folder)); await model.refresh() }
                                catch { model.setBanner(error.localizedDescription) }
                            }
                        }
                    }
                    if running {
                        Button(L("Restart Claude")) { job("restart", L("Restarting Claude …"), "/v1/claude/restart") }
                    }
                    if s.running {
                        Divider()
                        Button(L("Remove Claude entry …"), role: .destructive) { removeOpen = true }
                    }
                    } label: { Image(systemName: "ellipsis.circle") }
                    .menuStyle(.borderlessButton).fixedSize()
                }
            }
        }
    }

    func job(_ action: String, _ step: String, _ path: String) {
        Task { if let e = await model.runJob("claude-\(action)-\(s.name)-\(w.name)", step, path, ClaudeBody(sandbox: s.name, folder: w.folder)) { model.setBanner(e) } }
    }

    func remove() {
        Task { if let e = await model.runJob("claude-remove-\(s.name)-\(w.name)", L("Removing entry …"), "/v1/claude/remove", ClaudeRemoveBody(sandbox: s.name, folder: w.folder)) { model.setBanner(e) } }
    }
}

struct OpenCodeServiceCard: View {
    @EnvironmentObject var model: AppModel
    let s: SandboxInfo
    @Local private var pairLink: String? = nil
    @Local private var pairing = false
    @Local private var showPairSheet = false
    @Local private var pairError: String? = nil

    var serviceRunning: Bool { s.opencodeRunning == true }
    var canSignIn: Bool { s.running && s.opencodeInstalled == true && s.opencodeLoggedIn == false }

    var status: (String, Color) {
        if !s.running { return (L("not checked – start the sandbox"), .secondary) }
        switch s.opencodeInstalled {
        case .some(false): return (L("not installed – Start service installs it"), .secondary)
        case .some(true):
            switch s.opencodeLoggedIn {
            case .some(true): return (L("installed · signed in"), .green)
            case .some(false): return (L("installed · not signed in"), .orange)
            default: return (L("installed"), .secondary)
            }
        default: return (L("checking …"), .secondary)
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .top, spacing: 10) {
                RuntimeIcon(letter: "O", tint: .blue)
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 6) {
                        Text("OpenCode").font(.callout.weight(.semibold))
                        if s.running {
                            Circle().fill(serviceRunning ? .green : Color.secondary).frame(width: 6, height: 6)
                            Text(serviceRunning ? L("service running") : L("service stopped"))
                                .font(.caption2).foregroundStyle(serviceRunning ? .green : .secondary)
                        }
                    }
                    Text(status.0).font(.caption).foregroundStyle(status.1)
                    Text(L("One service for the whole sandbox. Opens the folders as projects in the desktop app."))
                        .font(.caption2).foregroundStyle(.secondary)
                }
                Spacer(minLength: 4)
                if canSignIn {
                    Button("Sign in") { model.setBanner(Terminal.login(vm: s.vm, runtime: "opencode")) }.controlSize(.small)
                }
            }
            if s.running {
                HStack(spacing: 8) {
                    Spacer()
                    if serviceRunning {
                        if pairing {
                            ProgressView().controlSize(.small).help(L("Preparing the connection …"))
                        } else {
                            Button("Connect desktop") { pair() }.controlSize(.small)
                        }
                        Button("Stop service") { model.setBanner(nil); Task { if let e = await model.runJob("opencode-stop-\(s.name)", L("Stopping the OpenCode service …"), "/v1/opencode/stop", NameBody(name: s.name)) { model.setBanner(e) } } }.controlSize(.small)
                    } else {
                        Button("Start service") { Task { if let e = await model.runJob("opencode-start-\(s.name)", L("Starting the OpenCode service …"), "/v1/opencode/start", NameBody(name: s.name)) { model.setBanner(e) } } }.controlSize(.small)
                    }
                }
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.secondary.opacity(0.05), in: RoundedRectangle(cornerRadius: 9))
        .overlay(RoundedRectangle(cornerRadius: 9).stroke(Color.secondary.opacity(0.14)))
        .sheet(isPresented: $showPairSheet) {
            PairSheet(link: pairLink, pairing: pairing, error: pairError, retry: { pair() })
        }
    }

    func pair() {
        pairLink = nil
        pairError = nil
        pairing = true
        showPairSheet = true
        Task {
            do {
                let r = try await model.api.send("POST", "/v1/sandboxes/pair", NameBody(name: s.name), as: PairLink.self)
                pairLink = r.url
            } catch { pairError = error.localizedDescription }
            pairing = false
        }
    }
}

struct PairSheet: View {
    let link: String?
    let pairing: Bool
    let error: String?
    let retry: () -> Void
    @Environment(\.dismiss) var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Connect the OpenCode desktop app").font(.title3.bold())
            if pairing {
                HStack(spacing: 10) {
                    ProgressView().controlSize(.small)
                    Text(L("Preparing the connection …")).font(.callout)
                }
                Text("The OpenCode service is restarted on this sandbox's own port and a one-time link is created. This can take a few seconds; sessions in the desktop app continue.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                HStack {
                    Spacer()
                    Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                }
            } else if let error {
                Label(error, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.red).font(.callout)
                    .fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
                HStack {
                    Button("Try again", action: retry).buttonStyle(.borderedProminent)
                    Spacer()
                    Button("Close") { dismiss() }.keyboardShortcut(.cancelAction)
                }
            } else if let link {
                Text("1. Copy the one-time link below (valid for 5 minutes).\n2. In the desktop app choose \"Add server\" and paste the link.\n3. Open the sandbox's folders as projects; Claude is not required.")
                    .font(.callout).fixedSize(horizontal: false, vertical: true)
                Text(link)
                    .font(.caption.monospaced())
                    .textSelection(.enabled)
                    .lineLimit(4)
                    .padding(8).frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color.secondary.opacity(0.1), in: RoundedRectangle(cornerRadius: 6))
                if let port = URL(string: link)?.port {
                    Text(L("Port %lld – this link belongs to this sandbox. A link on port 49374 is not from Bergfried; click Connect again.", port))
                        .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
                Text("Use the link immediately and only once. If you want the desktop app, do not open it in the browser first.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                Text("The link only works on this Mac and cannot be used from the internet or your local network. If it expired, close this window and connect again.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                HStack {
                    Button("Copy link") {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(link, forType: .string)
                    }
                    Button("Open in browser") {
                        if let u = URL(string: link) { NSWorkspace.shared.open(u) }
                    }
                    Spacer()
                    Button("Done") { dismiss() }.keyboardShortcut(.defaultAction)
                }
            } else {
                Text("No link received. Please try again.")
                    .font(.callout).foregroundStyle(.secondary)
                HStack {
                    Button("Try again", action: retry).buttonStyle(.borderedProminent)
                    Spacer()
                    Button("Close") { dismiss() }.keyboardShortcut(.cancelAction)
                }
            }
        }.padding(20).frame(width: 560)
    }
}

struct StartClaudeSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    let sandbox: SandboxInfo
    @Local private var folders: [FolderEntry] = []
    @Local private var selected: String?
    @Local private var custom = ""
    @Local private var loading = true
    @Local private var loadError: String?

    var chosen: String {
        let c = custom.trimmingCharacters(in: .whitespaces)
        return c.isEmpty ? (selected ?? "") : c
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(L("Start Claude")).font(.title3.bold())
            Text(L("Sandbox %@", sandbox.name)).font(.callout).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 4) {
                Text(L("Choose folder")).font(.callout.bold())
                Text(L("Claude works directly in this folder – no git worktree, no new branch."))
                    .font(.caption).foregroundStyle(.secondary)
            }
            if loading {
                HStack(spacing: 8) { ProgressView().controlSize(.small); Text(L("Loading folders …")).font(.caption).foregroundStyle(.secondary) }
            } else if folders.isEmpty {
                Text(L("No folders under ~/work found. Enter a path below."))
                    .font(.caption).foregroundStyle(.secondary)
            } else {
                ScrollView {
                    VStack(spacing: 6) {
                        ForEach(folders, id: \.folder) { f in
                            Button {
                                selected = f.folder; custom = ""
                            } label: {
                                HStack(spacing: 10) {
                                    Image(systemName: selected == f.folder ? "largecircle.fill.circle" : "circle")
                                        .foregroundStyle(selected == f.folder ? Color.accentColor : Color.secondary)
                                    VStack(alignment: .leading, spacing: 1) {
                                        Text(f.label.isEmpty ? f.folder : f.label).font(.callout.weight(.semibold))
                                        Text("~/"+f.folder).font(.caption.monospaced()).foregroundStyle(.secondary)
                                    }
                                    Spacer()
                                }
                                .contentShape(Rectangle())
                                .padding(8)
                                .background(selected == f.folder ? Color.accentColor.opacity(0.10) : Color.secondary.opacity(0.04), in: RoundedRectangle(cornerRadius: 7))
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }.frame(maxHeight: 190)
            }
            VStack(alignment: .leading, spacing: 4) {
                Text(L("Other folder")).font(.caption.bold()).foregroundStyle(.secondary)
                TextField("~/work/…", text: $custom).textFieldStyle(.roundedBorder)
            }
            Text(L("Claude continues the last session in this folder if there is one. The folder remains when the entry is removed."))
                .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if let loadError { Text(loadError).foregroundStyle(.red).font(.caption).textSelection(.enabled) }
            HStack {
                Spacer()
                Button(L("Cancel")) { dismiss() }.keyboardShortcut(.cancelAction)
                Button(L("Start Claude")) {
                    let folder = chosen, name = sandbox.name
                    dismiss()
                    Task { if let e = await model.runJob("claude-\(name)", L("Starting Claude …"), "/v1/claude/start", ClaudeBody(sandbox: name, folder: folder)) { model.setBanner(e) } }
                }.disabled(chosen.isEmpty).keyboardShortcut(.defaultAction)
            }
        }
        .padding(20).frame(width: 560)
        .task {
            do {
                let r: FoldersResponse = try await model.api.get("/v1/claude/folders?sandbox=\(sandbox.name)")
                folders = r.folders
                selected = r.folders.first?.folder
            } catch { loadError = error.localizedDescription }
            loading = false
        }
    }
}

struct CreateSandboxSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    @Local private var name = ""
    @Local private var source = 0            // 0 empty, 1 git repo, 2 folder
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
            Text("New sandbox").font(.title3.bold())
            VStack(alignment: .leading, spacing: 4) {
                Text("Name").font(.callout.bold())
                TextField("e.g. shop", text: $name).textFieldStyle(.roundedBorder)
                Text("a–z, 0–9 and -, at most 24 characters").font(.caption).foregroundStyle(.secondary)
            }
            Text("The sandbox starts with the folder \"main\". Claude is not started automatically; OpenCode runs as a service for the whole sandbox.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            VStack(alignment: .leading, spacing: 6) {
                Text("Code source").font(.callout.bold())
                Picker("Code source", selection: $source) {
                    Text("Empty").tag(0)
                    Text("Git repo").tag(1)
                    Text("Folder").tag(2)
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
                            Text(folder.isEmpty ? L("No folder chosen") : folder).lineLimit(1).truncationMode(.middle).foregroundStyle(folder.isEmpty ? .secondary : .primary)
                            Spacer()
                            Button("Choose folder …") { chooseFolder() }
                        }
                        if let p = preview { Label(L("%lld files, %@ will be copied", p.files, formatBytes(p.bytes)), systemImage: "doc.on.doc").font(.callout) }
                        if let e = previewError { Label(e, systemImage: "xmark.octagon.fill").foregroundStyle(.red).font(.callout).fixedSize(horizontal: false, vertical: true) }
                        LT("The folder is **copied**, not mounted: the VM never sees your home. Not copied: blocklist entries, .env files, keys, symlinks, node_modules and caches. Credentials in .git/config are removed. For git the assistant will need a GitHub token later (it asks for it).")
                            .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    }.frame(maxWidth: .infinity, alignment: .leading).padding(4)
                }
            }
            Text("The VM disk is at ~/.lima/bergfried-<name> (code in the VM at ~/work/main), the exchange folders at ~/.bergfried/sandboxes/<name>/shared/{in,out}.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Create") {
                    let n = name, r = source == 1 ? repo : "", f = source == 2 ? folder : ""
                    dismiss()
                    Task { if let e = await model.runJob("create-\(n)", L("%@ is being created …", n), "/v1/sandboxes/create", NameBody(name: n, repo: r, importPath: f, githubToken: source == 1 ? token : "")) { model.setBanner(e) } }
                }.disabled(!valid || !sourceOK).keyboardShortcut(.defaultAction)
            }
        }.padding(20).frame(width: 560).fixedSize(horizontal: false, vertical: true)
        .task(id: name) {
            // Pre-fill the link with the sandbox name (the token is then named bergfried-<name>)
            let q = valid ? name : "new"
            if let l: GitHubLink = try? await model.api.get("/v1/github/link?sandbox=\(q)") { link = l.url; hint = l.hint }
        }
    }

    func chooseFolder() {
        let p = NSOpenPanel()
        p.canChooseDirectories = true; p.canChooseFiles = false; p.allowsMultipleSelection = false
        p.directoryURL = URL(fileURLWithPath: NSHomeDirectory())
        p.prompt = L("Choose")
        p.message = L("Folder in your home directory that should be copied into the VM")
        guard p.runModal() == .OK, let url = p.url else { return }
        folder = url.path; preview = nil; previewError = nil
        Task {
            do { preview = try await model.api.send("POST", "/v1/import/preview", PathBody(path: url.path), as: ImportPreview.self) }
            catch { previewError = error.localizedDescription }
        }
    }
}
