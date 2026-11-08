//go:build !linux && !darwin

package castconn

import (
	"fmt"
	"net"
)

func bindInterface(_ uintptr, _ string, iface *net.Interface) error {
	return fmt.Errorf("binding Cast connections to interface %s is unsupported on this platform", iface.Name)
}
