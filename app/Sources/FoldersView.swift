import SwiftUI
import AppKit

/// One row of the folders card: a folder below ~/work (item) and/or a Claude entry for it.
/// A Claude entry can also point to a nested folder that is not listed as a top-level folder; then `item` is nil.
struct FolderRowData: Identifiable {
    var folder: String
    var item: FolderItem?
    var w: ClaudeInfo?
    var id: String { folder }
    var label: String { folder.hasPrefix("work/") ? String(folder.dropFirst(5)) : folder }
    var base: String { folder.split(separator: "/").last.map(String.init) ?? folder }
}

/// All folders of the sandbox's ~/work – with or without Claude – plus creating, copying, moving and deleting them.
struct FoldersCard: View {
    @EnvironmentObject var model: AppModel
    let s: SandboxInfo
    @Local private var items: [FolderItem]? = nil
    @Local private var loadError: String? = nil
    @Local private var showStart = false
    @Local private var showNew = false

    var reloadKey: String { "\(s.running)|\(s.claudeSessions.map(\.folder).joined(separator: ","))|\(model.running.count)" }

    var rows: [FolderRowData] {
        var res: [FolderRowData] = []
        var seen = Set<String>()
        for i in items ?? [] {
            seen.insert(i.folder)
            res.append(FolderRowData(folder: i.folder, item: i, w: s.claudeSessions.first { $0.folder == i.folder }))
        }
        for w in s.claudeSessions where !seen.contains(w.folder) {
            res.append(FolderRowData(folder: w.folder, item: nil, w: w))
        }
        return res.sorted { $0.folder < $1.folder }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .top, spacing: 10) {
                ZStack {
                    RoundedRectangle(cornerRadius: 8).fill(Color.blue.opacity(0.12)).frame(width: 34, height: 34)
                    Image(systemName: "folder.fill").foregroundStyle(.blue)
                }
                VStack(alignment: .leading, spacing: 3) {
                    Text(L("Folders")).font(.callout.weight(.semibold))
                    Text(L("Everything in ~/work of this VM. Claude can run in any folder; copying and moving folders to another sandbox is done here."))
                        .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
                Spacer(minLength: 4)
                if s.running {
                    Button { Task { await load() } } label: { Image(systemName: "arrow.clockwise") }
                        .buttonStyle(.borderless).help(L("Reload the folder list"))
                    Menu {
                        Button(L("New folder …")) { showNew = true }
                        if s.claudeInstalled == true && s.claudeLoggedIn == true {
                            Divider()
                            Button(L("Start Claude in another folder …")) { showStart = true }
                        }
                    } label: { Label(L("Folder"), systemImage: "plus") }
                        .menuStyle(.borderedButton).controlSize(.small).fixedSize()
                }
            }
            Divider().padding(.top, 2)
            if !s.running {
                Text(L("Start the sandbox to see all folders in ~/work."))
                    .font(.caption).foregroundStyle(.secondary)
            } else if let loadError {
                Text(loadError).font(.caption).foregroundStyle(.red).textSelection(.enabled)
            } else if items == nil {
                HStack(spacing: 8) { ProgressView().controlSize(.small); Text(L("Loading folders …")).font(.caption).foregroundStyle(.secondary) }
            } else if rows.isEmpty {
                Text(L("No folders in ~/work yet. Create one with “+ Folder”.")).font(.caption).foregroundStyle(.secondary)
            }
            if !rows.isEmpty {
                HStack(spacing: 10) {
                    Text(L("Folder").uppercased()).frame(maxWidth: .infinity, alignment: .leading)
                    Text(L("Claude").uppercased()).frame(width: 260, alignment: .leading)
                    Text(L("Actions").uppercased()).frame(width: 230, alignment: .trailing)
                }.font(.caption2).foregroundStyle(.secondary)
                ForEach(Array(rows.enumerated()), id: \.element.id) { i, r in
                    if i > 0 { Divider() }
                    FolderRow(s: s, r: r)
                }
            }
            Text(L("Claude works directly in the folder – no git worktree, no new branch. Removing a Claude entry keeps the folder."))
                .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.secondary.opacity(0.05), in: RoundedRectangle(cornerRadius: 9))
        .overlay(RoundedRectangle(cornerRadius: 9).stroke(Color.secondary.opacity(0.14)))
        .sheet(isPresented: $showStart) { StartClaudeSheet(sandbox: s) }
        .sheet(isPresented: $showNew) { NewFolderSheet(s: s) }
        .task(id: reloadKey) { await load() }
    }

    func load() async {
        guard s.running else { items = nil; loadError = nil; return }
        do {
            let r: FolderListResponse = try await model.api.get("/v1/folders?sandbox=\(s.name)")
            items = r.folders
            loadError = nil
        } catch { loadError = error.localizedDescription }
    }
}

