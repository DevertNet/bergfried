import Foundation
import UserNotifications
import AppKit

/// Notifications with action buttons. The assistants' text is used for display only.
final class Notifier: NSObject, UNUserNotificationCenterDelegate, @unchecked Sendable {
    static let shared = Notifier()
    private weak var model: AppModel?
    private var enabled: Bool { Bundle.main.bundleIdentifier != nil }

    @MainActor func setup(model: AppModel) {
        self.model = model
        guard enabled else { return }
        let c = UNUserNotificationCenter.current()
        c.delegate = self
        let allow = UNNotificationAction(identifier: "allow", title: L("Allow"), options: [])
        let deny = UNNotificationAction(identifier: "deny", title: L("Deny"), options: [.destructive])
        let sshAllow = UNNotificationAction(identifier: "ssh-allow", title: L("Allow (default)"), options: [])
        let open = UNNotificationAction(identifier: "open", title: L("Open …"), options: [.foreground])
        c.setNotificationCategories([
            UNNotificationCategory(identifier: "FILE", actions: [allow, deny], intentIdentifiers: [], options: []),
            UNNotificationCategory(identifier: "OPEN", actions: [open, deny], intentIdentifiers: [], options: []),
            UNNotificationCategory(identifier: "SSH_QUICK", actions: [sshAllow, open, deny], intentIdentifiers: [], options: []),
        ])
        c.requestAuthorization(options: [.alert, .sound, .badge]) { granted, err in
            let msg = "granted=\(granted) error=\(err.map { "\(($0 as NSError).domain) \(($0 as NSError).code): \($0.localizedDescription)" } ?? "none")"
            NSLog("Bergfried Mitteilungen: %@", msg)
            Notifier.shared.lastAuth = msg
            Task {
                let st = await Notifier.shared.status()
                try? "\(msg)\nstatus: \(st)\n".write(toFile: NSHomeDirectory() + "/.bergfried/notifications.txt", atomically: true, encoding: .utf8)
            }
        }
    }

    /// Last result of the permission request (for the diagnosis in Settings).
    var lastAuth: String = L("not requested yet")

    /// Current state according to macOS.
    func status() async -> String {
        guard enabled else { return L("no app bundle") }
        let s = await UNUserNotificationCenter.current().notificationSettings()
        let name: String
        switch s.authorizationStatus {
        case .notDetermined: name = L("not decided yet")
        case .denied: name = L("denied")
        case .authorized: name = L("allowed")
        case .provisional: name = L("provisionally allowed")
        case .ephemeral: name = L("temporarily allowed")
        @unknown default: name = L("unknown")
        }
        return L("%@ (banner: %@, sound: %@)", name, s.alertSetting == .enabled ? L("on") : L("off"), s.soundSetting == .enabled ? L("on") : L("off"))
    }

    func postTest() {
        guard enabled else { return }
        let c = UNMutableNotificationContent()
        c.title = L("Bergfried – test notification")
        c.body = L("If you can see this, notifications work.")
        c.sound = .default
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "test-\(UUID().uuidString)", content: c, trigger: nil)) { err in
            if let err { NSLog("Bergfried test notification error: %@", err.localizedDescription) }
        }
    }

    private func clip(_ s: String, _ n: Int) -> String { s.count > n ? String(s.prefix(n)) + "…" : s }

    func post(_ r: RequestInfo) {
        // Fallback in case macOS shows no notifications: the dock icon bounces (the counter badge is set by the AppModel).
        DispatchQueue.main.async { NSApp.requestUserAttention(.criticalRequest) }
        guard enabled else { return }
        let c = UNMutableNotificationContent()
        c.userInfo = ["id": r.id]
        c.sound = .default
        switch r.kind {
        case "ssh":
            c.title = L("SSH request: %@/%@", r.sandbox, r.worktree)
            c.body = L("Access to %@", r.path) + "\n\(clip(r.reason, 140))"
            let quick = r.ssh.map { $0.source == "known_hosts" && !$0.defaultKey.isEmpty } ?? false
            c.categoryIdentifier = quick ? "SSH_QUICK" : "OPEN"
        case "ghtoken":
            c.title = L("GitHub access: %@/%@", r.sandbox, r.worktree)
            c.body = "\(r.path)\n\(clip(r.reason, 140))"
            c.categoryIdentifier = "OPEN"
        default:
            c.title = L("File request: %@/%@", r.sandbox, r.worktree)
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
                Task { await m.deny(r) }
            case "ssh-allow":
                if let s = r.ssh, s.source == "known_hosts", !s.defaultKey.isEmpty {
                    Task { _ = await m.decideSSH(SSHDecideBody(id: r.id, allow: true, minutes: s.suggestedMinutes, keyFingerprint: s.defaultKey, user: s.defaultUser, allWorktrees: false, perConnection: false, trustUnknownHost: false)) }
                }
            default:
                m.pane = .requests
                NSApp.activate(ignoringOtherApps: true)
            }
        }
    }
}
