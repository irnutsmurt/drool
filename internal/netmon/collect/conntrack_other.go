//go:build !linux

package collect

import (
	"errors"
	"net/netip"
)

type kernelConntrack struct{}

func (kernelConntrack) Usable() string { return "conntrack is Linux only" }
func (kernelConntrack) Open() (Events, error) {
	return nil, errors.New("conntrack is Linux only")
}
func (kernelConntrack) Tracked(uint8, netip.AddrPort, netip.AddrPort) (bool, error) {
	return false, errors.New("conntrack is Linux only")
}