struct FolderRow: View {
    @EnvironmentObject var model: AppModel
    let s: SandboxInfo
    let r: FolderRowData
    @Local private var removeOpen = false
    @Local private var deleteOpen = false
    @Local private var forceMsg: String? = nil
    @Local private var transfer: Bool? = nil   // nil closed, false copy, true move

    var w: ClaudeInfo? { r.w }
    var running: Bool { w?.running == true }
    var canStart: Bool { s.running && s.claudeInstalled == true && s.claudeLoggedIn == true }
    var busy: String? {
        model.jobs(of: s.name).first(where: {
            let item = $0.key.split(separator: "|", omittingEmptySubsequences: false).last.map(String.init)
            return item == r.base || (w != nil && item == w!.name)
        })?.label
    }
    var sandboxBusy: Bool { !model.jobs(of: s.name).isEmpty }

    var state: (color: Color, text: String) {
        if !s.running { return (.gray, L("Sandbox stopped")) }
        guard let w else {
            if s.claudeInstalled == false { return (.gray, L("Claude is not installed")) }
            if s.claudeLoggedIn != true { return (.orange, L("Claude is not signed in")) }
            return (.gray, L("no Claude"))
        }
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
        return (.gray, L("Claude not started"))
    }

    var gitLine: String? {
        guard let i = r.item, i.git else { return nil }
        let b = i.branch.isEmpty ? L("git") : L("git: %@", i.branch)
        return i.dirty ? b + " · " + L("uncommitted changes") : b
    }

