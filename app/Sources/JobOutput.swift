import SwiftUI

/// Live output of a running job (limactl and the VM's own provisioning log while a VM is created, cloned or started).
/// The collapsed line shows the latest output line; expanded it shows the last lines and can be selected and copied.
/// While expanded it follows new output, but scrolling up pauses that so old lines stay readable.
struct JobOutput: View {
    @EnvironmentObject var model: AppModel
    let key: String
    @Local private var follow = true
    @Local private var expanded = false
    var lines: [String] { model.logs[key] ?? [] }

    private struct ScrollSnapshot: Equatable {
        var atBottom: Bool
        var height: CGFloat
    }

    var body: some View {
        if !lines.isEmpty {
            DisclosureGroup(isExpanded: $expanded) {
                ScrollViewReader { proxy in
                    ScrollView {
                        VStack(alignment: .leading, spacing: 0) {
                            Text(lines.joined(separator: "\n")).font(.caption.monospaced()).textSelection(.enabled)
                                .frame(maxWidth: .infinity, alignment: .leading)
                            Color.clear.frame(height: 1).id("end")
                        }
                    }
                    .frame(maxHeight: 180)
                    .onScrollGeometryChange(for: ScrollSnapshot.self) { geo in
                        ScrollSnapshot(atBottom: geo.contentOffset.y + geo.containerSize.height >= geo.contentSize.height - 24,
                                       height: geo.contentSize.height)
                    } action: { old, new in
                        // Content growth must not stop following; only a real scroll (same height) does.
                        if abs(new.height - old.height) < 0.5 {
                            follow = new.atBottom
                        }
                    }
                    .onChange(of: lines.count) { _, _ in
                        if expanded && follow { proxy.scrollTo("end", anchor: .bottom) }
                    }
                    .onChange(of: expanded) { _, open in
                        if open {
                            follow = true
                            DispatchQueue.main.async { proxy.scrollTo("end", anchor: .bottom) }
                        }
                    }
                    .overlay(alignment: .bottomTrailing) {
                        if expanded && !follow {
                            Button {
                                follow = true
                                proxy.scrollTo("end", anchor: .bottom)
                            } label: {
                                Label(L("Jump to latest"), systemImage: "arrow.down.to.line")
                            }
                            .buttonStyle(.borderedProminent)
                            .controlSize(.small)
                            .padding(6)
                            .help(L("Scroll to the newest output and follow it again"))
                        }
                    }
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
