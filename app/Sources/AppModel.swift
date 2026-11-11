import Foundation
import SwiftUI
import Combine
import AppKit

@MainActor
final class AppModel: ObservableObject {
    let api = API(socket: API.defaultSocket)

    @Published var online = false
    @Published var status: DaemonStatus?
    @Published var sandboxes: [SandboxInfo] = []
    @Published var requests: [RequestInfo] = []   // offene
    @Published var grants: [GrantInfo] = []       // aktive SSH-Freigaben
    @Published var pane: Pane? = .sandboxes
    @Published var banner: String?
    @Published var running: [String: String] = [:] // Aktion -> Beschriftung (laufende Jobs)
    /// Last step text a job reported (survives the job; used to explain a lost connection during an update).
    var lastStep: [String: String] = [:]
    /// Output lines of running (and the last finished) jobs by job key, shown by JobOutput.
    @Published var logs: [String: [String]] = [:]

    var pendingCount: Int { requests.count }
    private var started = false
    private var notified = Set<Int64>()
    private var firstLoad = true
    private var bag = Set<AnyCancellable>()

    func start() {
        guard !started else { return }
        started = true
        Notifier.shared.setup(model: self)
        // Dock badge follows the open requests.
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
            let ss: [SandboxInfo] = try await api.get("/v1/sandboxes")
            let rq: [RequestInfo] = try await api.get("/v1/requests")
            let gr: [GrantInfo] = try await api.get("/v1/ssh/grants")
            status = st; sandboxes = ss; requests = rq; grants = gr
            online = true
            // Do not notify about old items on the first load.
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

    /// Starts a daemon job and waits for its end. Returns the error message or nil.
    @discardableResult
    func runJob<B: Encodable>(_ key: String, _ label: String, _ path: String, _ body: B) async -> String? {
        running[key] = label
        lastStep[key] = nil
        logs[key] = nil
        defer { running[key] = nil }
        do {
            let job: Job = try await api.send("POST", path, body)
            var j = job
            while j.state == "running" {
                if let st = j.step, !st.isEmpty { running[key] = st; lastStep[key] = st }
                if let l = j.log, !l.isEmpty { logs[key] = l }
                try await Task.sleep(nanoseconds: 700_000_000)
                j = try await api.get("/v1/jobs/\(job.id)")
                await refresh()
            }
            if let l = j.log, !l.isEmpty { logs[key] = l }
            await refresh()
            return j.state == "failed" ? (j.error ?? L("Failed")) : nil
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

    /// Denies a request of any kind (file/folder, SSH, GitHub token) with an optional note for the assistant.
    func deny(_ r: RequestInfo, note: String = "") async {
        do {
            switch r.kind {
            case "ssh":
                try await api.post("/v1/ssh/decide", SSHDecideBody(id: r.id, allow: false, minutes: 0, keyFingerprint: "", user: "", allFolders: false, perConnection: false, trustUnknownHost: false, note: note))
            case "ghtoken":
                try await api.post("/v1/github/decide", GitHubDecideBody(id: r.id, allow: false, token: "", note: note))
            default:
                try await api.post("/v1/requests/decide", DecideBody(id: r.id, allow: false, note: note))
            }
        } catch { banner = error.localizedDescription }
        await refresh()
    }

    /// Paste a token or confirm "access extended" (token empty). Returns the error message (the request then stays open).
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
