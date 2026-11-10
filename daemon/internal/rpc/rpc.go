// Package rpc: gRPC server. One unix socket per running sandbox (identity = socket),
// plus an admin socket for CLI/app. All sockets 0600 in 0700 directories.
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
	"github.com/DevertNet/bergfried/daemon/internal/broker"
	"github.com/DevertNet/bergfried/daemon/internal/ghtoken"
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

// ListenUnix: directory 0700, socket 0600, an old socket is replaced.
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

type sandboxServer struct {
	pb.UnimplementedSandboxAPIServer
	sandbox string
	svc     *access.Service
	ssh     *broker.Service
	gh      *ghtoken.Service
	st      *store.Store
}

func (s *sandboxServer) SSHAttempt(ctx context.Context, r *pb.SSHAttemptRequest) (*pb.SSHAttemptResult, error) {
	state, msg := s.ssh.Attempt(ctx, s.sandbox, r.Host, r.Port, r.User, r.Wait)
	return &pb.SSHAttemptResult{Message: msg, State: state}, nil
}

func (s *sandboxServer) RequestGitHubToken(ctx context.Context, r *pb.GitHubTokenRequest) (*pb.GitHubTokenResult, error) {
	ok, msg, err := s.gh.Request(ctx, s.sandbox, s.agent(ctx), r.Repo, r.Reason)
	if err != nil {
		return nil, err
	}
	v := pb.Verdict_VERDICT_DENIED
	if ok {
		v = pb.Verdict_VERDICT_ALLOWED
	} else if strings.Contains(msg, "expired") {
		v = pb.Verdict_VERDICT_EXPIRED
	}
	return &pb.GitHubTokenResult{Verdict: v, Message: msg}, nil
}

func (s *sandboxServer) RequestSSH(ctx context.Context, r *pb.SSHAccessRequest) (*pb.SSHAccessResult, error) {
	return s.ssh.RequestSSH(ctx, s.sandbox, s.agent(ctx), r.Host, r.User, int(r.Minutes), r.Reason)
}

// agent: name without the "<sandbox>/" prefix that the VM sends along.
func (s *sandboxServer) agent(ctx context.Context) string {
	return strings.TrimPrefix(agentFrom(ctx), s.sandbox+"/")
}

func (s *sandboxServer) audit(ctx context.Context, event, detail string) {
	if err := s.st.Audit("agent", event, s.sandbox, s.agent(ctx), detail); err != nil {
		fmt.Fprintln(os.Stderr, "WARNING: audit log not written:", err)
	}
}

func toPB(es []index.Entry) []*pb.FileEntry {
	out := make([]*pb.FileEntry, 0, len(es))
	for _, e := range es {
		out = append(out, &pb.FileEntry{Path: e.Path, IsDir: e.IsDir, Size: e.Size, MtimeUnix: e.MTime})
	}
	return out
}

