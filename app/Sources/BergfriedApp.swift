import SwiftUI
import AppKit

enum Pane: String, CaseIterable, Identifiable {
    case sandboxes = "Sandboxes", requests = "Requests", audit = "Audit log", settings = "Settings"
    var id: String { rawValue }
    var title: LocalizedStringKey { LocalizedStringKey(rawValue) }
    var icon: String { ["Sandboxes": "shield", "Requests": "bell", "Audit log": "list.bullet.rectangle", "Settings": "gearshape"][rawValue] ?? "circle" }
}

struct ContentView: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        NavigationSplitView {
            List(Pane.allCases, selection: $model.pane) { p in
                HStack {
                    Label(p.title, systemImage: p.icon)
                    if p == .requests && model.pendingCount > 0 { Spacer(); Text("\(model.pendingCount)").font(.caption.bold()).padding(.horizontal, 6).background(.red, in: Capsule()).foregroundStyle(.white) }
                }.tag(p)
            }.navigationSplitViewColumnWidth(190)
        } detail: {
            VStack(spacing: 0) {
                if !model.online {
                    Label("The daemon is not reachable. You can install and start it under \"Settings\".", systemImage: "exclamationmark.triangle.fill")
                        .padding(8).frame(maxWidth: .infinity, alignment: .leading).background(.red.opacity(0.15))
                }
                if let b = model.banner {
                    HStack { Text(b).textSelection(.enabled); Spacer(); Button("Close") { model.banner = nil } }
                        .padding(8).background(.orange.opacity(0.2))
                }
                switch model.pane ?? .sandboxes {
                case .sandboxes: SandboxesView()
                case .requests: RequestsView()
                case .audit: AuditView()
                case .settings: SettingsView()
                }
            }.navigationTitle((model.pane ?? .sandboxes).title)
        }
        // Before the first index build after an install or update: explain the many macOS folder prompts, then let the daemon start.
        .alert("Why macOS asks for access to many folders", isPresented: Binding(get: { model.status?.indexWaiting == true }, set: { _ in })) {
            Button("Continue") { Task { do { try await model.api.post("/v1/index/start", NameBody(name: "x")) } catch { model.setBanner(error.localizedDescription) }; await model.refresh() } }
        } message: { Text("Bergfried now builds a search index of file names in your home folder, so that agents can find files with list_files/search_files. macOS therefore asks one by one for Documents, Desktop, Downloads and other folders – and again after every update, because the app is not signed with a developer certificate. Only names, sizes and dates are indexed, never contents. An agent only gets the content of a file after you approve that request in the app. Blocked locations (such as ~/.ssh and ~/Library) are skipped. If you deny a folder, it is simply missing from the index.") }
    }
}

struct MenuLabel: View {
    @ObservedObject var model: AppModel
    var body: some View {
        HStack(spacing: 3) {
            Image(systemName: model.online ? "shield.lefthalf.filled" : "shield.slash")
            if model.pendingCount > 0 { Text("\(model.pendingCount)") }
        }
    }
}

struct MenuContent: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.openWindow) var openWindow

    var body: some View {
        if !model.online { Text("Daemon not reachable") }
        else if model.status?.indexWaiting == true { Text("Open the Bergfried window to start indexing") }
        else if model.requests.isEmpty { Text("No open requests") }
        ForEach(model.requests.prefix(6)) { r in
            if r.kind == "ssh" {
                Button(L("SSH %@/%@ → %@ …", r.sandbox, r.agent, r.path)) { open() }
            } else if r.kind == "ghtoken" {
                Button("GitHub \(r.sandbox)/\(r.agent): \(r.path) …") { open() }
            } else {
                Menu("\(r.sandbox)/\(r.agent): ~/\(r.path)") {
                    Button("Allow") { if r.large { open() } else { Task { await model.decideFile(r, allow: true) } } }
                    Button("Deny") { Task { await model.decideFile(r, allow: false) } }
                }
            }
        }
        if !model.grants.isEmpty { Divider(); Text(L("%lld active SSH grant(s)", model.grants.count)) }
        Divider()
        Button("Open window") { open() }
        Button("Quit") { NSApp.terminate(nil) }
    }

    func open() { model.pane = .requests; openWindow(id: "main"); NSApp.activate(ignoringOtherApps: true) }
}

@main
struct BergfriedApp: App {
    @StateObject private var model = AppModel()

    init() {
        if CommandLine.arguments.contains("--selftest") {
            Task {
                do {
                    let api = API(socket: API.defaultSocket)
                    let st: DaemonStatus = try await api.get("/v1/status")
                    let ss: [SandboxInfo] = try await api.get("/v1/sandboxes")
                    let rq: [RequestInfo] = try await api.get("/v1/requests?all=1")
                    let au: [AuditEntry] = try await api.get("/v1/audit?limit=2000")
                    let bl: Blocklist = try await api.get("/v1/blocklist")
                    print("selftest ok: version=\(st.version) base=\(st.baseReady) index=\(st.indexReady) sandboxes=\(ss.count) requests=\(rq.count) audit=\(au.count) blocklist=\(bl.paths.count)")
                    exit(0)
                } catch { print("selftest ERROR: \(error.localizedDescription)"); exit(1) }
            }
        }
    }

    var body: some Scene {
        Window("Bergfried", id: "main") {
            ContentView().environmentObject(model).frame(minWidth: 920, minHeight: 560)
                .task { model.start() }
        }
        MenuBarExtra {
            MenuContent().environmentObject(model)
        } label: { MenuLabel(model: model) }
    }
}
