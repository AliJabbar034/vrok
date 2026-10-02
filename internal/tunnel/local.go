package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
)

func init() { Register("local", newLocal) }

// LocalTunnel does not tunnel anything. It reports the address the share is
// already reachable on, which is what `--local` wants: a LAN URL that a phone
// on the same Wi-Fi can open, with nothing leaving the network.
type LocalTunnel struct{}

func newLocal(Config) (Tunnel, error) { return LocalTunnel{}, nil }

// Name implements Tunnel.
func (LocalTunnel) Name() string { return "local" }

// Start implements Tunnel. It rewrites a wildcard or loopback host into the
// machine's LAN address so the URL is useful on another device.
func (LocalTunnel) Start(_ context.Context, target string) (PublicURL, error) {
	u, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("tunnel: parse target %q: %w", target, err)
	}

	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return PublicURL(target), nil
	}
	if !isWildcard(host) {
		return PublicURL(target), nil
	}
	lan, err := LANAddress()
	if err != nil {
		// A wildcard host is useless in a URL you intend to hand to someone,
		// so fall back to loopback: still correct for this machine, and
		// obviously local rather than mysteriously broken.
		lan = "127.0.0.1"
	}
	u.Host = net.JoinHostPort(lan, port)
	return PublicURL(u.String()), nil
}

// Stop implements Tunnel.
func (LocalTunnel) Stop(context.Context) error { return nil }

func isWildcard(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::" || host == "[::]"
}

// LANAddress returns the machine's primary non-loopback IPv4 address.
func LANAddress() (string, error) {
	// The routing table knows which interface would actually be used, which
	// is the right answer on a machine with a VPN, Docker bridges and several
	// physical interfaces.
	if ip, err := routedAddress(); err == nil {
		return ip, nil
	}
	// Asking the routing table needs a socket, which a sandbox or a strict
	// firewall may refuse. Enumerating interfaces needs none.
	return interfaceAddress()
}

// routedAddress asks the kernel which local address would be used to reach the
// internet. No packet is sent: a UDP socket is only "connected", which is
// enough to select a route, so this works offline and leaks nothing.
func routedAddress() (string, error) {
	conn, err := net.Dial("udp4", "192.0.2.1:9") // reserved documentation range
	if err != nil {
		return "", fmt.Errorf("tunnel: detect routed address: %w", err)
	}
	defer conn.Close()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("tunnel: unexpected local address type %T", conn.LocalAddr())
	}
	return addr.IP.String(), nil
}

// interfaceAddress picks the first private IPv4 address on an interface that
// is up, preferring private ranges because a LAN URL is for the local network.
func interfaceAddress() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("tunnel: list interfaces: %w", err)
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			return ip.String(), nil
		}
	}
	return "", errors.New("tunnel: no private IPv4 address found on any interface")
}