func (s *sandboxServer) ListFiles(ctx context.Context, r *pb.ListFilesRequest) (*pb.ListFilesResponse, error) {
	s.audit(ctx, "files.list", r.Path)
	es, trunc, err := s.svc.Ix.List(r.Path, r.Pattern)
	if errors.Is(err, index.ErrNotReady) {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	if err != nil {
		// do not reveal the existence of blocked paths
		return nil, status.Error(codes.NotFound, "path not found")
	}
	return &pb.ListFilesResponse{Entries: toPB(es), Truncated: trunc}, nil
}

func (s *sandboxServer) SearchFiles(ctx context.Context, r *pb.SearchFilesRequest) (*pb.SearchFilesResponse, error) {
	s.audit(ctx, "files.search", r.Query)
	if !s.svc.Ix.Ready() {
		return nil, status.Error(codes.Unavailable, index.ErrNotReady.Error())
	}
	es, trunc := s.svc.Ix.Search(r.Query)
	return &pb.SearchFilesResponse{Entries: toPB(es), Truncated: trunc}, nil
}

func (s *sandboxServer) RequestFile(ctx context.Context, r *pb.AccessRequest) (*pb.AccessResult, error) {
	return s.svc.Request(ctx, s.sandbox, s.agent(ctx), false, r.Path, r.Reason)
}

func (s *sandboxServer) RequestFolder(ctx context.Context, r *pb.AccessRequest) (*pb.AccessResult, error) {
	return s.svc.Request(ctx, s.sandbox, s.agent(ctx), true, r.Path, r.Reason)
}

type adminServer struct {
	pb.UnimplementedAdminServer
	svc *access.Service
	ssh *broker.Service
	st  *store.Store
}

func fail(err error) error { return status.Error(codes.FailedPrecondition, err.Error()) }

func (a *adminServer) DecideSSH(_ context.Context, r *pb.DecideSSHRequest) (*pb.DecideResponse, error) {
	msg, err := a.ssh.DecideSSH(broker.Decision{ID: r.Id, Allow: r.Allow, Minutes: int(r.Minutes), KeyFP: r.KeyFingerprint,
		User: r.User, AllAgents: r.AllAgents, PerConn: r.PerConnection, TrustUnknownHost: r.TrustUnknownHost})
	if err != nil {
		return nil, fail(err)
	}
	return &pb.DecideResponse{Message: msg}, nil
}

func (a *adminServer) ListGrants(_ context.Context, r *pb.ListGrantsRequest) (*pb.ListGrantsResponse, error) {
	gs, err := a.st.ListGrants(r.OnlyActive, time.Now())
	if err != nil {
		return nil, err
	}
	out := &pb.ListGrantsResponse{}
	for _, g := range gs {
		out.Grants = append(out.Grants, &pb.Grant{Id: g.ID, Sandbox: g.Sandbox, Agent: g.Agent, Host: g.Host, KeyFingerprint: g.KeyFP,
			User: g.User, ExpiresUnix: g.Expires.Unix(), PerConnection: g.PerConn, Revoked: g.Revoked, HostKeyFingerprints: g.HostKeys})
	}
	return out, nil
}

func (a *adminServer) RevokeGrant(ctx context.Context, r *pb.GrantRef) (*pb.DecideResponse, error) {
	if err := a.ssh.Revoke(ctx, r.Id); err != nil {
		return nil, fail(err)
	}
	return &pb.DecideResponse{Message: "entzogen"}, nil
}

func (a *adminServer) ExtendGrant(ctx context.Context, r *pb.ExtendGrantRequest) (*pb.DecideResponse, error) {
	exp, err := a.ssh.Extend(ctx, r.Id, int(r.Minutes))
	if err != nil {
		return nil, fail(err)
	}
	return &pb.DecideResponse{Message: "valid until " + exp.Format("2006-01-02 15:04")}, nil
}

func (a *adminServer) ListKeys(context.Context, *pb.ListKeysRequest) (*pb.ListKeysResponse, error) {
	ks, err := a.ssh.Keys()
	if err != nil {
		return &pb.ListKeysResponse{Error: err.Error()}, nil
	}
	return &pb.ListKeysResponse{Keys: ks}, nil
}

func (a *adminServer) ListRequests(_ context.Context, r *pb.ListRequestsRequest) (*pb.ListRequestsResponse, error) {
	rs, err := a.st.ListRequests(r.OnlyPending, 200)
	if err != nil {
		return nil, err
	}
	out := &pb.ListRequestsResponse{}
	for _, x := range rs {
		out.Requests = append(out.Requests, &pb.Request{Id: x.ID, Sandbox: x.Sandbox, Agent: x.Agent, Kind: x.Kind, Path: x.Path,
			Reason: x.Reason, Status: x.Status, CreatedUnix: x.Created.Unix(), TotalBytes: x.Bytes, TotalFiles: x.Files})
	}
	return out, nil
}

func (a *adminServer) Decide(_ context.Context, r *pb.DecideRequest) (*pb.DecideResponse, error) {
	msg, err := a.svc.Decide(r.Id, r.Allow, r.ConfirmLarge, "")
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &pb.DecideResponse{Message: msg}, nil
}

// Daemon manages the admin socket and the sandbox sockets.
type Daemon struct {
	St        *store.Store
	Svc       *access.Service
	SSH       *broker.Service
	GH        *ghtoken.Service
	AdminSock string
	L         lima.Client

	mu      sync.Mutex
	servers map[string]*runtime // Sandbox -> Server + Agent-Socket
}

type runtime struct {
	g  *grpc.Server
	al net.Listener
}

func (r *runtime) stop() {
	r.g.Stop()
	r.al.Close()
}

func AgentSocket(vm string) (string, error) {
	d, err := lima.InstanceDir(vm)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "sock", "ssh-agent.sock"), nil
}

func SandboxSocket(vm string) (string, error) {
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
	pb.RegisterAdminServer(g, &adminServer{svc: d.Svc, ssh: d.SSH, st: d.St})
	go g.Serve(l)
	return nil
}

func (d *Daemon) startSandbox(name, vm string) error {
	sock, err := SandboxSocket(vm)
	if err != nil {
		return err
	}
	l, err := ListenUnix(sock)
	if err != nil {
		return err
	}
	g := grpc.NewServer()
	pb.RegisterSandboxAPIServer(g, &sandboxServer{sandbox: name, svc: d.Svc, ssh: d.SSH, gh: d.GH, st: d.St})
	go g.Serve(l)
	asock, err := AgentSocket(vm)
	if err != nil {
		g.Stop()
		return err
	}
	al, err := ListenUnix(asock)
	if err != nil {
		g.Stop()
		return err
	}
	go func() {
		for {
			c, err := al.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = d.SSH.B.NewConn(name).Serve(c)
			}()
		}
	}()
	d.servers[name] = &runtime{g: g, al: al}
	return nil
}

// Reconcile: one socket per running sandbox, tear down for stopped/deleted ones.
func (d *Daemon) Reconcile(ctx context.Context) error {
	ss, err := d.St.ListSandboxes()
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
				if err := d.startSandbox(s.Name, s.VM); err != nil {
					fmt.Fprintln(os.Stderr, "socket for", s.Name, "failed:", err)
					continue
				}
				_ = d.St.Audit("daemon", "socket.open", s.Name, "", "")
			}
		}
	}
	for name, g := range d.servers {
		if !want[name] {
			g.stop()
			delete(d.servers, name)
			_ = d.St.Audit("daemon", "socket.close", name, "", "")
		}
	}
	return nil
}

func (d *Daemon) Run(ctx context.Context) error {
	d.servers = map[string]*runtime{}
	if err := d.St.ExpirePending(); err != nil {
		return err
	}
	if err := d.startAdmin(); err != nil {
		return err
	}
	t := time.NewTicker(5 * time.Second) // also the tick for the expiry check of SSH grants
	defer t.Stop()
	for {
		if err := d.Reconcile(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "Reconcile:", err)
		}
		d.SSH.Sweep(ctx)
		select {
		case <-ctx.Done():
			d.mu.Lock()
			for _, g := range d.servers {
				g.stop()
			}
			d.mu.Unlock()
			return errors.Join(ctx.Err())
		case <-t.C:
		}
	}
}
