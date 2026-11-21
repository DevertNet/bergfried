import SwiftUI

/// One colour per kind of access, used everywhere the kind is shown (request cards, decision sheets, active grants, history):
/// SSH is violet, GitHub (git grants, GitHub actions, legacy token) is green. Other kinds (files, skills) stay neutral.
enum KindStyle {
    case ssh, github

    init?(requestKind k: String) {
        switch k {
        case "ssh": self = .ssh
        case "gitgrant", "ghaction", "ghtoken": self = .github
        default: return nil
        }
    }

    var label: String { self == .ssh ? "SSH" : "GitHub" }
    var icon: String { self == .ssh ? "terminal" : "arrow.triangle.branch" }

    /// Solid colour: window border, header, prominent buttons.
    var strong: Color { self == .ssh ? .adaptive(0x534AB7, 0x6B63D0) : .adaptive(0x0F6E56, 0x1D9E75) }
    /// Card background.
    var tint: Color { self == .ssh ? .adaptive(0xEEEDFE, 0x2A2660) : .adaptive(0xE1F5EE, 0x0B3D31) }
    /// Card border.
    var border: Color { self == .ssh ? .adaptive(0xAFA9EC, 0x534AB7) : .adaptive(0x5DCAA5, 0x0F6E56) }
    /// Text on the tint.
    var text: Color { self == .ssh ? .adaptive(0x3C3489, 0xCECBF6) : .adaptive(0x085041, 0x9FE1CB) }
}

extension Color {
    /// A colour with one value for light and one for dark appearance (0xRRGGBB).
    static func adaptive(_ light: UInt32, _ dark: UInt32) -> Color {
        Color(nsColor: NSColor(name: nil) { appearance in
            let isDark = appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua
            let v = isDark ? dark : light
            return NSColor(red: CGFloat((v >> 16) & 0xFF) / 255, green: CGFloat((v >> 8) & 0xFF) / 255, blue: CGFloat(v & 0xFF) / 255, alpha: 1)
        })
    }
}

/// Small label "SSH" / "GitHub".
struct KindPill: View {
    let kind: KindStyle
    var onStrong = false // on the solid header: light pill
    var body: some View {
        Text(kind.label).font(.caption.bold())
            .padding(.horizontal, 8).padding(.vertical, 2)
            .foregroundStyle(onStrong ? kind.strong : kind.text)
            .background(onStrong ? Color.white : kind.border.opacity(0.5), in: Capsule())
    }
}

/// Decision window: thick border and solid header in the colour of the kind.
struct KindFrame<Content: View>: View {
    let kind: KindStyle
    let title: String
    @ViewBuilder var content: Content
    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 10) {
                Image(systemName: kind.icon).font(.title2)
                Text(title).font(.title3.bold())
                Spacer()
                KindPill(kind: kind, onStrong: true)
            }
            .foregroundStyle(.white)
            .padding(.horizontal, 16).padding(.vertical, 12)
            .background(kind.strong)
            content
        }
        .tint(kind.strong)
        .overlay(Rectangle().strokeBorder(kind.strong, lineWidth: 5).allowsHitTesting(false))
    }
}

extension View {
    /// Card of a request or grant: tinted background and a border in the colour of the kind (no-op for other kinds).
    @ViewBuilder func kindCard(_ kind: KindStyle?) -> some View {
        if let kind {
            self.padding(10)
                .background(kind.tint, in: RoundedRectangle(cornerRadius: 10))
                .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(kind.border, lineWidth: 2))
                .tint(kind.strong)
                .listRowSeparator(.hidden)
        } else {
            self
        }
    }
}
