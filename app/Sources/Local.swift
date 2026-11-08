import SwiftUI

// Ersatz für @State: In SDKs ohne Xcode (nur Command Line Tools) ist @State ein Makro,
// dessen Plugin (SwiftUIMacros) fehlt. Gleiches Verhalten, gebaut auf @StateObject.
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
