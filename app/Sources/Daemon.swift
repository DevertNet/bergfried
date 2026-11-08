import Foundation

/// Installiert/steuert bergfriedd als LaunchAgent. Binaries und Vorlage liegen im App-Bundle.
enum DaemonController {
    static let label = "de.einfach123.bergfried"
    static var plistURL: URL { URL(fileURLWithPath: NSHomeDirectory() + "/Library/LaunchAgents/\(label).plist") }
    static var domain: String { "gui/\(getuid())" }

    static func run(_ args: [String]) -> (Int32, String) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        p.arguments = args
        let pipe = Pipe()
        p.standardOutput = pipe; p.standardError = pipe
        do { try p.run() } catch { return (-1, error.localizedDescription) }
        p.waitUntilExit()
        return (p.terminationStatus, String(decoding: pipe.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self))
    }

    static func install() throws -> String {
        guard let res = Bundle.main.resourceURL else { throw APIError(message: "Kein App-Bundle") }
        let daemon = res.appendingPathComponent("bergfriedd").path
        guard FileManager.default.isExecutableFile(atPath: daemon) else { throw APIError(message: "bergfriedd fehlt im App-Bundle (build.sh nach `make build` ausführen)") }
        let home = NSHomeDirectory()
        try FileManager.default.createDirectory(atPath: home + "/.bergfried", withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let plist: [String: Any] = [
            "Label": label,
            "ProgramArguments": [daemon, "serve", "--no-dialog"],
            "RunAtLoad": true,
            "KeepAlive": true,
            "EnvironmentVariables": [
                "PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin",
                "BERGFRIED_TEMPLATE": res.appendingPathComponent("bergfried.yaml").path,
                "BERGFRIED_MCP_BIN": res.appendingPathComponent("bergfried-mcp").path,
            ],
            "StandardOutPath": home + "/.bergfried/daemon.log",
            "StandardErrorPath": home + "/.bergfried/daemon.log",
        ]
        let data = try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0)
        try FileManager.default.createDirectory(at: plistURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        try data.write(to: plistURL, options: .atomic)
        _ = run(["bootout", "\(domain)/\(label)"])
        let (code, out) = run(["bootstrap", domain, plistURL.path])
        if code != 0 { throw APIError(message: "launchctl bootstrap: \(out)") }
        _ = run(["kickstart", "-k", "\(domain)/\(label)"])
        return "Daemon installiert und gestartet."
    }

    static func uninstall() -> String {
        _ = run(["bootout", "\(domain)/\(label)"])
        try? FileManager.default.removeItem(at: plistURL)
        return "Daemon entfernt."
    }

    static var installed: Bool { FileManager.default.fileExists(atPath: plistURL.path) }
}

enum Terminal {
    static var hasITerm: Bool { FileManager.default.fileExists(atPath: "/Applications/iTerm.app") }

    static var limactl: String {
        ["/opt/homebrew/bin/limactl", "/usr/local/bin/limactl"].first { FileManager.default.isExecutableFile(atPath: $0) } ?? "limactl"
    }

    /// Öffnet iTerm (falls installiert) oder Terminal.app mit einem Befehl in der VM.
    /// Der Befehl wird als Argument an AppleScript übergeben (keine Quoting-Probleme); der VM-Name wird streng geprüft.
    static func open(vm: String, inner: String) {
        guard vm.range(of: #"^bf-[a-z][a-z0-9-]{0,30}$"#, options: .regularExpression) != nil else { return }
        let cmd = "\(limactl) shell \(vm) -- \(inner)"
        let script: String
        if hasITerm {
            script = """
            on run argv
              tell application "iTerm"
                activate
                set w to (create window with default profile)
                tell current session of w to write text (item 1 of argv)
              end tell
            end run
            """
        } else {
            script = """
            on run argv
              tell application "Terminal"
                activate
                do script (item 1 of argv)
              end tell
            end run
            """
        }
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
        p.arguments = ["-e", script, cmd]
        try? p.run()
    }

    static func attach(vm: String) { open(vm: vm, inner: "bash -lc 'tmux attach -t bf || tmux new -s bf'") }

    static func login(vm: String) {
        open(vm: vm, inner: "bash -lc \"claude auth login; echo; echo 'Fertig – dieses Fenster kann geschlossen werden. Die Agents starten automatisch neu.'\"")
    }
}
