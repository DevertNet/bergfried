import Foundation

struct APIError: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

/// Minimaler HTTP/1.1-Client über einen Unix-Socket (BSD-Sockets, keine Abhängigkeiten).
final class UnixHTTP: @unchecked Sendable {
    let path: String
    init(path: String) { self.path = path }

    func request(_ method: String, _ target: String, body: Data? = nil) async throws -> (Int, Data) {
        let path = self.path
        return try await withCheckedThrowingContinuation { (cont: CheckedContinuation<(Int, Data), Error>) in
            DispatchQueue.global().async {
                cont.resume(with: Result { try Self.exchange(path: path, method: method, target: target, body: body) })
            }
        }
    }

    static func exchange(path: String, method: String, target: String, body: Data?) throws -> (Int, Data) {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw APIError(message: "socket(): \(String(cString: strerror(errno)))") }
        defer { close(fd) }
        var one: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))
        var tv = timeval(tv_sec: 60, tv_usec: 0)
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let bytes = Array(path.utf8)
        guard bytes.count < MemoryLayout.size(ofValue: addr.sun_path) else { throw APIError(message: "Socket-Pfad zu lang") }
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            raw.copyBytes(from: bytes)
            raw[bytes.count] = 0
        }
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        guard rc == 0 else { throw APIError(message: "Daemon nicht erreichbar (\(String(cString: strerror(errno))))") }
        var head = "\(method) \(target) HTTP/1.1\r\nHost: bergfried\r\nAccept: application/json\r\nConnection: close\r\n"
        if let body { head += "Content-Type: application/json\r\nContent-Length: \(body.count)\r\n" }
        head += "\r\n"
        var out = Data(head.utf8)
        if let body { out.append(body) }
        try out.withUnsafeBytes { (p: UnsafeRawBufferPointer) in
            var sent = 0
            while sent < p.count {
                let n = send(fd, p.baseAddress! + sent, p.count - sent, 0)
                if n <= 0 { throw APIError(message: "Senden fehlgeschlagen") }
                sent += n
            }
        }
        var buf = Data()
        var chunk = [UInt8](repeating: 0, count: 1 << 16)
        while true {
            let n = recv(fd, &chunk, chunk.count, 0)
            if n < 0 { throw APIError(message: "Zeitüberschreitung beim Lesen") }
            if n == 0 { break }
            buf.append(chunk, count: n)
        }
        return try parse(buf)
    }

    static func parse(_ data: Data) throws -> (Int, Data) {
        guard let sep = data.range(of: Data("\r\n\r\n".utf8)) else { throw APIError(message: "Ungültige Antwort des Daemons") }
        let head = String(decoding: data[..<sep.lowerBound], as: UTF8.self)
        var body = Data(data[sep.upperBound...])
        let lines = head.components(separatedBy: "\r\n")
        let parts = (lines.first ?? "").split(separator: " ")
        guard parts.count >= 2, let code = Int(parts[1]) else { throw APIError(message: "Ungültige Statuszeile") }
        let chunked = lines.dropFirst().contains { $0.lowercased().hasPrefix("transfer-encoding:") && $0.lowercased().contains("chunked") }
        if chunked { body = try dechunk(body) }
        return (code, body)
    }

    static func dechunk(_ d: Data) throws -> Data {
        var out = Data()
        var i = d.startIndex
        while i < d.endIndex {
            guard let eol = d[i...].range(of: Data("\r\n".utf8)) else { break }
            let sizeStr = String(decoding: d[i..<eol.lowerBound], as: UTF8.self).split(separator: ";").first.map(String.init) ?? "0"
            guard let size = Int(sizeStr.trimmingCharacters(in: .whitespaces), radix: 16) else { throw APIError(message: "Ungültige Chunk-Größe") }
            if size == 0 { break }
            let start = eol.upperBound
            guard let end = d.index(start, offsetBy: size, limitedBy: d.endIndex) else { throw APIError(message: "Chunk abgeschnitten") }
            out.append(d[start..<end])
            i = d.index(end, offsetBy: 2, limitedBy: d.endIndex) ?? d.endIndex
        }
        return out
    }
}

final class API: @unchecked Sendable {
    let http: UnixHTTP
    private let dec: JSONDecoder = { let d = JSONDecoder(); d.keyDecodingStrategy = .convertFromSnakeCase; return d }()
    private let enc: JSONEncoder = { let e = JSONEncoder(); e.keyEncodingStrategy = .convertToSnakeCase; return e }()

    init(socket: String) { http = UnixHTTP(path: socket) }

    static var defaultSocket: String {
        (ProcessInfo.processInfo.environment["BERGFRIED_HOME"] ?? NSHomeDirectory() + "/.bergfried") + "/app.sock"
    }

    func raw(_ method: String, _ target: String, body: Data? = nil) async throws -> Data {
        let (code, data) = try await http.request(method, target, body: body)
        if code >= 400 {
            let msg = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["error"] as? String
            throw APIError(message: msg ?? "Fehler \(code)")
        }
        return data
    }

    func get<T: Decodable>(_ target: String, as: T.Type = T.self) async throws -> T {
        try dec.decode(T.self, from: try await raw("GET", target))
    }

    func send<T: Decodable, B: Encodable>(_ method: String, _ target: String, _ body: B, as: T.Type = T.self) async throws -> T {
        try dec.decode(T.self, from: try await raw(method, target, body: try enc.encode(body)))
    }

    func post<B: Encodable>(_ target: String, _ body: B) async throws {
        _ = try await raw("POST", target, body: try enc.encode(body))
    }
}
