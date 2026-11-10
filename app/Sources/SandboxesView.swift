import SwiftUI
import AppKit

struct SandboxesView: View {
    @EnvironmentObject var model: AppModel
    @Local private var showCreate = false
    @Local private var addWorktreeFor: SandboxInfo?
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
                            SandboxCard(s: s, addWorktree: { addWorktreeFor = s }, delete: { deleteFor = s })
                        }
                    }.padding(16)
                }
                .transaction { $0.animation = nil }
            }
        }
        .toolbar { Button { showCreate = true } label: { Label("New sandbox", systemImage: "plus") } }
        .sheet(isPresented: $showCreate) { CreateSandboxSheet() }
        .sheet(item: $addWorktreeFor) { s in AddWorktreeSheet(sandbox: s) }
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
    let addWorktree: () -> Void
    let delete: () -> Void
    @Local private var pairLink: String? = nil
    @Local private var pairing = false
    @Local private var showPairSheet = false
    @Local private var pairError: String? = nil

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
            Divider()
            worktreesSection
        }
        .padding(16)
        .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(Color.secondary.opacity(0.18)))
        .sheet(isPresented: $showPairSheet) {
            PairSheet(link: pairLink, pairing: pairing, error: pairError, retry: { pair() })
        }
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
        VStack(alignment: .leading, spacing: 8) {
            Text(L("Runtimes").uppercased()).font(.caption.bold()).foregroundStyle(.secondary)
            HStack(alignment: .top, spacing: 12) {
                ClaudeStatusCard(s: s,
                                 signIn: { report(Terminal.login(vm: s.vm, runtime: "claude")) })
                OpenCodeStatusCard(s: s,
                                   signIn: { report(Terminal.login(vm: s.vm, runtime: "opencode")) },
                                   connect: { pair() }, connecting: pairing,
                                   start: { openCode(true) }, stop: { openCode(false) })
            }
        }
    }

    func openCode(_ start: Bool) {
        let kind = start ? "opencode-start" : "opencode-stop"
        let step = start ? L("Starting the OpenCode service …") : L("Stopping the OpenCode service …")
        let path = start ? "/v1/opencode/start" : "/v1/opencode/stop"
        Task { report(await model.runJob("\(kind)-\(s.name)", step, path, NameBody(name: s.name))) }
    }

    var worktreesSection: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Text(L("Worktrees").uppercased()).font(.caption.bold()).foregroundStyle(.secondary)
                Text("\(s.worktrees.count)").font(.caption).foregroundStyle(.secondary)
                Spacer()
            }
            Text("A worktree is a folder with code. OpenCode opens these folders as projects.")
                .font(.caption2).foregroundStyle(.secondary)
            if s.worktrees.isEmpty {
                Text("No worktrees yet").font(.caption).foregroundStyle(.secondary)
            } else {
                HStack(spacing: 10) {
                    Text(L("Worktree").uppercased()).frame(width: 240, alignment: .leading)
                    Text("Claude").frame(maxWidth: .infinity, alignment: .leading)
                    Text(L("Actions").uppercased()).frame(width: 220, alignment: .trailing)
                }.font(.caption2).foregroundStyle(.secondary)
                ForEach(Array(s.worktrees.enumerated()), id: \.element.name) { i, w in
                    if i > 0 { Divider() }
                    WorktreeRow(s: s, w: w)
                }
            }
            Text("Claude and OpenCode can use the same worktree, but not at the same time on the same files. Use separate worktrees for parallel work.")
                .font(.caption2).foregroundStyle(.secondary).padding(.top, 2)
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
            Button("Worktree +", action: addWorktree)
            Menu {
                Button(L("Shell in the VM (%@)", Terminal.appName)) { report(Terminal.shell(vm: s.vm)) }
                Button("Copy SSH command") {
                    if let c = Terminal.sshCommand(vm: s.vm) { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(c, forType: .string) }
                }
                Divider()
                if s.claudeInstalled != false {
                    Button("Sign in to Claude …") { report(Terminal.login(vm: s.vm, runtime: "claude")) }
                }
                if s.opencodeInstalled != false {
                    Button("Sign in to OpenCode …") { report(Terminal.login(vm: s.vm, runtime: "opencode")) }
                }
                Button(pairing ? L("Preparing the connection …") : L("Connect desktop app …")) { pair() }
                    .disabled(pairing)
            } label: { Label("Shell / SSH", systemImage: "terminal") }
        } else {
            Button("Start") { Task { report(await model.runJob("start-\(s.name)", L("Starting …"), "/v1/sandboxes/start", NameBody(name: s.name))) } }
        }
        Button(role: .destructive, action: delete) { Image(systemName: "trash") }
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

