package types

import (
	"net"
	"os/exec"
)

type VaultTunnelConnAttrs struct {
	LocalAddr   string
	LocalPort   string
	VaultScheme string
}
type VaultTunnelConn struct {
	Conn  net.Conn
	Cmd   *exec.Cmd
	Attrs VaultTunnelConnAttrs
}
