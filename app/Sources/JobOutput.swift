import SwiftUI

/// Live output of a running job (limactl and the VM's own provisioning log while a VM is created, cloned or started).
/// The collapsed line shows the latest output line; expanded it shows the last lines and can be selected and copied.
struct JobOutput: View {
    @EnvironmentObject var model: AppModel
    let key: String
    var lines: [String] { model.logs[key] ?? [] }

    var body: some View {
        if !lines.isEmpty {
            DisclosureGroup {
                ScrollViewReader { proxy in
                    ScrollView {
                        Text(lines.joined(separator: "\n")).font(.caption.monospaced()).textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                        Color.clear.frame(height: 1).id("end")
                    }
                    .frame(maxHeight: 180)
                    .onChange(of: lines.count) { _, _ in proxy.scrollTo("end", anchor: .bottom) }
                }
                HStack {
                    Spacer()
                    Button("Copy output") { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(lines.joined(separator: "\n"), forType: .string) }.controlSize(.small)
                }
            } label: {
                Text(lines.last ?? "").font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1).truncationMode(.tail)
            }
        }
    }
}
