import Foundation
import UserNotifications
import AppKit

/// Mitteilungen mit Aktions-Buttons. Der Text der Agents wird nur als Anzeige verwendet.
final class Notifier: NSObject, UNUserNotificationCenterDelegate, @unchecked Sendable {
    static let shared = Notifier()
    private weak var model: AppModel?
    private var enabled: Bool { Bundle.main.bundleIdentifier != nil }

    @MainActor func setup(model: AppModel) {
        self.model = model
        guard enabled else { return }
        let c = UNUserNotificationCenter.current()
        c.delegate = self
        let allow = UNNotificationAction(identifier: "allow", title: "Erlauben", options: [])
        let deny = UNNotificationAction(identifier: "deny", title: "Ablehnen", options: [.destructive])
        let sshAllow = UNNotificationAction(identifier: "ssh-allow", title: "Erlauben (Standard)", options: [])
        let open = UNNotificationAction(identifier: "open", title: "Öffnen …", options: [.foreground])
        c.setNotificationCategories([
            UNNotificationCategory(identifier: "FILE", actions: [allow, deny], intentIdentifiers: [], options: []),
            UNNotificationCategory(identifier: "OPEN", actions: [open, deny], intentIdentifiers: [], options: []),
            UNNotificationCategory(identifier: "SSH_QUICK", actions: [sshAllow, open, deny], intentIdentifiers: [], options: []),
        ])
        c.requestAuthorization(options: [.alert, .sound, .badge]) { _, _ in }
    }

    private func clip(_ s: String, _ n: Int) -> String { s.count > n ? String(s.prefix(n)) + "…" : s }

    func post(_ r: RequestInfo) {
        guard enabled else { return }
        let c = UNMutableNotificationContent()
        c.userInfo = ["id": r.id]
        c.sound = .default
        switch r.kind {
        case "ssh":
            c.title = "SSH-Anfrage: \(r.session)/\(r.agent)"
            c.body = "Zugriff auf \(r.path)\n\(clip(r.reason, 140))"
            let quick = r.ssh.map { $0.source == "known_hosts" && !$0.defaultKey.isEmpty } ?? false
            c.categoryIdentifier = quick ? "SSH_QUICK" : "OPEN"
        default:
            c.title = "Datei-Anfrage: \(r.session)/\(r.agent)"
            c.body = "~/\(r.path) (\(formatBytes(r.bytes)))\n\(clip(r.reason, 140))"
            c.categoryIdentifier = r.large ? "OPEN" : "FILE"
        }
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "req-\(r.id)", content: c, trigger: nil))
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent n: UNNotification) async -> UNNotificationPresentationOptions {
        [.banner, .sound]
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        guard let id = response.notification.request.content.userInfo["id"] as? Int64 ?? (response.notification.request.content.userInfo["id"] as? Int).map(Int64.init) else { return }
        let action = response.actionIdentifier
        await MainActor.run {
            guard let m = model, let r = m.requests.first(where: { $0.id == id }) else { return }
            switch action {
            case "allow": Task { await m.decideFile(r, allow: true) }
            case "deny":
                if r.kind == "ssh" {
                    Task { _ = await m.decideSSH(SSHDecideBody(id: r.id, allow: false, minutes: 0, keyFingerprint: "", user: "", allAgents: false, perConnection: false, trustUnknownHost: false)) }
                } else { Task { await m.decideFile(r, allow: false) } }
            case "ssh-allow":
                if let s = r.ssh, s.source == "known_hosts", !s.defaultKey.isEmpty {
                    Task { _ = await m.decideSSH(SSHDecideBody(id: r.id, allow: true, minutes: s.suggestedMinutes, keyFingerprint: s.defaultKey, user: s.defaultUser, allAgents: false, perConnection: false, trustUnknownHost: false)) }
                }
            default:
                NSApp.activate(ignoringOtherApps: true)
            }
        }
    }
}
