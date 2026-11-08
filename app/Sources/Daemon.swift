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
    /// Einstellung: "auto" (iTerm, falls installiert), "iterm" oder "terminal".
    static let key = "terminalApp"
    static var iTermInstalled: Bool { FileManager.default.fileExists(atPath: "/Applications/iTerm.app") }
    static var choice: String { UserDefaults.standard.string(forKey: key) ?? "auto" }

    /// Name der App, die tatsächlich benutzt wird.
    static var appName: String {
        switch choice {
        case "iterm": return iTermInstalled ? "iTerm" : "Terminal"
        case "terminal": return "Terminal"
        default: return iTermInstalled ? "iTerm" : "Terminal"
        }
    }

    static var limactl: String {
        ["/opt/homebrew/bin/limactl", "/usr/local/bin/limactl"].first { FileManager.default.isExecutableFile(atPath: $0) } ?? "limactl"
    }

    static let vmRe = #"^(bf|bergfried)-[a-z][a-z0-9-]{0,40}$"#
    static let agentRe = #"^[a-z][a-z0-9-]{0,30}$"#

    /// Öffnet die gewählte Terminal-App mit einer Zeile. Statt AppleScript (braucht Automations-Rechte und
    /// scheitert still) wird eine temporäre .command-Datei mit `open -a` gestartet; sie löscht sich selbst.
    /// VM- und Agent-Namen werden streng geprüft. Liefert eine Fehlermeldung oder nil.
    @discardableResult
    static func openLine(_ line: String, waitKey: Bool = false) -> String? {
        let dir = NSHomeDirectory() + "/.bergfried/tmp"
        let file = dir + "/cmd-\(UUID().uuidString).command"
        var body = "#!/bin/zsh\nrm -f \"$0\"\n\(line)\n"
        if waitKey { body += "echo\nread -k 1 -s \"?Beliebige Taste zum Schließen …\"\n" }
        do {
            try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            try body.write(toFile: file, atomically: true, encoding: .utf8)
            try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: file)
        } catch { return "Konnte Startskript nicht anlegen: \(error.localizedDescription)" }
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/open")
        p.arguments = ["-a", appName, file]
        let err = Pipe(); p.standardError = err
        do { try p.run() } catch { return "open: \(error.localizedDescription)" }
        p.waitUntilExit()
        if p.terminationStatus != 0 {
            let msg = String(decoding: err.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
            try? FileManager.default.removeItem(atPath: file)
            return "\(appName) konnte nicht geöffnet werden: \(msg)"
        }
        return nil
    }

    private static func checked(_ vm: String) -> Bool { vm.range(of: vmRe, options: .regularExpression) != nil }

    /// Normale Shell in der VM (nicht das Claude-Fenster).
    static func shell(vm: String) -> String? {
        guard checked(vm) else { return "Ungültiger VM-Name" }
        return openLine("\(limactl) shell \(vm)")
    }

    /// tmux der VM, direkt im Fenster dieses Agents.
    static func attach(vm: String, agent: String) -> String? {
        guard checked(vm), agent.range(of: agentRe, options: .regularExpression) != nil else { return "Ungültiger Name" }
        return openLine("\(limactl) shell \(vm) -- bash -lc 'tmux select-window -t bf:\(agent) 2>/dev/null; tmux attach -t bf || tmux new -s bf'")
    }

    static func login(vm: String) -> String? {
        guard checked(vm) else { return "Ungültiger VM-Name" }
        return openLine("\(limactl) shell \(vm) -- bash -lc 'claude auth login; echo; echo \"Fertig. Die Agents starten automatisch neu (Status in der App).\"'", waitKey: true)
    }

    /// SSH-Befehl für externe Werkzeuge (Terminal, VS Code Remote-SSH): nutzt die von Lima gepflegte ssh.config.
    static func sshCommand(vm: String) -> String? {
        guard checked(vm) else { return nil }
        return "ssh -F ~/.lima/\(vm)/ssh.config lima-\(vm)"
    }
}
