import SwiftUI
import AppKit

/// Shows the end of the daemon log, the updater log or Lima's log of a VM, so that long error messages can be read and copied.
struct LogView: View {
    @EnvironmentObject var model: AppModel
    @Local private var source = "daemon"
    @Local private var info: LogInfo?
    @Local private var failed: String?

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Picker("Diagnostics log", selection: $source) {
                    Text("Daemon (errors of actions)").tag("daemon")
                    Text("Updates").tag("update")
                    Text("Last base build failure").tag("base")
                    ForEach(model.sandboxes) { s in Text(L("VM: %@ (Lima)", s.name)).tag("lima:" + s.vm) }
                }.frame(maxWidth: 360)
                Button("Refresh") { Task { await load() } }
                Button("Copy all") {
                    NSPasteboard.general.clearContents(); NSPasteboard.general.setString(info?.text ?? "", forType: .string)
                }.disabled((info?.text ?? "").isEmpty)
                Button("Show in Finder") { if let p = info?.path { NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: p)]) } }
                    .disabled(info == nil)
                Spacer()
            }.padding(8)
            if let f = failed { Text(f).foregroundStyle(.orange).padding(.horizontal, 8) }
            if let p = info?.path { Text(p).font(.caption.monospaced()).foregroundStyle(.secondary).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 8) }
            ScrollViewReader { proxy in
                ScrollView {
                    Text((info?.text ?? "").isEmpty ? L("(the log is empty or does not exist yet)") : info!.text)
                        .font(.caption.monospaced()).textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading).padding(8)
                    Color.clear.frame(height: 1).id("end")
                }
                .onChange(of: info?.text) { _, _ in proxy.scrollTo("end", anchor: .bottom) }
            }
        }
        .task { await load() }
        .onChange(of: source) { _, _ in Task { await load() } }
    }

    func load() async {
        failed = nil
        var c = URLComponents(); c.path = "/v1/log"
        if source.hasPrefix("lima:") {
            c.queryItems = [URLQueryItem(name: "source", value: "lima"), URLQueryItem(name: "vm", value: String(source.dropFirst(5)))]
        } else {
            c.queryItems = [URLQueryItem(name: "source", value: source)]
        }
        do { info = try await model.api.get(c.string ?? "/v1/log") } catch { failed = error.localizedDescription }
    }
}
