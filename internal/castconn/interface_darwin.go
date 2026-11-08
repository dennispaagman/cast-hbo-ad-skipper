package castconn

import (
	"net"

	"golang.org/x/sys/unix"
)

func bindInterface(fd uintptr, network string, iface *net.Interface) error {
	if network == "tcp6" {
		return unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, iface.Index)
	}
	return unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, iface.Index)
}