struct ClaudeStatusCard: View {
    let s: SandboxInfo
    let signIn: () -> Void

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
        HStack(alignment: .top, spacing: 10) {
            RuntimeIcon(letter: "C", tint: .purple)
            VStack(alignment: .leading, spacing: 3) {
                Text("Claude Code").font(.callout.weight(.semibold))
                Text(status.0).font(.caption).foregroundStyle(status.1)
                Text("Runs per worktree. Start it in the worktree list below.")
                    .font(.caption2).foregroundStyle(.secondary)
                Text("Remote Control is available for every running Claude.")
                    .font(.caption2).foregroundStyle(.secondary)
            }
            Spacer(minLength: 4)
            if s.running, s.claudeInstalled == true, s.claudeLoggedIn != true {
                Button("Sign in", action: signIn).controlSize(.small)
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.secondary.opacity(0.05), in: RoundedRectangle(cornerRadius: 9))
        .overlay(RoundedRectangle(cornerRadius: 9).stroke(Color.secondary.opacity(0.14)))
    }
}

struct OpenCodeStatusCard: View {
    let s: SandboxInfo
    let signIn: () -> Void
    let connect: () -> Void
    let connecting: Bool
    let start: () -> Void
    let stop: () -> Void

    var installedStatus: (String, Color) {
        if !s.running { return (L("not checked – start the sandbox"), .secondary) }
        switch s.opencodeInstalled {
        case .some(false): return (L("not installed – Connect installs it"), .secondary)
        case .some(true):
            switch s.opencodeLoggedIn {
            case .some(true): return (L("installed · signed in"), .green)
            case .some(false): return (L("installed · not signed in"), .orange)
            default: return (L("installed"), .secondary)
            }
        default: return (L("checking …"), .secondary)
        }
    }

    var serviceRunning: Bool { s.opencodeRunning == true }

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
                    Text(installedStatus.0).font(.caption).foregroundStyle(installedStatus.1)
                    Text("One service for the whole sandbox. Sessions live in the desktop app.")
                        .font(.caption2).foregroundStyle(.secondary)
                    Text("OpenCode opens the worktrees as projects; Claude is not required.")
                        .font(.caption2).foregroundStyle(.secondary)
                }
                Spacer(minLength: 4)
                if s.running, s.opencodeInstalled == true, s.opencodeLoggedIn != true {
                    Button("Sign in", action: signIn).controlSize(.small)
                }
            }
            if s.running {
                HStack(spacing: 8) {
                    Spacer()
                    if connecting {
                        ProgressView().controlSize(.small).help(L("Preparing the connection …"))
                    } else {
                        Button("Connect desktop", action: connect).controlSize(.small)
                    }
                    if s.opencodeInstalled != false {
                        if serviceRunning {
                            Button("Stop service", action: stop).controlSize(.small)
                        } else {
                            Button("Start service", action: start).controlSize(.small)
                        }
                    }
                }
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.secondary.opacity(0.05), in: RoundedRectangle(cornerRadius: 9))
        .overlay(RoundedRectangle(cornerRadius: 9).stroke(Color.secondary.opacity(0.14)))
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
                Text("1. Copy the one-time link below (valid for 5 minutes).\n2. In the desktop app choose \"Add server\" and paste the link.\n3. Open the sandbox's worktrees as projects; Claude is not required.")
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

