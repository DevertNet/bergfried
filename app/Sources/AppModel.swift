import Foundation
import SwiftUI
import Combine
import AppKit

@MainActor
final class AppModel: ObservableObject {
    let api = API(socket: API.defaultSocket)

    @Published var online = false
    @Published var status: DaemonStatus?
    @Published var sessions: [SessionInfo] = []
    @Published var requests: [RequestInfo] = []   // offene
    @Published var grants: [GrantInfo] = []       // aktive SSH-Freigaben
    @Published var pane: Pane? = .sessions
    @Published var banner: String?
    @Published var running: [String: String] = [:] // Aktion -> Beschriftung (laufende Jobs)

    var pendingCount: Int { requests.count }
    private var started = false
    private var notified = Set<Int64>()
    private var firstLoad = true
    private var bag = Set<AnyCancellable>()

    func start() {
        guard !started else { return }
        started = true
        Notifier.shared.setup(model: self)
        // Dock-Badge folgt den offenen Anfragen.
        $requests.removeDuplicates().receive(on: RunLoop.main).sink { [weak self] reqs in
            guard let self else { return }
            NSApp.dockTile.badgeLabel = reqs.isEmpty ? nil : "\(reqs.count)"
        }.store(in: &bag)
        Task { [weak self] in
            while let self {
                await self.refresh()
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    func refresh() async {
        do {
            let st: DaemonStatus = try await api.get("/v1/status")
            let ss: [SessionInfo] = try await api.get("/v1/sessions")
            let rq: [RequestInfo] = try await api.get("/v1/requests")
            let gr: [GrantInfo] = try await api.get("/v1/ssh/grants")
            status = st; sessions = ss; requests = rq; grants = gr
            online = true
            // Beim ersten Laden nicht für Altbestand benachrichtigen.
            for r in rq where !notified.contains(r.id) {
                notified.insert(r.id)
                if !firstLoad { Notifier.shared.post(r) }
            }
            firstLoad = false
        } catch {
            online = false
        }
    }

    func setBanner(_ s: String?) { banner = s }

    /// Startet einen Daemon-Job und wartet auf sein Ende. Liefert die Fehlermeldung oder nil.
    @discardableResult
    func runJob<B: Encodable>(_ key: String, _ label: String, _ path: String, _ body: B) async -> String? {
        running[key] = label
        defer { running[key] = nil }
        do {
            let job: Job = try await api.send("POST", path, body)
            var j = job
            while j.state == "running" {
                if let st = j.step, !st.isEmpty { running[key] = st }
                try await Task.sleep(nanoseconds: 700_000_000)
                j = try await api.get("/v1/jobs/\(job.id)")
                await refresh()
            }
            await refresh()
            return j.state == "failed" ? (j.error ?? "Fehlgeschlagen") : nil
        } catch {
            return error.localizedDescription
        }
    }

    func decideFile(_ r: RequestInfo, allow: Bool, confirmLarge: Bool = false) async {
        do { try await api.post("/v1/requests/decide", DecideBody(id: r.id, allow: allow, confirmLarge: confirmLarge)) }
        catch { banner = error.localizedDescription }
        await refresh()
    }

    func decideSSH(_ b: SSHDecideBody) async -> String? {
        do { try await api.post("/v1/ssh/decide", b); await refresh(); return nil }
        catch { return error.localizedDescription }
    }

    /// Lehnt eine Anfrage beliebiger Art ab (Datei/Ordner, SSH, GitHub-Token).
    func deny(_ r: RequestInfo) async {
        do {
            switch r.kind {
            case "ssh":
                try await api.post("/v1/ssh/decide", SSHDecideBody(id: r.id, allow: false, minutes: 0, keyFingerprint: "", user: "", allAgents: false, perConnection: false, trustUnknownHost: false))
            case "ghtoken":
                try await api.post("/v1/github/decide", GitHubDecideBody(id: r.id, allow: false, token: ""))
            default:
                try await api.post("/v1/requests/decide", DecideBody(id: r.id, allow: false))
            }
        } catch { banner = error.localizedDescription }
        await refresh()
    }

    /// Token einfügen bzw. „Zugriff erweitert“ bestätigen (token leer). Rückgabe: Fehlermeldung (Anfrage bleibt dann offen).
    func decideGitHub(id: Int64, token: String) async -> String? {
        do { try await api.post("/v1/github/decide", GitHubDecideBody(id: id, allow: true, token: token)); await refresh(); return nil }
        catch { return error.localizedDescription }
    }

    func revoke(_ g: GrantInfo) async {
        do { try await api.post("/v1/ssh/revoke", IDBody(id: g.id)) } catch { banner = error.localizedDescription }
        await refresh()
    }

    func extend(_ g: GrantInfo, minutes: Int) async {
        do { try await api.post("/v1/ssh/extend", ExtendBody(id: g.id, minutes: minutes)) } catch { banner = error.localizedDescription }
        await refresh()
    }
}
