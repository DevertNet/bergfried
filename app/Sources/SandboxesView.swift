import SwiftUI
import AppKit

struct SandboxesView: View {
    @EnvironmentObject var model: AppModel
    @Local private var showCreate = false
    @Local private var addAgentFor: SandboxInfo?
    @Local private var deleteFor: SandboxInfo?
    @Local private var forceDeleteMsg: String?
    @Local private var forceDeleteFor: SandboxInfo?

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
            if let s = model.status, !s.baseReady {
                Label("Base VM missing – it is built when you create the first sandbox (a few minutes).", systemImage: "info.circle")
                    .padding(8).frame(maxWidth: .infinity, alignment: .leading).background(.yellow.opacity(0.15))
            }
            if model.sandboxes.isEmpty && creating.isEmpty {
                ContentUnavailableView("No sandboxes", systemImage: "shield", description: Text("Create an isolated VM with \"New sandbox\"."))
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
                .transaction { $0.animation = nil }   // rows should not animate in when they appear (it looked cut off at the top)
            }
        }
        .toolbar { Button { showCreate = true } label: { Label("New sandbox", systemImage: "plus") } }
        .sheet(isPresented: $showCreate) { CreateSandboxSheet() }
        .sheet(item: $addAgentFor) { s in AddAgentSheet(sandbox: s) }
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
                if s.activeGrants > 0 { Label(L("%lld SSH grant(s)", s.activeGrants), systemImage: "key.fill").foregroundStyle(.orange) }
                if s.pendingRequests > 0 { Label(L("%lld open", s.pendingRequests), systemImage: "bell.fill").foregroundStyle(.red) }
                Text(L("Template r%lld", s.templateVersion))
            }.font(.caption).foregroundStyle(.secondary)
            if s.probeError != nil, s.running { Text("VM status currently unavailable (the VM may still be starting).").font(.caption).foregroundStyle(.secondary) }
            if !s.agents.isEmpty {
                // Agents belong to the sandbox: indented, with a guide line and their own panel.
                HStack(alignment: .top, spacing: 0) {
                    RoundedRectangle(cornerRadius: 1).fill(Color.accentColor.opacity(0.5)).frame(width: 3).padding(.leading, 4)
                    VStack(alignment: .leading, spacing: 0) {
                        Label(L("Agents (%lld)", s.agents.count), systemImage: "person.2.fill").font(.caption.bold()).foregroundStyle(.secondary).padding(.bottom, 4)
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
            Label("Claude logged in", systemImage: "checkmark.seal.fill").font(.caption).foregroundStyle(.green)
        case .some(false):
            Button { report(Terminal.login(vm: s.vm)) } label: { Label("Claude not logged in – log in now", systemImage: "person.crop.circle.badge.exclamationmark") }
                .buttonStyle(.borderedProminent).tint(.orange).controlSize(.small)
        default:
            Label("Checking Claude status …", systemImage: "hourglass").font(.caption).foregroundStyle(.secondary)
        }
    }

    @ViewBuilder var diskLabel: some View {
        let host = formatBytes(s.hostDiskUsed)
        if s.guestDiskSize > 0 {
            Label(L("Disk: %@ used (in the VM %@ of %@)", host, formatBytes(s.guestDiskUsed), formatBytes(s.guestDiskSize)), systemImage: "internaldrive")
                .help(L("On the Mac the VM disk uses %@ (sparse, at most %@). Inside the VM %@ are used.", host, formatBytes(s.disk), formatBytes(s.guestDiskUsed)))
        } else {
            Label(L("Disk: %@ used (max. %@)", host, formatBytes(s.disk)), systemImage: "internaldrive")
        }
    }

    @ViewBuilder var actions: some View {
        if s.running {
            Button("Stop") { Task { report(await model.runJob("stop-\(s.name)", L("Stopping …"), "/v1/sandboxes/stop", NameBody(name: s.name))) } }
            Button("Agent +", action: addAgent)
            Menu {
                Button(L("Shell in the VM (%@)", Terminal.appName)) { report(Terminal.shell(vm: s.vm)) }
                Button("Copy SSH command") {
                    if let c = Terminal.sshCommand(vm: s.vm) { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(c, forType: .string) }
                }
                Divider()
                Button("Claude login (once per VM)") { report(Terminal.login(vm: s.vm)) }
            } label: { Label("Shell / SSH", systemImage: "terminal") }
        } else {
            Button("Start") { Task { report(await model.runJob("start-\(s.name)", L("Starting …"), "/v1/sandboxes/start", NameBody(name: s.name))) } }
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
        if a.rc { return (.green, L("Remote Control active")) }
        if !a.window && sandboxBusy { return (.orange, L("starting …")) }
        if s.claudeLoggedIn == false { return (.orange, L("waiting for Claude login")) }
        if a.rcAction == "failed" { return (.red, L("RC cannot be activated – please restart")) }
        if a.rcAction == "activating" { return (.orange, L("activating Remote Control …")) }
        if a.rcAction == "waiting" { return (.orange, L("Claude is probably waiting for input – open tmux")) }
        if !a.window { return (.gray, L("no tmux window")) }
        return (.orange, L("Remote Control starting …"))
    }

    var busy: String? { model.running.first(where: { $0.key.hasSuffix("-\(s.name)-\(a.name)") })?.value }
    /// A job is running for the sandbox (create, start, add agent …).
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
                    Button { NSWorkspace.shared.open(url) } label: { Label("Open RC link", systemImage: "arrow.up.right.square") }.controlSize(.small)
                }
                Button { if let e = Terminal.attach(vm: s.vm, agent: a.name) { model.setBanner(e) } } label: { Label(L("%@: Claude window", Terminal.appName), systemImage: "terminal") }
                    .controlSize(.small).disabled(!s.running || !a.window)
                    .help(L("Opens this agent's tmux window in %@", Terminal.appName))
                Menu {
                    if a.rc, !a.rcUrl.isEmpty {
                        Button("Copy link") { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(a.rcUrl, forType: .string) }
                        Divider()
                    }
                    if !a.rc {
                        Button("Remote Control is running – mark as active") {
                            Task {
                                do { try await model.api.post("/v1/agents/rc-ok", AgentBody(sandbox: s.name, agent: a.name)); await model.refresh() }
                                catch { model.setBanner(error.localizedDescription) }
                            }
                        }.disabled(!s.running || !a.window)
                    }
                    Button("Restart") {
                        Task { if let e = await model.runJob("restart-\(s.name)-\(a.name)", L("restarting …"), "/v1/agents/restart", AgentBody(sandbox: s.name, agent: a.name)) { model.setBanner(e) } }
                    }.disabled(!s.running)

                    Divider()
                    Button("Remove …", role: .destructive) { removeOpen = true }.disabled(!s.running)
                } label: { Image(systemName: "ellipsis.circle") }
                .menuStyle(.borderlessButton).fixedSize()
            }
        }
        .padding(.vertical, 5)
        .sheet(isPresented: $removeOpen) { RemoveAgentSheet(s: s, a: a, onForce: { msg in forceMsg = msg }) }
        .alert("Unsaved work in the agent directory", isPresented: Binding(get: { forceMsg != nil }, set: { if !$0 { forceMsg = nil } })) {
            Button("Delete anyway", role: .destructive) { Task { await remove(deleteDir: true, force: true) } }
            Button("Cancel", role: .cancel) {}
        } message: { Text(forceMsg ?? "") }
    }

    func remove(deleteDir: Bool, force: Bool) async {
        let err = await model.runJob("agentrm-\(s.name)-\(a.name)", L("being removed …"), "/v1/agents/remove", AgentRemoveBody(sandbox: s.name, agent: a.name, deleteDir: deleteDir, force: force))
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
            Text(L("Remove agent \"%@\"", a.name)).font(.headline)
            Text("The tmux window with Claude (and its Remote Control session) is terminated.").font(.callout)
            Picker("", selection: $deleteDir) {
                Text(L("Terminate the agent only – the directory %@ is kept", a.worktree)).tag(false)
                if a.name != "main" { Text(L("Terminate the agent and delete its directory (git worktree including branch agent/%@, Docker stack %@-%@)", a.name, s.name, a.name)).tag(true) }
            }.pickerStyle(.radioGroup).labelsHidden()
            if a.name == "main" { Text("The main directory \"main\" is never deleted.").font(.caption).foregroundStyle(.secondary) }
            if deleteDir { Text("Unpushed commits and unsaved changes are checked first.").font(.caption).foregroundStyle(.secondary) }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Remove", role: .destructive) {
                    let dd = deleteDir
                    dismiss()
                    Task {
                        let err = await model.runJob("agentrm-\(s.name)-\(a.name)", L("being removed …"), "/v1/agents/remove", AgentRemoveBody(sandbox: s.name, agent: a.name, deleteDir: dd, force: false))
                        if let err { if err.contains("unsaved work") { onForce(err) } else { model.setBanner(err) } }
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
                        LT("The folder is **copied**, not mounted: the VM never sees your home. Not copied: blocklist entries, .env files, keys, symlinks, node_modules and caches. Credentials in .git/config are removed. For git the agent will need a GitHub token later (it asks for it).")
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

struct AddAgentSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    let sandbox: SandboxInfo
    @Local private var name = ""
    var valid: Bool { name.range(of: #"^[a-z][a-z0-9-]{0,30}$"#, options: .regularExpression) != nil }

    var body: some View {
        Form {
            Text(L("New agent in \"%@\"", sandbox.name)).font(.headline)
            TextField("Agent name", text: $name)
            Text("Each agent gets a tmux window, a Remote Control session and a git worktree.").font(.caption).foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Add") {
                    let n = name, s = sandbox.name
                    dismiss()
                    Task { if let e = await model.runJob("agent-\(s)", L("Agent %@ is starting …", n), "/v1/agents/add", AgentBody(sandbox: s, agent: n)) { model.setBanner(e) } }
                }.disabled(!valid).keyboardShortcut(.defaultAction)
            }
        }.padding(20).frame(width: 440)
    }
}
