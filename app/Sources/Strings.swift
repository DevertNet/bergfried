import Foundation
import SwiftUI

/// Localised text with format arguments (`%@` for strings, `%lld` for integers).
/// SwiftUI literals such as `Text("…")` or `Button("…")` are localised automatically from `Localizable.strings`;
/// use `L` for strings built in code (banners, errors, interpolations).
func L(_ key: String, _ args: CVarArg...) -> String {
    let format = NSLocalizedString(key, bundle: .main, comment: "")
    return args.isEmpty ? format : String(format: format, arguments: args)
}

/// Like `L`, but returns `Text` and renders Markdown (`**bold**`) in the localised string.
func LT(_ key: String, _ args: CVarArg...) -> Text {
    let format = NSLocalizedString(key, bundle: .main, comment: "")
    return Text(LocalizedStringKey(args.isEmpty ? format : String(format: format, arguments: args)))
}