struct WorktreeRow: View {
    @EnvironmentObject var model: AppModel
    let s: SandboxInfo
    let w: WorktreeInfo
    @Local private var removeOpen = false
    @Local private var forceMsg: String?

    var running: Bool { w.running == true }

    var state: (color: Color, text: String) {
        if !s.running { return (.gray, L("Sandbox stopped")) }
        if running {
            if w.rc { return (.green, L("Claude runs · Remote Control active")) }
            if w.rcAction == "failed" { return (.red, L("RC cannot be activated – please restart")) }
            if w.rcAction == "activating" { return (.orange, L("activating Remote Control …")) }
            if w.rcAction == "waiting" { return (.orange, L("Claude is probably waiting for input – open %@", Terminal.appName)) }
            return (.orange, L("Claude runs · Remote Control starting …"))
        }
        if sandboxBusy { return (.orange, L("starting …")) }
        return (.gray, L("not started"))
    }

    var busy: String? { model.running.first(where: { $0.key.hasSuffix("-\(s.name)-\(w.name)") })?.value }
    /// A job is running for the sandbox (create, start, add worktree …).
    var sandboxBusy: Bool { model.running.keys.contains { $0.hasSuffix(s.name) } }

    var body: some View {
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 1) {
                HStack(spacing: 6) {
                    Circle().fill(state.color).frame(width: 8, height: 8)
                    Text(w.name).font(.callout.bold()).lineLimit(1)
                }
                Text("~/work/\(w.name)").font(.caption2.monospaced()).foregroundStyle(.secondary)
                    .lineLimit(1).truncationMode(.middle)
            }.frame(width: 240, alignment: .leading)
            Text(state.text).font(.caption).foregroundStyle(state.color)
                .lineLimit(1).truncationMode(.tail)
                .frame(maxWidth: .infinity, alignment: .leading)
                .help(state.text)
            actions.frame(width: 220, alignment: .trailing)
        }
        .padding(.vertical, 6)
        .sheet(isPresented: $removeOpen) { RemoveWorktreeSheet(s: s, w: w, onForce: { msg in forceMsg = msg }) }
        .alert("Unsaved work in the worktree", isPresented: Binding(get: { forceMsg != nil }, set: { if !$0 { forceMsg = nil } })) {
            Button("Delete anyway", role: .destructive) { Task { await remove(deleteDir: true, force: true) } }
            Button("Cancel", role: .cancel) {}
        } message: { Text(forceMsg ?? "") }
    }

    @ViewBuilder var actions: some View {
        if let b = busy {
            ProgressView().controlSize(.small)
            Text(b).font(.caption).foregroundStyle(.secondary).lineLimit(1)
        } else {
            HStack(spacing: 8) {
                if running {
                    Button("Stop Claude") { claude(start: false) }.controlSize(.small).disabled(!s.running)
                } else {
                    Button("Start Claude") { claude(start: true) }.controlSize(.small)
                        .buttonStyle(.borderedProminent).disabled(!s.running)
                }
                Button { if let e = Terminal.attach(vm: s.vm, worktree: w.name) { model.setBanner(e) } } label: { Label(Terminal.appName, systemImage: "terminal") }
                    .controlSize(.small).disabled(!s.running || !w.window)
                    .help(L("Opens this worktree's Claude window in %@", Terminal.appName))
                Menu {
                    if w.rc, !w.rcUrl.isEmpty {
                        Button("Copy Remote Control link") { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(w.rcUrl, forType: .string) }
                        Divider()
                    }
                    if running, !w.rc {
                        Button("Remote Control is running – mark as active") {
                            Task {
                                do { try await model.api.post("/v1/worktrees/rc-ok", WorktreeBody(sandbox: s.name, worktree: w.name)); await model.refresh() }
                                catch { model.setBanner(error.localizedDescription) }
                            }
                        }
                    }
                    if running {
                        Button("Stop Claude") { claude(start: false) }.disabled(!s.running)
                    }
                    Button("Restart Claude") { restart() }.disabled(!s.running || !running)
                    Divider()
                    if w.name != "main" {
                        Button("Remove worktree …", role: .destructive) { removeOpen = true }.disabled(!s.running)
                    }
                } label: { Image(systemName: "ellipsis.circle") }
                .menuStyle(.borderlessButton).fixedSize()
            }
        }
    }

    func claude(start: Bool) {
        let kind = start ? "claude-start" : "claude-stop"
        let step = start ? L("starting Claude …") : L("stopping Claude …")
        let path = start ? "/v1/worktrees/start" : "/v1/worktrees/stop"
        Task { if let e = await model.runJob("\(kind)-\(s.name)-\(w.name)", step, path, WorktreeBody(sandbox: s.name, worktree: w.name)) { model.setBanner(e) } }
    }

    func restart() {
        Task { if let e = await model.runJob("claude-restart-\(s.name)-\(w.name)", L("restarting Claude …"), "/v1/worktrees/restart", WorktreeBody(sandbox: s.name, worktree: w.name)) { model.setBanner(e) } }
    }

    func remove(deleteDir: Bool, force: Bool) async {
        let err = await model.runJob("worktreerm-\(s.name)-\(w.name)", L("being removed …"), "/v1/worktrees/remove", WorktreeRemoveBody(sandbox: s.name, worktree: w.name, deleteDir: deleteDir, force: force))
        if let err { model.setBanner(err) }
    }
}

