package rpc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestListenUnixPerms(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d", "x.sock")
	l, err := ListenUnix(p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(p)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v", fi.Mode().Perm())
	}
	l2, err := ListenUnix(p) // old socket is replaced
	if err != nil {
		t.Fatal(err)
	}
	l2.Close()
}

func TestFolderHeaderValidated(t *testing.T) {
	mk := func(v string) context.Context {
		return metadata.NewIncomingContext(context.Background(), metadata.Pairs(FolderHeader, v))
	}
	if folderFrom(mk("shop/main")) != "shop/main" {
		t.Fatal("valid name")
	}
	for _, bad := range []string{"Evil\nname", "a b", "../x", "", "x'; rm"} {
		if folderFrom(mk(bad)) != "unknown" {
			t.Fatalf("%q", bad)
		}
	}
	if folderFrom(context.Background()) != "unknown" {
		t.Fatal("without header")
	}
}