    var body: some View {
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 1) {
                HStack(spacing: 6) {
                    Circle().fill(state.color).frame(width: 8, height: 8)
                    Image(systemName: "folder").foregroundStyle(.secondary).font(.caption)
                    Text(r.label).font(.callout.bold()).lineLimit(1)
                    if running, let rr = w?.restartReasons, !rr.isEmpty { RestartNeededLabel(help: restartHelp(tool: "Claude", reasons: rr, footer: L("Use “Restart Claude” in the menu; the conversation continues."))) }
                }
                HStack(spacing: 8) {
                    Text("~/"+r.folder).font(.caption2.monospaced()).foregroundStyle(.secondary)
                        .lineLimit(1).truncationMode(.middle)
                    if let g = gitLine {
                        Text(g).font(.caption2).foregroundStyle(r.item?.dirty == true ? Color.orange : Color.secondary).lineLimit(1)
                    }
                }
            }.frame(maxWidth: .infinity, alignment: .leading)
            Text(state.text).font(.caption).foregroundStyle(state.color)
                .lineLimit(1).truncationMode(.tail)
                .frame(width: 260, alignment: .leading).help(state.text)
            actions.frame(width: 230, alignment: .trailing)
        }
        .padding(.vertical, 4)
        .confirmationDialog(L("Remove Claude entry for “%@”?", r.label), isPresented: $removeOpen) {
            Button(L("Remove entry"), role: .destructive) { removeEntry() }
            Button(L("Cancel"), role: .cancel) {}
        } message: {
            Text(L("Claude is stopped. The folder ~/%@ and its contents remain.", r.folder))
        }
        .confirmationDialog(L("Delete folder “%@”?", r.label), isPresented: $deleteOpen) {
            Button(L("Delete folder"), role: .destructive) { deleteFolder(force: false) }
            Button(L("Cancel"), role: .cancel) {}
        } message: {
            Text(L("~/%@ and everything in it is removed from the VM. This cannot be undone. A Claude entry for the folder is removed, too. Unpushed git work is checked first.", r.folder))
        }
        .alert(L("Unsaved work in the folder"), isPresented: Binding(get: { forceMsg != nil }, set: { if !$0 { forceMsg = nil } })) {
            Button(L("Delete anyway"), role: .destructive) { deleteFolder(force: true) }
            Button(L("Cancel"), role: .cancel) {}
        } message: { Text(forceMsg ?? "") }
        .sheet(isPresented: Binding(get: { transfer != nil }, set: { if !$0 { transfer = nil } })) {
            TransferFolderSheet(s: s, folder: r.folder, label: r.label, move: transfer ?? false)
        }
    }

    @ViewBuilder var actions: some View {
        if let b = busy {
            ProgressView().controlSize(.small)
            Text(b).font(.caption).foregroundStyle(.secondary).lineLimit(1)
        } else {
            HStack(spacing: 8) {
                if running {
                    Button(L("Stop Claude")) { claudeJob("stop", L("Stopping Claude …"), "/v1/claude/stop") }.controlSize(.small)
                } else if canStart {
                    Button(L("Start Claude")) { claudeJob("start", L("Starting Claude …"), "/v1/claude/start") }
                        .controlSize(.small).buttonStyle(.borderedProminent)
                }
                if running, let w, w.window {
                    Button { if let e = Terminal.attach(vm: s.vm, window: w.name) { model.setBanner(e) } } label: { Label(Terminal.appName, systemImage: "terminal") }
                        .controlSize(.small)
                        .help(L("Opens the Claude window for ~/%@ in %@", r.folder, Terminal.appName))
                }
                if s.running {
                    Menu {
                        if running, let w, w.rc, !w.rcUrl.isEmpty {
                            Button(L("Copy Remote Control link")) { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(w.rcUrl, forType: .string) }
                            Divider()
                        }
                        if running, let w, !w.rc {
                            Button(L("Remote Control is running – mark as active")) {
                                Task {
                                    do { try await model.api.post("/v1/claude/rc-ok", ClaudeBody(sandbox: s.name, folder: w.folder)); await model.refresh() }
                                    catch { model.setBanner(error.localizedDescription) }
                                }
                            }
                        }
                        if running { Button(L("Restart Claude")) { claudeJob("restart", L("Restarting Claude …"), "/v1/claude/restart") } }
                        if r.item != nil {
                            Divider()
                            Button(L("Copy to sandbox …")) { transfer = false }
                            Button(L("Move to sandbox …")) { transfer = true }
                        }
                        if w != nil || r.item != nil { Divider() }
                        if w != nil { Button(L("Remove Claude entry …"), role: .destructive) { removeOpen = true } }
                        if r.item != nil { Button(L("Delete folder …"), role: .destructive) { deleteOpen = true } }
                    } label: { Image(systemName: "ellipsis.circle") }
                    .menuStyle(.borderlessButton).fixedSize()
                }
            }
        }
    }

    func claudeJob(_ action: String, _ step: String, _ path: String) {
        let key = AppModel.jobKey("claude-\(action)", s.name, w?.name ?? r.base)
        Task { if let e = await model.runJob(key, step, path, ClaudeBody(sandbox: s.name, folder: r.folder)) { model.setBanner(e) } }
    }

    func removeEntry() {
        Task { if let e = await model.runJob(AppModel.jobKey("claude-remove", s.name, w?.name ?? r.base), L("Removing entry …"), "/v1/claude/remove", ClaudeRemoveBody(sandbox: s.name, folder: r.folder)) { model.setBanner(e) } }
    }

    func deleteFolder(force: Bool) {
        Task {
            let e = await model.runJob(AppModel.jobKey("folder-delete", s.name, r.base), L("Deleting folder …"), "/v1/folders/delete", FolderBody(sandbox: s.name, folder: r.folder, force: force))
            if let e { if e.contains("unpushed") { forceMsg = e } else { model.setBanner(e) } }
        }
    }
}

struct NewFolderSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    let s: SandboxInfo
    @Local private var clone = false
    @Local private var name = ""
    @Local private var url = ""

    var trimmedName: String { name.trimmingCharacters(in: .whitespaces) }
    var trimmedURL: String { url.trimmingCharacters(in: .whitespaces) }
    var valid: Bool {
        if clone { return trimmedURL.hasPrefix("https://") }
        return !trimmedName.isEmpty
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(L("New folder")).font(.title3.bold())
            Text(L("Sandbox %@ · created in ~/work", s.name)).font(.callout).foregroundStyle(.secondary)
            Picker("", selection: $clone) {
                Text(L("Empty folder")).tag(false)
                Text(L("Clone Git repository")).tag(true)
            }.pickerStyle(.segmented).labelsHidden()
            if clone {
                TextField(L("https://github.com/owner/repo"), text: $url).textFieldStyle(.roundedBorder)
                TextField(L("Folder name (optional, default: repository name)"), text: $name).textFieldStyle(.roundedBorder)
                Text(L("Only https URLs without credentials. For a private repository the assistant asks for a GitHub token (or clone it later in the VM)."))
                    .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            } else {
                TextField(L("Folder name"), text: $name).textFieldStyle(.roundedBorder)
                Text(L("Letters, digits, dot, underscore and dash; at most 64 characters."))
                    .font(.caption2).foregroundStyle(.secondary)
            }
            HStack {
                Spacer()
                Button(L("Cancel")) { dismiss() }.keyboardShortcut(.cancelAction)
                Button(L("Create")) {
                    let body = FolderBody(sandbox: s.name, name: trimmedName, gitUrl: clone ? trimmedURL : "")
                    let key = AppModel.jobKey("folder-create", s.name, trimmedName.isEmpty ? "new" : trimmedName)
                    dismiss()
                    Task { if let e = await model.runJob(key, clone ? L("Cloning repository …") : L("Creating folder …"), "/v1/folders/create", body) { model.setBanner(e) } }
                }.disabled(!valid).keyboardShortcut(.defaultAction)
            }
        }
        .padding(20).frame(width: 520)
    }
}

