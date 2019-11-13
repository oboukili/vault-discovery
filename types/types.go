package types

import (
	"google.golang.org/api/compute/v1"
	"net"
	"os/exec"
)

type GCEZoneInstances map[string][]*compute.Instance
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
