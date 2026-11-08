import SwiftUI
import AppKit

/// Eigenes Popup-Fenster für offene Anfragen. Zuverlässiger Ersatz, falls macOS keine Banner zeigt
/// (Mitteilungs-Stil der App auf „Ohne“). Schwebt über anderen Fenstern, stiehlt aber nicht den Fokus.
@MainActor
final class PopupController {
    static let shared = PopupController()
    static let key = "requestPopup"
    private var window: NSWindow?
    private var host: NSHostingView<AnyView>?

    static var enabled: Bool { UserDefaults.standard.object(forKey: key) as? Bool ?? true }

    /// `requests` ist der NEUE Wert (@Published meldet vor dem Setzen; model.requests wäre noch der alte).
    func update(model: AppModel, requests: [RequestInfo]) {
        guard Self.enabled, !requests.isEmpty else { hide(); return }
        if window == nil {
            let hv = NSHostingView(rootView: AnyView(PopupView().environmentObject(model)))
            let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 400, height: 150), styleMask: [.titled, .closable], backing: .buffered, defer: false)
            w.title = "Bergfried – offene Anfragen"
            w.level = .floating
            w.hidesOnDeactivate = false
            w.isReleasedWhenClosed = false
            w.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
            w.contentView = hv
            window = w; host = hv
        }
        guard let w = window, let hv = host else { return }
        // Höhe an den Inhalt anpassen (neuer Inhalt erst nach dem Update messbar).
        DispatchQueue.main.async { [weak self] in
            guard let self, let w = self.window, let hv = self.host else { return }
            let h = max(110, hv.fittingSize.height)
            w.setContentSize(NSSize(width: 400, height: h))
            if let screen = NSScreen.main {
                let f = screen.visibleFrame
                w.setFrameOrigin(NSPoint(x: f.maxX - w.frame.width - 16, y: f.maxY - w.frame.height - 16))
            }
        }
        _ = hv
        w.orderFrontRegardless()
    }

    private func hide() {
        guard let w = window else { return }
        w.orderOut(nil)
        w.contentView = nil
        window = nil; host = nil
    }

    func openMainWindow(pane: Pane = .requests, model: AppModel) {
        model.pane = pane
        NSApp.activate(ignoringOtherApps: true)
        NSWorkspace.shared.openApplication(at: Bundle.main.bundleURL, configuration: NSWorkspace.OpenConfiguration())
    }
}

struct PopupView: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(model.requests.prefix(3)) { r in
                VStack(alignment: .leading, spacing: 5) {
                    HStack {
                        Image(systemName: r.kind == "ssh" ? "key.fill" : (r.kind == "folder" ? "folder" : "doc"))
                        Text("\(r.session) / \(r.agent)").font(.headline)
                    }
                    if r.kind == "ssh" {
                        Text("möchte per SSH auf **\(r.path)** zugreifen" + (r.ssh?.requestedUser.isEmpty == false ? " (Benutzer \(r.ssh!.requestedUser))" : ""))
                    } else {
                        Text("möchte \(r.kind == "folder" ? "den Ordner" : "die Datei") **~/\(r.path)** (\(formatBytes(r.bytes)))").lineLimit(2)
                    }
                    Text(r.reason.isEmpty ? "Keine Begründung angegeben" : r.reason).font(.caption).foregroundStyle(.secondary).lineLimit(3)
                    HStack {
                        if r.kind == "ssh" || r.large {
                            Button("Entscheiden …") { PopupController.shared.openMainWindow(model: model) }.buttonStyle(.borderedProminent)
                        } else {
                            Button("Erlauben") { Task { await model.decideFile(r, allow: true) } }.buttonStyle(.borderedProminent)
                        }
                        Button("Ablehnen", role: .destructive) {
                            Task {
                                if r.kind == "ssh" { _ = await model.decideSSH(SSHDecideBody(id: r.id, allow: false, minutes: 0, keyFingerprint: "", user: "", allAgents: false, perConnection: false, trustUnknownHost: false)) }
                                else { await model.decideFile(r, allow: false) }
                            }
                        }
                    }
                }
                if r.id != model.requests.prefix(3).last?.id { Divider() }
            }
            if model.requests.count > 3 {
                Button("… und \(model.requests.count - 3) weitere") { PopupController.shared.openMainWindow(model: model) }.buttonStyle(.link)
            }
        }
        .padding(14).frame(width: 400)
    }
}

