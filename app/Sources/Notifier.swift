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

    /// Letztes Ergebnis der Berechtigungsanfrage (für die Diagnose in den Einstellungen).
    var lastAuth: String = "noch nicht angefragt"

    /// Aktueller Stand laut macOS.
    func status() async -> String {
        guard enabled else { return "kein App-Bundle" }
        let s = await UNUserNotificationCenter.current().notificationSettings()
        let name: String
        switch s.authorizationStatus {
        case .notDetermined: name = "noch nicht entschieden"
        case .denied: name = "abgelehnt"
        case .authorized: name = "erlaubt"
        case .provisional: name = "vorläufig erlaubt"
        case .ephemeral: name = "temporär erlaubt"
        @unknown default: name = "unbekannt"
        }
        return "\(name) (Banner: \(s.alertSetting == .enabled ? "an" : "aus"), Ton: \(s.soundSetting == .enabled ? "an" : "aus"))"
    }

    func postTest() {
        guard enabled else { return }
        let c = UNMutableNotificationContent()
        c.title = "Bergfried – Testmitteilung"
        c.body = "Wenn du das siehst, funktionieren Mitteilungen."
        c.sound = .default
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "test-\(UUID().uuidString)", content: c, trigger: nil)) { err in
            if let err { NSLog("Bergfried Testmitteilung Fehler: %@", err.localizedDescription) }
        }
    }

    private func clip(_ s: String, _ n: Int) -> String { s.count > n ? String(s.prefix(n)) + "…" : s }

    func post(_ r: RequestInfo) {
        // Ausweichen, falls macOS keine Mitteilungen zeigt: Dock-Icon springt (Zähler-Badge setzt das AppModel).
        DispatchQueue.main.async { NSApp.requestUserAttention(.criticalRequest) }
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
        case "gitpush":
            c.title = "Push-Anfrage: \(r.session)/\(r.agent)"
            c.body = "\(r.git?.commits.count ?? 0) Commit(s) nach \(r.path)\n\(clip(r.reason, 140))"
            c.categoryIdentifier = "OPEN"
        case "cilogs":
            c.title = "CI-Logs angefragt: \(r.session)/\(r.agent)"
            c.body = "\(r.path)"
            c.categoryIdentifier = "OPEN"
        case "cirerun":
            c.title = "CI wiederholen: \(r.session)/\(r.agent)"
            c.body = "\(r.path)"
            c.categoryIdentifier = "OPEN"
        default:
            c.title = "Datei-Anfrage: \(r.session)/\(r.agent)"
            c.body = "~/\(r.path) (\(formatBytes(r.bytes)))\n\(clip(r.reason, 140))"
            c.categoryIdentifier = r.large ? "OPEN" : "FILE"
        }
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "req-\(r.id)", content: c, trigger: nil))
    }

    /// Die CI eines Agent-Branches ist rot geworden.
    func postCI(session: String, agent: String, ci: CIState) {
        DispatchQueue.main.async { NSApp.requestUserAttention(.informationalRequest) }
        guard enabled else { return }
        let c = UNMutableNotificationContent()
        c.title = "CI rot: \(session)/\(agent)"
        c.body = "\(ci.repo) · \(ci.branch)\(ci.name.isEmpty ? "" : " · " + ci.name)\nIn der App „Agent: CI fixen“ wählen."
        c.sound = .default
        c.categoryIdentifier = "OPEN"
        c.userInfo = ["ci": true]
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "ci-\(session)-\(agent)-\(ci.branch)", content: c, trigger: nil))
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent n: UNNotification) async -> UNNotificationPresentationOptions {
        [.banner, .sound]
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        if response.notification.request.content.userInfo["ci"] != nil {
            await MainActor.run { model?.pane = .sessions; NSApp.activate(ignoringOtherApps: true) }
            return
        }
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
                    Task { _ = await m.decideSSH(SSHDecideBody(id: r.id, allow: true, minutes: s.suggestedMinutes, keyFingerprint: s.defaultKey, user: s.defaultUser, allAgents: false, perConnection: false, trustUnknownHost: false)) }
                }
            default:
                m.pane = .requests
                NSApp.activate(ignoringOtherApps: true)
            }
        }
    }
}
