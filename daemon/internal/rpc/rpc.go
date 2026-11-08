// Package rpc: gRPC-Server. Pro laufender Session ein eigener Unix-Socket (Identität = Socket),
// dazu ein Admin-Socket für CLI/App. Alle Sockets 0600 in 0700-Verzeichnissen.
package rpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/access"
	"github.com/DevertNet/bergfried/daemon/internal/index"
	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/store"
	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const AgentHeader = "x-bergfried-agent"

var agentRe = regexp.MustCompile(`^[a-z][a-z0-9/-]{0,63}$`)

func agentFrom(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(AgentHeader); len(v) > 0 && agentRe.MatchString(v[0]) {
			return v[0]
		}
	}
	return "unknown"
}

// ListenUnix: Verzeichnis 0700, Socket 0600, alter Socket wird ersetzt.
func ListenUnix(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

type sessionServer struct {
	pb.UnimplementedSessionAPIServer
	session string
	svc     *access.Service
	st      *store.Store
}

// agent: Name ohne das "<session>/"-Präfix, das die VM mitschickt.
func (s *sessionServer) agent(ctx context.Context) string {
	return strings.TrimPrefix(agentFrom(ctx), s.session+"/")
}

func (s *sessionServer) audit(ctx context.Context, event, detail string) {
	if err := s.st.Audit("agent", event, s.session, s.agent(ctx), detail); err != nil {
		fmt.Fprintln(os.Stderr, "WARNUNG: Audit-Log nicht geschrieben:", err)
	}
}

func toPB(es []index.Entry) []*pb.FileEntry {
	out := make([]*pb.FileEntry, 0, len(es))
	for _, e := range es {
		out = append(out, &pb.FileEntry{Path: e.Path, IsDir: e.IsDir, Size: e.Size, MtimeUnix: e.MTime})
	}
	return out
}

func (s *sessionServer) ListFiles(ctx context.Context, r *pb.ListFilesRequest) (*pb.ListFilesResponse, error) {
	s.audit(ctx, "files.list", r.Path)
	es, trunc, err := s.svc.Ix.List(r.Path, r.Pattern)
	if errors.Is(err, index.ErrNotReady) {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	if err != nil {
		// Existenz gesperrter Pfade nicht verraten.
		return nil, status.Error(codes.NotFound, "Pfad nicht gefunden")
	}
	return &pb.ListFilesResponse{Entries: toPB(es), Truncated: trunc}, nil
}

func (s *sessionServer) SearchFiles(ctx context.Context, r *pb.SearchFilesRequest) (*pb.SearchFilesResponse, error) {
	s.audit(ctx, "files.search", r.Query)
	if !s.svc.Ix.Ready() {
		return nil, status.Error(codes.Unavailable, index.ErrNotReady.Error())
	}
	es, trunc := s.svc.Ix.Search(r.Query)
	return &pb.SearchFilesResponse{Entries: toPB(es), Truncated: trunc}, nil
}

func (s *sessionServer) RequestFile(ctx context.Context, r *pb.AccessRequest) (*pb.AccessResult, error) {
	return s.svc.Request(ctx, s.session, s.agent(ctx), false, r.Path, r.Reason)
}

func (s *sessionServer) RequestFolder(ctx context.Context, r *pb.AccessRequest) (*pb.AccessResult, error) {
	return s.svc.Request(ctx, s.session, s.agent(ctx), true, r.Path, r.Reason)
}

type adminServer struct {
	pb.UnimplementedAdminServer
	svc *access.Service
	st  *store.Store
}

func (a *adminServer) ListRequests(_ context.Context, r *pb.ListRequestsRequest) (*pb.ListRequestsResponse, error) {
	rs, err := a.st.ListRequests(r.OnlyPending, 200)
	if err != nil {
		return nil, err
	}
	out := &pb.ListRequestsResponse{}
	for _, x := range rs {
		out.Requests = append(out.Requests, &pb.Request{Id: x.ID, Session: x.Session, Agent: x.Agent, Kind: x.Kind, Path: x.Path,
			Reason: x.Reason, Status: x.Status, CreatedUnix: x.Created.Unix(), TotalBytes: x.Bytes, TotalFiles: x.Files})
	}
	return out, nil
}

func (a *adminServer) Decide(_ context.Context, r *pb.DecideRequest) (*pb.DecideResponse, error) {
	msg, err := a.svc.Decide(r.Id, r.Allow, r.ConfirmLarge)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &pb.DecideResponse{Message: msg}, nil
}

// Daemon verwaltet Admin-Socket und Session-Sockets.
type Daemon struct {
	St        *store.Store
	Svc       *access.Service
	AdminSock string
	L         lima.Client

	mu      sync.Mutex
	servers map[string]*grpc.Server // Session -> Server
}

func SessionSocket(vm string) (string, error) {
	d, err := lima.InstanceDir(vm)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "sock", "bergfried.sock"), nil
}

func (d *Daemon) startAdmin() error {
	l, err := ListenUnix(d.AdminSock)
	if err != nil {
		return err
	}
	g := grpc.NewServer()
	pb.RegisterAdminServer(g, &adminServer{svc: d.Svc, st: d.St})
	go g.Serve(l)
	return nil
}

func (d *Daemon) startSession(name, vm string) error {
	sock, err := SessionSocket(vm)
	if err != nil {
		return err
	}
	l, err := ListenUnix(sock)
	if err != nil {
		return err
	}
	g := grpc.NewServer()
	pb.RegisterSessionAPIServer(g, &sessionServer{session: name, svc: d.Svc, st: d.St})
	go g.Serve(l)
	d.servers[name] = g
	return nil
}

// Reconcile: für jede laufende Session einen Socket, für beendete/gelöschte abbauen.
func (d *Daemon) Reconcile(ctx context.Context) error {
	ss, err := d.St.ListSessions()
	if err != nil {
		return err
	}
	vms, err := d.L.List(ctx)
	if err != nil {
		return err
	}
	running := map[string]bool{}
	for _, v := range vms {
		running[v.Name] = v.Status == "Running"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	want := map[string]bool{}
	for _, s := range ss {
		if running[s.VM] {
			want[s.Name] = true
			if _, ok := d.servers[s.Name]; !ok {
				if err := d.startSession(s.Name, s.VM); err != nil {
					fmt.Fprintln(os.Stderr, "Socket für", s.Name, "fehlgeschlagen:", err)
					continue
				}
				_ = d.St.Audit("daemon", "socket.open", s.Name, "", "")
			}
		}
	}
	for name, g := range d.servers {
		if !want[name] {
			g.Stop()
			delete(d.servers, name)
			_ = d.St.Audit("daemon", "socket.close", name, "", "")
		}
	}
	return nil
}

func (d *Daemon) Run(ctx context.Context) error {
	d.servers = map[string]*grpc.Server{}
	if err := d.St.ExpirePending(); err != nil {
		return err
	}
	if err := d.startAdmin(); err != nil {
		return err
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		if err := d.Reconcile(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "Reconcile:", err)
		}
		select {
		case <-ctx.Done():
			d.mu.Lock()
			for _, g := range d.servers {
				g.Stop()
			}
			d.mu.Unlock()
			return errors.Join(ctx.Err())
		case <-t.C:
		}
	}
}
