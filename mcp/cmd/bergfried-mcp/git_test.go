package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
	"google.golang.org/grpc"
)

type fakeCred struct {
	res  *pb.GitCredentialResult
	err  error
	seen []*pb.GitCredentialRequest
}

func (f *fakeCred) GitCredential(_ context.Context, in *pb.GitCredentialRequest, _ ...grpc.CallOption) (*pb.GitCredentialResult, error) {
	f.seen = append(f.seen, in)
	return f.res, f.err
}

func run(op, in string, c credClient) (string, string) {
	var out, errw bytes.Buffer
	gitCredentialHelper(op, strings.NewReader(in), &out, &errw, c)
	return out.String(), errw.String()
}

const req = "protocol=https\nhost=github.com\npath=o/r.git\n\n"

func TestHelperAnswersWithTheGrantedToken(t *testing.T) {
	f := &fakeCred{res: &pb.GitCredentialResult{Username: "x-access-token", Password: "ghs_abc"}}
	out, errw := run("get", req, f)
	if out != "username=x-access-token\npassword=ghs_abc\n" || errw != "" {
		t.Fatalf("%q %q", out, errw)
	}
	if len(f.seen) != 1 || f.seen[0].Host != "github.com" || f.seen[0].Path != "o/r.git" {
		t.Fatalf("%+v", f.seen)
	}
}

func TestHelperWithoutGrantPrintsHintToStderrOnly(t *testing.T) {
	f := &fakeCred{res: &pb.GitCredentialResult{Message: "Bergfried: no active git access – call request_git_access first."}}
	out, errw := run("get", req, f)
	if out != "" || !strings.Contains(errw, "request_git_access") {
		t.Fatalf("stdout must stay empty so git fails normally: %q %q", out, errw)
	}
	f = &fakeCred{err: errors.New("socket gone")}
	if out, errw = run("get", req, f); out != "" || !strings.Contains(errw, "not available") {
		t.Fatalf("%q %q", out, errw)
	}
}

func TestHelperNeverStoresAndIgnoresOtherHosts(t *testing.T) {
	f := &fakeCred{res: &pb.GitCredentialResult{Username: "u", Password: "p"}}
	for _, op := range []string{"store", "erase", ""} {
		if out, _ := run(op, req+"username=x\npassword=y\n", f); out != "" {
			t.Fatalf("%s must print nothing", op)
		}
	}
	for _, in := range []string{
		"protocol=https\nhost=gitlab.com\npath=o/r\n\n",
		"protocol=http\nhost=github.com\npath=o/r\n\n",
		"protocol=https\nhost=github.com.evil.example\npath=o/r\n\n",
	} {
		if out, _ := run("get", in, f); out != "" {
			t.Fatalf("no credentials for %q", in)
		}
	}
	if len(f.seen) != 0 {
		t.Fatalf("the daemon must not even be asked: %+v", f.seen)
	}
}
