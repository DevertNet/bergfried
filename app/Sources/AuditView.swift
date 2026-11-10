import SwiftUI
import AppKit

struct AuditView: View {
    @EnvironmentObject var model: AppModel
    @Local private var query = ""
    @Local private var entries: [AuditEntry] = []
    @Local private var loading = false

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                TextField("Filter (sandbox, worktree, event, details …)", text: $query).textFieldStyle(.roundedBorder).onSubmit { Task { await load() } }
                Button("Refresh") { Task { await load() } }
                Button("Export CSV …") { Task { await export() } }
            }.padding(8)
            Table(entries) {
                TableColumn("Time") { Text(Date(timeIntervalSince1970: TimeInterval($0.tsUnix)).formatted(date: .abbreviated, time: .standard)) }.width(150)
                TableColumn("Actor") { Text($0.actor) }.width(60)
                TableColumn("Event") { Text($0.event).foregroundStyle(color($0.event)) }.width(150)
                TableColumn("Sandbox/Worktree") { Text($0.sandbox.isEmpty ? "" : "\($0.sandbox)/\($0.worktree)") }.width(120)
                TableColumn("Details") { Text($0.detail).lineLimit(2).textSelection(.enabled) }
            }
        }
        .task { await load() }
        .onChange(of: model.requests.count) { _, _ in Task { await load() } }
    }

    func color(_ e: String) -> Color { e.contains("denied") || e.contains("blocked") || e.contains("revoked") ? .red : (e.contains("allowed") || e.contains("copied") || e.contains("grant_created") ? .green : .primary) }

    func target(_ path: String) -> String {
        var c = URLComponents(); c.path = path
        c.queryItems = [URLQueryItem(name: "limit", value: "1000"), URLQueryItem(name: "q", value: query)]
        return c.string ?? path
    }

    func load() async {
        if let e: [AuditEntry] = try? await model.api.get(target("/v1/audit")) { entries = e }
    }

    func export() async {
        do {
            var c = URLComponents(); c.path = "/v1/audit.csv"
            c.queryItems = [URLQueryItem(name: "limit", value: "100000"), URLQueryItem(name: "q", value: query)]
            let data = try await model.api.raw("GET", c.string ?? "/v1/audit.csv")
            let panel = NSSavePanel()
            panel.nameFieldStringValue = "bergfried-audit-log.csv"
            if panel.runModal() == .OK, let url = panel.url { try data.write(to: url) }
        } catch { model.setBanner(error.localizedDescription) }
    }
}
