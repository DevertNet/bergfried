package hostaddr

import (
	"errors"
	"net"
	"testing"
)

func ipn(s string) net.Addr {
	ip, n, _ := net.ParseCIDR(s)
	n.IP = ip
	return n
}

func TestSelect(t *testing.T) {
	up := net.FlagUp | net.FlagBroadcast
	ifs := []Iface{
		{"lo0", net.FlagUp | net.FlagLoopback, []net.Addr{ipn("127.0.0.1/8")}},
		{"utun3", up | net.FlagPointToPoint, []net.Addr{ipn("10.8.0.2/24")}},
		{"bridge100", up, []net.Addr{ipn("192.168.64.1/24")}},
		{"docker0", up, []net.Addr{ipn("172.17.0.1/16")}},
		{"en1", up, []net.Addr{ipn("10.0.0.7/24"), ipn("fe80::1/64"), ipn("2001:db8::1/64")}},
		{"en0", up, []net.Addr{ipn("169.254.3.4/16"), ipn("192.168.1.20/24")}},
		{"en2", net.FlagBroadcast, []net.Addr{ipn("192.168.9.9/24")}}, // down
		{"enx", up, []net.Addr{ipn("192.168.7.7/24")}},                // not enN
	}
	got := Select(ifs)
	want := []Address{{"en0", "192.168.1.20"}, {"en1", "10.0.0.7"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSelectNone(t *testing.T) {
	if got := Select([]Iface{{"lo0", net.FlagUp | net.FlagLoopback, []net.Addr{ipn("127.0.0.1/8")}}}); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestBonjourNameRejectsOddNames(t *testing.T) {
	if bonjourName("Leons-MacBook") != "Leons-MacBook.local" {
		t.Fatal("valid name")
	}
	for _, n := range []string{"", "a b", "x\n.evil", "a.b", "é"} {
		if bonjourName(n) != "" {
			t.Fatalf("%q accepted", n)
		}
	}
}

func TestLookup(t *testing.T) {
	r, err := Lookup(func() ([]Iface, error) {
		return []Iface{{"en0", net.FlagUp, []net.Addr{ipn("192.168.1.2/24")}}}, nil
	}, func() string { return "mac.local" })
	if err != nil || r.Hostname != "mac.local" || len(r.Addresses) != 1 {
		t.Fatalf("%v %v", r, err)
	}
	if _, err := Lookup(func() ([]Iface, error) { return nil, errors.New("x") }, func() string { return "" }); err == nil {
		t.Fatal("want error")
	}
}