struct RemoveWorktreeSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    let s: SandboxInfo
    let w: WorktreeInfo
    let onForce: (String) -> Void
    @Local private var deleteDir = false

    var body: some View {
        Form {
            Text(L("Remove worktree \"%@\"", w.name)).font(.headline)
            Text("Claude is stopped first. The worktree can be removed with or without its directory.").font(.callout)
            Picker("", selection: $deleteDir) {
                Text(L("Remove the entry only – the directory ~/work/%@ is kept", w.name)).tag(false)
                Text(L("Remove the entry and delete the directory (git worktree including branch worktree/%@, Docker stack %@-%@)", w.name, s.name, w.name)).tag(true)
            }.pickerStyle(.radioGroup).labelsHidden()
            if deleteDir { Text("Unpushed commits and unsaved changes are checked first.").font(.caption).foregroundStyle(.secondary) }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Remove", role: .destructive) {
                    let dd = deleteDir
                    dismiss()
                    Task {
                        let err = await model.runJob("worktreerm-\(s.name)-\(w.name)", L("being removed …"), "/v1/worktrees/remove", WorktreeRemoveBody(sandbox: s.name, worktree: w.name, deleteDir: dd, force: false))
                        if let err { if err.contains("unsaved work") { onForce(err) } else { model.setBanner(err) } }
                    }
                }.keyboardShortcut(.defaultAction)
            }
        }.padding(20).frame(width: 560)
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
            Text("The sandbox starts with the worktree \"main\". Claude is not started automatically; OpenCode runs as a service for the whole sandbox.")
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

struct AddWorktreeSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    let sandbox: SandboxInfo
    @Local private var name = ""
    var valid: Bool { name.range(of: #"^[a-z][a-z0-9-]{0,30}$"#, options: .regularExpression) != nil && name != "main" && !sandbox.worktrees.contains { $0.name == name } }

    var body: some View {
        Form {
            Text(L("New worktree in \"%@\"", sandbox.name)).font(.headline)
            TextField("Worktree name", text: $name)
            Text("A worktree is a folder with code (~/work/<name>). Claude is not started automatically; start it in the worktree list. OpenCode sees the worktree as a project.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Add") {
                    let n = name, s = sandbox.name
                    dismiss()
                    Task { if let e = await model.runJob("worktree-\(s)", L("Worktree %@ is being created …", n), "/v1/worktrees/add", WorktreeBody(sandbox: s, worktree: n)) { model.setBanner(e) } }
                }.disabled(!valid).keyboardShortcut(.defaultAction)
            }
        }.padding(20).frame(width: 500)
    }
}
