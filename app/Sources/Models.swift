import Foundation

struct DaemonStatus: Codable, Equatable {
    var version: String
    var baseReady: Bool
    var baseName: String
    var templateVersion: Int
    var indexReady: Bool
    var upstreamAgent: Bool
    var sessions: Int
}

struct AgentInfo: Codable, Hashable {
    var name: String
    var worktree: String
    var window: Bool
    var rc: Bool
    var rcUrl: String
    var rcAction: String   // "", "activating", "failed"
}

struct SessionInfo: Codable, Identifiable, Hashable {
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
    var hostDiskUsed: Int64
    var guestDiskUsed: Int64
    var guestDiskSize: Int64
    var probeError: String?
    var hasGithubToken: Bool
    var id: String { name }
    var running: Bool { status == "Running" }
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
    var session: String
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
}

struct GrantInfo: Codable, Identifiable, Hashable {
    var id: Int64
    var session: String
    var agent: String
    var host: String
    var keyFingerprint: String
    var user: String
    var expiresUnix: Int64
    var perConnection: Bool
    var revoked: Bool
    var active: Bool
    var hostKeyFingerprints: [String]
}

struct AuditEntry: Codable, Identifiable, Hashable {
    var tsUnix: Int64
    var actor: String
    var event: String
    var session: String
    var agent: String
    var detail: String
    var id: String { "\(tsUnix)-\(actor)-\(event)-\(session)-\(agent)-\(detail.hashValue)" }
}

struct KeyInfo: Codable, Identifiable, Hashable {
    var fingerprint: String
    var comment: String
    var type: String
    var id: String { fingerprint }
}
struct KeysResponse: Codable { var keys: [KeyInfo]; var error: String? }

struct Blocklist: Codable, Equatable { var paths: [String]; var names: [String] }

struct Job: Codable { var id: Int64; var kind: String; var state: String; var step: String?; var error: String? }
struct Message: Codable { var message: String? }

struct DecideBody: Encodable { var id: Int64; var allow: Bool; var confirmLarge: Bool = false }
struct SSHDecideBody: Encodable {
    var id: Int64
    var allow: Bool
    var minutes: Int
    var keyFingerprint: String
    var user: String
    var allAgents: Bool
    var perConnection: Bool
    var trustUnknownHost: Bool
}
struct IDBody: Encodable { var id: Int64 }
struct ExtendBody: Encodable { var id: Int64; var minutes: Int }
struct NameBody: Encodable { var name: String; var repo: String = ""; var force: Bool = false; var importPath: String = ""; var githubToken: String = "" }
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
struct AgentBody: Encodable { var session: String; var agent: String }
struct AgentRemoveBody: Encodable { var session: String; var agent: String; var deleteDir: Bool; var force: Bool }

func formatBytes(_ b: Int64) -> String { ByteCountFormatter.string(fromByteCount: b, countStyle: .file) }

struct GitHubDecideBody: Encodable { var id: Int64; var allow: Bool; var token: String }
struct GitHubLink: Codable { var url: String; var hint: String }