/// Copies or moves a folder into another sandbox (or copies it inside the same sandbox under a new name).
struct TransferFolderSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    let s: SandboxInfo
    let folder: String
    let label: String
    let move: Bool
    @Local private var target = ""
    @Local private var newName = ""
    @Local private var existing: Set<String>? = nil
    @Local private var checkError: String? = nil

    var targets: [SandboxInfo] { model.sandboxes.filter { $0.running && (!move || $0.name != s.name) }.sorted { $0.name < $1.name } }
    var name: String { newName.trimmingCharacters(in: .whitespaces) }
    var conflict: Bool { existing?.contains(name) == true || (target == s.name && name == label) }
    var ready: Bool { !target.isEmpty && !name.isEmpty && existing != nil && !conflict }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(move ? L("Move folder “%@”", label) : L("Copy folder “%@”", label)).font(.title3.bold())
            Text(L("From sandbox %@ · ~/%@", s.name, folder)).font(.callout).foregroundStyle(.secondary)
            if targets.isEmpty {
                Text(L("There is no running sandbox to send the folder to. Start the other sandbox first."))
                    .font(.callout).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
            } else {
                Picker(L("Target sandbox"), selection: $target) {
                    ForEach(targets) { t in Text(t.name == s.name ? L("%@ (this sandbox)", t.name) : t.name).tag(t.name) }
                }
                TextField(L("Folder name in the target"), text: $newName).textFieldStyle(.roundedBorder)
                if conflict {
                    Text(L("A folder with this name already exists in the target. Choose another name – nothing is overwritten."))
                        .font(.caption).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
                }
                if let checkError { Text(checkError).font(.caption).foregroundStyle(.red).textSelection(.enabled) }
            }
            VStack(alignment: .leading, spacing: 4) {
                Text(L("Everything in the folder is copied, including hidden files, .env files and keys, .git and node_modules. Docker volumes and running processes are not."))
                if move {
                    Text(L("Claude in this folder is stopped first. The folder is removed from this sandbox only after the copy was verified (same number of files and bytes)."))
                } else {
                    Text(L("The original stays where it is. An incomplete copy is removed automatically."))
                }
                Text(L("The data goes directly from VM to VM; nothing is stored on the Mac."))
            }.font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button(L("Cancel")) { dismiss() }.keyboardShortcut(.cancelAction)
                Button(move ? L("Move") : L("Copy")) { start() }
                    .disabled(!ready).keyboardShortcut(.defaultAction)
            }
        }
        .padding(20).frame(width: 540)
        .onAppear {
            if newName.isEmpty { newName = label }
            if target.isEmpty { target = targets.first?.name ?? "" }
        }
        .task(id: target) { await loadExisting() }
    }

    func loadExisting() async {
        existing = nil; checkError = nil
        guard !target.isEmpty else { return }
        do {
            let r: FolderListResponse = try await model.api.get("/v1/folders?sandbox=\(target)")
            existing = Set(r.folders.map(\.name))
        } catch { checkError = error.localizedDescription }
    }

    func start() {
        let body = FolderBody(sandbox: s.name, folder: folder, name: name, target: target, move: move)
        let key = AppModel.jobKey("folder-\(move ? "move" : "copy")", s.name, folder.split(separator: "/").last.map(String.init) ?? folder)
        let step = move ? L("Moving to %@ …", target) : L("Copying to %@ …", target)
        dismiss()
        Task { if let e = await model.runJob(key, step, "/v1/folders/transfer", body) { model.setBanner(e) } }
    }
}
