import Foundation

struct DaemonStatus: Codable, Equatable {
    var version: String
    var baseReady: Bool
    var baseName: String
    var templateVersion: Int
    var indexReady: Bool
    var indexWaiting: Bool?
    var upstreamAgent: Bool
    var sandboxes: Int
    var updateAvailable: Bool?
    var updateVersion: String?
    var updateSize: Int64?
}

struct LogInfo: Codable, Equatable { var path: String; var text: String }

struct UpdateInfo: Codable {
    var current: String
    var channel: String
    var latest: String
    var available: Bool
    var notes: String
    var url: String
    var size: Int64?
    var checkedUnix: Int64
    var error: String?
    var updatable: Bool
    var reason: String?
}

struct AgentInfo: Codable, Hashable {
    var name: String
    var worktree: String
    var runtime: String?
    var window: Bool
    var running: Bool?
    var rc: Bool
    var rcUrl: String
    var rcAction: String   // "", "activating", "failed", "waiting", "working"
    var isOpenCode: Bool { runtime == "opencode" }
}

struct SandboxInfo: Codable, Identifiable, Hashable {
    var name: String
    var vm: String
    var status: String
    var repo: String
    var templateVersion: Int
    var createdUnix: Int64
    var cpus: Int
    var memory: Int64
    var disk: Int64
    var agents: [AgentInfo]
    var activeGrants: Int
    var pendingRequests: Int
    var claudeLoggedIn: Bool?
    var opencodeLoggedIn: Bool?
    var claudeInstalled: Bool?
    var opencodeInstalled: Bool?
    var opencodeRunning: Bool?
    var opencodePort: Int?
    var hostDiskUsed: Int64
    var guestDiskUsed: Int64
    var guestDiskSize: Int64
    var probeError: String?
    var hasGithubToken: Bool
    var id: String { name }
    var running: Bool { status == "Running" }
    var usesClaude: Bool { agents.contains { !$0.isOpenCode } }
    var usesOpenCode: Bool { agents.contains { $0.isOpenCode } }
}

struct SSHInfo: Codable, Hashable {
    var hostKeyFingerprints: [String]
    var source: String
    var defaultKey: String
    var defaultUser: String
    var requestedUser: String
    var suggestedMinutes: Int
}

struct GitHubDetail: Codable, Hashable {
    var repo: String
    var hasToken: Bool
    var link: String
    var hint: String
}

struct RequestInfo: Codable, Identifiable, Hashable {
    var id: Int64
    var sandbox: String
    var agent: String
    var kind: String
    var path: String
    var reason: String
    var status: String
    var createdUnix: Int64
    var bytes: Int64
    var files: Int64
    var large: Bool
    var ssh: SSHInfo?
    var github: GitHubDetail?
    var pending: Bool { status == "pending" }
    // Request and grant ids come from different tables and can be equal: give the list rows distinct identities.
    var listID: String { "request-\(id)" }
    var historyID: String { "history-\(id)" }
}

struct GrantInfo: Codable, Identifiable, Hashable {
    var id: Int64
    var sandbox: String
    var agent: String
    var host: String
    var keyFingerprint: String
    var user: String
    var expiresUnix: Int64
    var perConnection: Bool
    var revoked: Bool
    var active: Bool
    var hostKeyFingerprints: [String]
    var listID: String { "grant-\(id)" }
}

struct AuditEntry: Codable, Identifiable, Hashable {
    var tsUnix: Int64
    var actor: String
    var event: String
    var sandbox: String
    var agent: String
    var detail: String
    var id: String { "\(tsUnix)-\(actor)-\(event)-\(sandbox)-\(agent)-\(detail.hashValue)" }
}

struct KeyInfo: Codable, Identifiable, Hashable {
    var fingerprint: String
    var comment: String
    var type: String
    var loaded: Bool?      // in the ssh-agent (no passphrase needed)
    var encrypted: Bool?   // key file that needs a passphrase
    var id: String { fingerprint }
}
struct KeysResponse: Codable { var keys: [KeyInfo]; var error: String? }

struct Blocklist: Codable, Equatable { var paths: [String]; var names: [String] }

struct Job: Codable { var id: Int64; var kind: String; var state: String; var step: String?; var error: String?; var log: [String]? }
struct Message: Codable { var message: String? }

struct DecideBody: Encodable { var id: Int64; var allow: Bool; var confirmLarge: Bool = false; var note: String = "" }
struct SSHDecideBody: Encodable {
    var id: Int64
    var allow: Bool
    var minutes: Int
    var keyFingerprint: String
    var user: String
    var allAgents: Bool
    var perConnection: Bool
    var trustUnknownHost: Bool
    var passphrase: String = ""   // only for a key that is not loaded in the agent; never stored
    var note: String = ""         // optional message shown to the agent with the decision
}
struct IDBody: Encodable { var id: Int64 }
struct ChannelBody: Encodable { var channel: String }
struct ExtendBody: Encodable { var id: Int64; var minutes: Int }
struct NameBody: Encodable { var name: String; var repo: String = ""; var force: Bool = false; var importPath: String = ""; var githubToken: String = ""; var runtime: String = "claude" }
struct PathBody: Encodable { var path: String }
struct ImportPreview: Codable { var files: Int64; var bytes: Int64 }
struct YAMLBody: Encodable { var yaml: String }
struct TemplateInfo: Codable {
    var yaml: String
    var revision: Int
    var baseName: String
    var baseReady: Bool
    var path: String
    var warnings: [String]
}
struct AgentBody: Encodable { var sandbox: String; var agent: String; var runtime: String = "claude" }
struct AgentRemoveBody: Encodable { var sandbox: String; var agent: String; var deleteDir: Bool; var force: Bool }
struct PairLink: Codable { var url: String }

func formatBytes(_ b: Int64) -> String { ByteCountFormatter.string(fromByteCount: b, countStyle: .file) }

struct GitHubDecideBody: Encodable { var id: Int64; var allow: Bool; var token: String; var note: String = "" }
struct GitHubLink: Codable { var url: String; var hint: String }
