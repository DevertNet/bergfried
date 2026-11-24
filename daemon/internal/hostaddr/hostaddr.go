// Package hostaddr determines the LAN address of the host (the Mac) so an assistant in a
// sandbox can hand the user a link that works from the phone. It only reads interface data.
package hostaddr

import (
	"context"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Iface is the part of net.Interface plus its addresses that the selection needs.
type Iface struct {
	Name  string
	Flags net.Flags
	Addrs []net.Addr
}

// Address is one usable LAN address.
type Address struct {
	Interface string
	IP        string
}

// Result: Addresses[0] is the preferred one (empty when the host has no LAN address).
type Result struct {
	Addresses []Address
	Hostname  string // "<name>.local" (Bonjour) or empty
}

// Interfaces reads the real interfaces.
func Interfaces() ([]Iface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]Iface, 0, len(ifs))
	for _, i := range ifs {
		as, err := i.Addrs()
		if err != nil {
			continue
		}
		out = append(out, Iface{Name: i.Name, Flags: i.Flags, Addrs: as})
	}
	return out, nil
}

// physical: on macOS Ethernet and Wi-Fi are enN. Everything else (utun/VPN, bridge/vmenet/Lima/Docker,
// awdl/llw, gif, ...) is not reachable from the phone or not the address the user means.
func physical(name string) (int, bool) {
	if !strings.HasPrefix(name, "en") {
		return 0, false
	}
	n, err := strconv.Atoi(name[2:])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// Select picks the private-network IPv4 addresses of up, physical interfaces, en0 first.
func Select(ifs []Iface) []Address {
	type cand struct {
		n int
		a Address
	}
	var cs []cand
	for _, i := range ifs {
		n, ok := physical(i.Name)
		if !ok || i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		for _, a := range i.Addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
				continue
			}
			cs = append(cs, cand{n, Address{Interface: i.Name, IP: ip.String()}})
		}
	}
	sort.SliceStable(cs, func(x, y int) bool { return cs[x].n < cs[y].n })
	out := make([]Address, len(cs))
	for i, c := range cs {
		out[i] = c.a
	}
	return out
}

var validName = func(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// Bonjour returns "<LocalHostName>.local" from macOS (empty elsewhere or on error).
func Bonjour() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--get", "LocalHostName").Output()
	if err != nil {
		return ""
	}
	return bonjourName(strings.TrimSpace(string(out)))
}

func bonjourName(n string) string {
	if !validName(n) {
		return ""
	}
	return n + ".local"
}

// Lookup collects the result with the given sources (tests inject fakes).
func Lookup(ifaces func() ([]Iface, error), hostname func() string) (Result, error) {
	ifs, err := ifaces()
	if err != nil {
		return Result{}, err
	}
	return Result{Addresses: Select(ifs), Hostname: hostname()}, nil
}

// Default uses the real interfaces and macOS' Bonjour name.
func Default() (Result, error) { return Lookup(Interfaces, Bonjour) }
