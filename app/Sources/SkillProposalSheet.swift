import SwiftUI

/// Review of a skill proposal: the complete diff per file, an optional note for the assistant and Allow/Deny.
/// The assistant's text (reason, paths, diff) is shown as unchecked data.
struct SkillProposalSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) var dismiss
    let r: RequestInfo
    @Local private var note = ""
    @Local private var error: String?
    @Local private var busy = false

    var title: String { L("Skill proposal for %@", r.origin) }

    func opLabel(_ op: String) -> String {
        switch op {
        case "create": return L("Create")
        case "modify": return L("Change")
        case "delete": return L("Delete")
        default: return L("No change")
        }
    }

    func lineColor(_ line: String) -> Color {
        if line.hasPrefix("@@") { return .secondary }
        if line.hasPrefix("+") { return .green }
        if line.hasPrefix("-") { return .red }
        return .primary
    }

    /// One line per Text in a LazyVStack: large diffs stay fast and are shown completely.
    func diffView(_ diff: String) -> some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                ForEach(Array(diff.split(separator: "\n", omittingEmptySubsequences: false).enumerated()), id: \.offset) { _, part in
                    let line = String(part)
                    Text(line)
                        .font(.caption.monospaced())
                        .foregroundStyle(lineColor(line))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }.padding(6)
        }
        .frame(minHeight: 90, idealHeight: 200, maxHeight: 260)
        .background(Color.secondary.opacity(0.08), in: RoundedRectangle(cornerRadius: 6))
    }

    var sheetContent: some View {
        VStack(alignment: .leading, spacing: 18) {
            VStack(alignment: .leading, spacing: 6) {
                Text(L("Assistant's reason (unchecked): %@", r.reason.isEmpty ? "–" : r.reason))
                    .font(.callout).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
            }

            HStack(alignment: .top, spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                LT("These changes apply to the shared skills of **all** sandboxes. Skills are instructions for the assistants and may contain scripts that run in the VMs – read the whole diff before you allow.")
                    .font(.callout).fixedSize(horizontal: false, vertical: true)
            }.padding(10).frame(maxWidth: .infinity, alignment: .leading)
                .background(.orange.opacity(0.1), in: RoundedRectangle(cornerRadius: 8))

            ForEach(r.skill ?? [], id: \.self) { f in
                VStack(alignment: .leading, spacing: 6) {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(opLabel(f.op)).font(.caption.bold()).foregroundStyle(.secondary)
                        Text(f.path).font(.callout.monospaced()).textSelection(.enabled)
                    }
                    diffView(f.diff)
                }
            }

            VStack(alignment: .leading, spacing: 6) {
                Text("Note for the assistant if you deny (optional)").font(.callout.bold())
                TextEditor(text: $note)
                    .font(.callout)
                    .frame(minHeight: 54, maxHeight: 80)
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.secondary.opacity(0.3)))
            }
        }.padding(.trailing, 6)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text(title).font(.title3.bold())
                Spacer()
            }
            Divider().padding(.vertical, 12)

            ViewThatFits(in: .vertical) {
                sheetContent
                ScrollView { sheetContent }
            }

            if let error {
                Text(error).foregroundStyle(.red).font(.callout)
                    .fixedSize(horizontal: false, vertical: true).textSelection(.enabled).padding(.top, 8)
            }
            Divider().padding(.vertical, 12)
            HStack(spacing: 10) {
                if busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction).disabled(busy)
                Button("Deny", role: .destructive) { decide(false) }.disabled(busy)
                Button("Allow") { decide(true) }.buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction).disabled(busy)
            }
        }
        .padding(20).frame(width: 720).frame(maxHeight: 720)
    }

    func decide(_ allow: Bool) {
        busy = true
        error = nil
        // The note belongs to a denial ("Note for the assistant if you deny").
        let n = allow ? "" : note
        Task {
            let e = await model.decideSkill(id: r.id, allow: allow, note: n)
            busy = false
            if let e { error = e } else { dismiss() }
        }
    }
}
