import SwiftUI

// Replacement for @State: in SDKs without Xcode (Command Line Tools only) @State is a macro
// whose plugin (SwiftUIMacros) is missing. Same behaviour, built on @StateObject.
final class LocalBox<V>: ObservableObject {
    @Published var value: V
    init(_ v: V) { value = v }
}

@propertyWrapper
struct Local<Value>: DynamicProperty {
    @StateObject private var box: LocalBox<Value>
    init(wrappedValue: Value) { _box = StateObject(wrappedValue: LocalBox(wrappedValue)) }
    var wrappedValue: Value {
        get { box.value }
        nonmutating set { box.value = newValue }
    }
    var projectedValue: Binding<Value> {
        Binding(get: { box.value }, set: { box.value = $0 })
    }
}
