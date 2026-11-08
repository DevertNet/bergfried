import SwiftUI
import AppKit

enum Pane: String, CaseIterable, Identifiable {
    case sessions = "Sessions", requests = "Anfragen", audit = "Protokoll", settings = "Einstellungen"
    var id: String { rawValue }
    var icon: String { ["Sessions": "shield", "Anfragen": "bell", "Protokoll": "list.bullet.rectangle", "Einstellungen": "gearshape"][rawValue] ?? "circle" }
}

struct ContentView: View {
    @EnvironmentObject var model: AppModel
    @Local private var pane: Pane? = .sessions

    var body: some View {
        NavigationSplitView {
            List(Pane.allCases, selection: $pane) { p in
                HStack {
                    Label(p.rawValue, systemImage: p.icon)
                    if p == .requests && model.pendingCount > 0 { Spacer(); Text("\(model.pendingCount)").font(.caption.bold()).padding(.horizontal, 6).background(.red, in: Capsule()).foregroundStyle(.white) }
                }.tag(p)
            }.navigationSplitViewColumnWidth(190)
        } detail: {
            VStack(spacing: 0) {
                if !model.online {
                    Label("Der Daemon ist nicht erreichbar. Unter „Einstellungen“ kannst du ihn installieren und starten.", systemImage: "exclamationmark.triangle.fill")
                        .padding(8).frame(maxWidth: .infinity, alignment: .leading).background(.red.opacity(0.15))
                }
                if let b = model.banner {
                    HStack { Text(b).textSelection(.enabled); Spacer(); Button("Schließen") { model.banner = nil } }
                        .padding(8).background(.orange.opacity(0.2))
                }
                switch pane ?? .sessions {
                case .sessions: SessionsView()
                case .requests: RequestsView()
                case .audit: AuditView()
                case .settings: SettingsView()
                }
            }.navigationTitle((pane ?? .sessions).rawValue)
        }
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
        if !model.online { Text("Daemon nicht erreichbar") }
        else if model.requests.isEmpty { Text("Keine offenen Anfragen") }
        ForEach(model.requests.prefix(6)) { r in
            if r.kind == "ssh" {
                Button("SSH \(r.session)/\(r.agent) → \(r.path) …") { open() }
            } else {
                Menu("\(r.session)/\(r.agent): ~/\(r.path)") {
                    Button("Erlauben") { if r.large { open() } else { Task { await model.decideFile(r, allow: true) } } }
                    Button("Ablehnen") { Task { await model.decideFile(r, allow: false) } }
                }
            }
        }
        if !model.grants.isEmpty { Divider(); Text("\(model.grants.count) aktive SSH-Freigabe(n)") }
        Divider()
        Button("Fenster öffnen") { open() }
        Button("Beenden") { NSApp.terminate(nil) }
    }

    func open() { openWindow(id: "main"); NSApp.activate(ignoringOtherApps: true) }
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
                    let ss: [SessionInfo] = try await api.get("/v1/sessions")
                    let rq: [RequestInfo] = try await api.get("/v1/requests?all=1")
                    let au: [AuditEntry] = try await api.get("/v1/audit?limit=2000")
                    let bl: Blocklist = try await api.get("/v1/blocklist")
                    print("selftest ok: version=\(st.version) base=\(st.baseReady) index=\(st.indexReady) sessions=\(ss.count) requests=\(rq.count) audit=\(au.count) blocklist=\(bl.paths.count)")
                    exit(0)
                } catch { print("selftest FEHLER: \(error.localizedDescription)"); exit(1) }
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
