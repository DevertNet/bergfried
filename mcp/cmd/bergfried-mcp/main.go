// bergfried-mcp: stdio-MCP-Server in der VM. Spricht über den weitergeleiteten Unix-Socket
// mit bergfriedd. Liefert nur Metadaten; Dateiinhalte kommen erst nach Freigabe in shared/in.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type app struct{ c pb.SessionAPIClient }

func (a *app) ctx(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "x-bergfried-agent", os.Getenv("BERGFRIED_AGENT"))
}

type Entry struct {
	Path  string `json:"path" jsonschema:"Pfad relativ zum Home"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime_unix"`
}
type Entries struct {
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

func conv(es []*pb.FileEntry) []Entry {
	out := []Entry{}
	for _, e := range es {
		out = append(out, Entry{e.Path, e.IsDir, e.Size, e.MtimeUnix})
	}
	return out
}

type listIn struct {
	Path    string `json:"path" jsonschema:"Ordner relativ zum Home des Nutzers (leer = Home)"`
	Pattern string `json:"pattern,omitempty" jsonschema:"optionaler Glob auf den Dateinamen, z. B. *.pdf"`
}
type searchIn struct {
	Query string `json:"query" jsonschema:"Teil des Dateinamens oder Pfads"`
}
type reqIn struct {
	Path   string `json:"path" jsonschema:"Pfad relativ zum Home des Nutzers, z. B. Documents/bericht.pdf"`
	Reason string `json:"reason" jsonschema:"Warum wird die Datei gebraucht? Wird dem Nutzer angezeigt."`
}
type Result struct {
	Verdict string `json:"verdict"`
	VMPath  string `json:"vm_path,omitempty"`
	Message string `json:"message"`
}

func (a *app) list(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, Entries, error) {
	r, err := a.c.ListFiles(a.ctx(ctx), &pb.ListFilesRequest{Path: in.Path, Pattern: in.Pattern})
	if err != nil {
		return nil, Entries{}, err
	}
	return nil, Entries{conv(r.Entries), r.Truncated}, nil
}

func (a *app) search(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, Entries, error) {
	r, err := a.c.SearchFiles(a.ctx(ctx), &pb.SearchFilesRequest{Query: in.Query})
	if err != nil {
		return nil, Entries{}, err
	}
	return nil, Entries{conv(r.Entries), r.Truncated}, nil
}

func res(r *pb.AccessResult) Result {
	return Result{Verdict: map[pb.Verdict]string{
		pb.Verdict_VERDICT_ALLOWED: "allowed", pb.Verdict_VERDICT_DENIED: "denied",
		pb.Verdict_VERDICT_BLOCKED: "blocked", pb.Verdict_VERDICT_EXPIRED: "expired"}[r.Verdict],
		VMPath: r.VmPath, Message: r.Message}
}

func (a *app) file(ctx context.Context, _ *mcp.CallToolRequest, in reqIn) (*mcp.CallToolResult, Result, error) {
	r, err := a.c.RequestFile(a.ctx(ctx), &pb.AccessRequest{Path: in.Path, Reason: in.Reason})
	if err != nil {
		return nil, Result{}, err
	}
	return nil, res(r), nil
}

func (a *app) folder(ctx context.Context, _ *mcp.CallToolRequest, in reqIn) (*mcp.CallToolResult, Result, error) {
	r, err := a.c.RequestFolder(a.ctx(ctx), &pb.AccessRequest{Path: in.Path, Reason: in.Reason})
	if err != nil {
		return nil, Result{}, err
	}
	return nil, res(r), nil
}

type sshIn struct {
	Host    string `json:"host" jsonschema:"Zielhost, optional mit :port, z. B. server.example.com"`
	User    string `json:"user,omitempty" jsonschema:"Benutzername auf dem Zielhost, falls bekannt (kleingeschrieben). Der Nutzer sieht ihn und kann ihn ändern."`
	Minutes int    `json:"minutes,omitempty" jsonschema:"gewünschte Dauer in Minuten (nur ein Vorschlag, der Nutzer entscheidet)"`
	Reason  string `json:"reason" jsonschema:"Wofür wird der SSH-Zugriff gebraucht? Wird dem Nutzer angezeigt."`
}
type sshOut struct {
	Verdict string `json:"verdict"`
	Message string `json:"message"`
	Expires int64  `json:"expires_unix,omitempty"`
	User    string `json:"user,omitempty"`
}

func (a *app) ssh(ctx context.Context, _ *mcp.CallToolRequest, in sshIn) (*mcp.CallToolResult, sshOut, error) {
	r, err := a.c.RequestSSH(a.ctx(ctx), &pb.SSHAccessRequest{Host: in.Host, User: in.User, Minutes: int32(in.Minutes), Reason: in.Reason})
	if err != nil {
		return nil, sshOut{}, err
	}
	v := res(&pb.AccessResult{Verdict: r.Verdict})
	return nil, sshOut{Verdict: v.Verdict, Message: r.Message, Expires: r.ExpiresUnix, User: r.User}, nil
}

// instructions erscheinen im Kontext von Claude Code (MCP-Server-Hinweise).
const instructions = `Du läufst in einer isolierten Bergfried-VM. "Mein Computer", "mein Mac", "meine Dateien", "mein Rechner", "mein Home", Dokumente, Downloads oder Desktop meinen den Mac des Nutzers AUSSERHALB dieser VM; er ist hier nicht eingebunden.
Dateien des Nutzers findest du nur mit diesen Tools: list_files und search_files (nur Namen, Größe, Datum; Pfade relativ zum Home). Nutze sie zuerst; suche solche Dateien nie mit find/ls in der VM.
Inhalte bekommst du nur mit request_file bzw. request_folder (kurze, ehrliche Begründung angeben); nach Zustimmung liegt eine Kopie unter vm_path. Verweigerte oder gesperrte Anfragen respektierst du und umgehst sie nicht.
Für einen Server per SSH rufst du vorher request_ssh auf (host, user falls bekannt, reason), danach funktioniert ssh user@host während der Freigabe. Es gibt keinen Zugriff auf private Schlüssel.
Sage nie, du hättest keinen Zugriff auf den Computer des Nutzers, bevor du diese Tools versucht hast.`

// sshHook läuft als `Match exec` aus /etc/ssh/ssh_config.d bei jedem ssh-Start in der VM. Es meldet den Versuch dem Daemon
// (der bei Bedarf eine Anfrage in der App anlegt) und endet IMMER mit Status 1, damit der Match nie greift und ssh unverändert weiterläuft.
func sshHook(args []string) {
	defer os.Exit(1)
	if len(args) < 3 {
		return
	}
	sock := os.Getenv("BERGFRIED_SOCKET")
	if sock == "" {
		sock = fmt.Sprintf("/run/user/%d/bergfried.sock", os.Getuid())
	}
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := pb.NewSessionAPIClient(conn).SSHAttempt(ctx, &pb.SSHAttemptRequest{Host: args[0], Port: args[1], User: args[2]})
	if err == nil && r.Message != "" {
		fmt.Fprintln(os.Stderr, r.Message)
	}
}

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "ssh-hook" {
		sshHook(os.Args[2:])
		return
	}
	sock := os.Getenv("BERGFRIED_SOCKET")
	if sock == "" {
		sock = fmt.Sprintf("/run/user/%d/bergfried.sock", os.Getuid())
	}
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	a := &app{c: pb.NewSessionAPIClient(conn)}
	s := mcp.NewServer(&mcp.Implementation{Name: "bergfried", Version: "0.5.0"}, &mcp.ServerOptions{Instructions: instructions})
	mcp.AddTool(s, &mcp.Tool{Name: "list_files", Description: "Listet Dateinamen, Größe und Änderungsdatum eines Ordners im Home des Nutzers (nur Metadaten, keine Inhalte). Gesperrte Bereiche erscheinen nicht."}, a.list)
	mcp.AddTool(s, &mcp.Tool{Name: "search_files", Description: "Sucht nach Dateinamen im Home des Nutzers (nur Metadaten)."}, a.search)
	mcp.AddTool(s, &mcp.Tool{Name: "request_file", Description: "Bittet den Nutzer um eine Datei. Wartet auf dessen Entscheidung (bis zu 10 Minuten). Bei Zustimmung liegt eine Kopie unter vm_path (read-only). Gesperrte Pfade werden ohne Rückfrage abgelehnt."}, a.file)
	mcp.AddTool(s, &mcp.Tool{Name: "request_folder", Description: "Wie request_file, aber für einen ganzen Ordner. Große Ordner (>100 MB oder >500 Dateien) brauchen eine zweite Bestätigung."}, a.folder)
	mcp.AddTool(s, &mcp.Tool{Name: "request_ssh", Description: "Bittet den Nutzer um eine zeitlich begrenzte SSH-Freigabe für einen Host. Wartet auf die Entscheidung (bis zu 10 Minuten). Bei Zustimmung funktioniert `ssh <user>@<host>` bis zum angegebenen Ablauf; danach wird abgelehnt und laufende Verbindungen werden beendet. Es gibt keinen Zugriff auf private Keys."}, a.ssh)
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
