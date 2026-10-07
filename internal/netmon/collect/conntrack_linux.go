//go:build linux

package collect

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// A minimal ctnetlink client: it subscribes to "new connection" events and
// asks for one entry by its tuple. It only reads the connection tracking
// table; it never changes it, loads modules or touches sysctls. Opening it
// needs CAP_NET_ADMIN (the agent runs as root).

const (
	ctMsgNew        = 0 // IPCTNL_MSG_CT_NEW
	ctMsgGet        = 1 // IPCTNL_MSG_CT_GET
	ctaTupleOrig    = 1
	ctaTupleIP      = 1
	ctaTupleProto   = 2
	ctaIPv4Src      = 1
	ctaIPv4Dst      = 2
	ctaIPv6Src      = 3
	ctaIPv6Dst      = 4
	ctaProtoNum     = 1
	ctaProtoSrcPort = 2
	ctaProtoDstPort = 3
	nlaTypeMask     = 0x3fff
)

// ctTuple is a connection's original direction: who opened it.
type ctTuple struct {
	Proto    uint8 // 6 tcp, 17 udp
	Src, Dst netip.Addr
	SPort    uint16
	DPort    uint16
}

type ctConn struct {
	fd     int
	lost   atomic.Int64
	closed atomic.Bool
}

// conntrackUsable explains why conntrack can't be used, or returns "".
// It reads only /proc.
func conntrackUsable() string {
	if _, err := os.Stat("/proc/sys/net/netfilter/nf_conntrack_count"); err != nil {
		return "connection tracking is not active on this host (no nf_conntrack); DROOL does not load kernel modules"
	}
	if b, err := os.ReadFile("/proc/sys/net/netfilter/nf_conntrack_events"); err == nil && strings.TrimSpace(string(b)) == "0" {
		return "conntrack events are disabled (net.netfilter.nf_conntrack_events = 0); DROOL does not change sysctls"
	}
	return ""
}

func nlSocket(groups uint32) (int, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_NETFILTER)
	if err != nil {
		return -1, err
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// openEvents subscribes to new-connection events.
func openEvents() (*ctConn, error) {
	fd, err := nlSocket(1 << (unix.NFNLGRP_CONNTRACK_NEW - 1))
	if err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			return nil, fmt.Errorf("no permission to read conntrack events (needs CAP_NET_ADMIN)")
		}
		return nil, fmt.Errorf("conntrack events: %v", err)
	}
	// A large buffer rides out bursts; overflows are counted, not hidden.
	if unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, 8<<20) != nil {
		unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 8<<20)
	}
	// Wake up every second so Close is noticed.
	tv := unix.Timeval{Sec: 1}
	unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	return &ctConn{fd: fd}, nil
}

// Close stops Read, which closes the socket (a blocked recvfrom isn't
// woken by closing the descriptor from another goroutine).
func (c *ctConn) Close() error { c.closed.Store(true); return nil }

// Lost returns the number of times the kernel dropped events.
func (c *ctConn) Lost() int64 { return c.lost.Load() }

// Read delivers events until the socket is closed or fails. Lost events
// (the kernel's buffer overflowed) are counted.
func (c *ctConn) Read(fn func(ctTuple)) error {
	defer unix.Close(c.fd)
	buf := make([]byte, 1<<16)
	for {
		if c.closed.Load() {
			return nil
		}
		n, _, err := unix.Recvfrom(c.fd, buf, 0)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
				continue
			}
			if errors.Is(err, unix.ENOBUFS) {
				c.lost.Add(1)
				continue
			}
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		parseMessages(buf[:n], func(typ uint16, flags uint16, payload []byte) {
			if typ != unix.NFNL_SUBSYS_CTNETLINK<<8|ctMsgNew || flags&(unix.NLM_F_CREATE) == 0 {
				return
			}
			if t, ok := parseCT(payload); ok {
				fn(t)
			}
		})
	}
}

// parseMessages walks netlink messages.
func parseMessages(b []byte, fn func(typ, flags uint16, payload []byte)) {
	for len(b) >= unix.NLMSG_HDRLEN {
		l := int(binary.NativeEndian.Uint32(b[0:4]))
		if l < unix.NLMSG_HDRLEN || l > len(b) {
			return
		}
		typ := binary.NativeEndian.Uint16(b[4:6])
		flags := binary.NativeEndian.Uint16(b[6:8])
		fn(typ, flags, b[unix.NLMSG_HDRLEN:l])
		al := (l + unix.NLMSG_ALIGNTO - 1) &^ (unix.NLMSG_ALIGNTO - 1)
		if al > len(b) {
			return
		}
		b = b[al:]
	}
}

// attrs walks netlink attributes.
func attrs(b []byte, fn func(typ uint16, v []byte)) {
	for len(b) >= 4 {
		l := int(binary.NativeEndian.Uint16(b[0:2]))
		if l < 4 || l > len(b) {
			return
		}
		fn(binary.NativeEndian.Uint16(b[2:4])&nlaTypeMask, b[4:l])
		al := (l + 3) &^ 3
		if al > len(b) {
			return
		}
		b = b[al:]
	}
}

// parseCT reads a ctnetlink message (nfgenmsg + attributes): the original
// tuple only.
func parseCT(p []byte) (ctTuple, bool) {
	if len(p) < 4 {
		return ctTuple{}, false
	}
	var t ctTuple
	var ok bool
	attrs(p[4:], func(typ uint16, v []byte) {
		if typ != ctaTupleOrig {
			return
		}
		attrs(v, func(typ uint16, v []byte) {
			switch typ {
			case ctaTupleIP:
				attrs(v, func(typ uint16, v []byte) {
					switch typ {
					case ctaIPv4Src, ctaIPv6Src:
						t.Src, _ = netip.AddrFromSlice(v)
					case ctaIPv4Dst, ctaIPv6Dst:
						t.Dst, _ = netip.AddrFromSlice(v)
					}
				})
			case ctaTupleProto:
				attrs(v, func(typ uint16, v []byte) {
					switch {
					case typ == ctaProtoNum && len(v) >= 1:
						t.Proto = v[0]
					case typ == ctaProtoSrcPort && len(v) >= 2:
						t.SPort = binary.BigEndian.Uint16(v)
					case typ == ctaProtoDstPort && len(v) >= 2:
						t.DPort = binary.BigEndian.Uint16(v)
					}
				})
			}
		})
		ok = true
	})
	t.Src, t.Dst = t.Src.Unmap(), t.Dst.Unmap()
	return t, ok && t.Src.IsValid() && t.Dst.IsValid()
}

func nlattr(typ uint16, v []byte) []byte {
	l := 4 + len(v)
	b := make([]byte, 4, (l+3)&^3)
	binary.NativeEndian.PutUint16(b[0:2], uint16(l))
	binary.NativeEndian.PutUint16(b[2:4], typ)
	b = append(b, v...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func nested(typ uint16, parts ...[]byte) []byte {
	var v []byte
	for _, p := range parts {
		v = append(v, p...)
	}
	return nlattr(typ|unix.NLA_F_NESTED, v)
}

// buildGet encodes IPCTNL_MSG_CT_GET for one original tuple.
func buildGet(t ctTuple, seq uint32) []byte {
	fam := byte(unix.AF_INET)
	srcT, dstT := uint16(ctaIPv4Src), uint16(ctaIPv4Dst)
	src, dst := t.Src.AsSlice(), t.Dst.AsSlice()
	if t.Src.Is6() {
		fam, srcT, dstT = unix.AF_INET6, ctaIPv6Src, ctaIPv6Dst
	}
	sp, dp := make([]byte, 2), make([]byte, 2)
	binary.BigEndian.PutUint16(sp, t.SPort)
	binary.BigEndian.PutUint16(dp, t.DPort)
	body := append([]byte{fam, unix.NFNETLINK_V0, 0, 0},
		nested(ctaTupleOrig,
			nested(ctaTupleIP, nlattr(srcT, src), nlattr(dstT, dst)),
			nested(ctaTupleProto, nlattr(ctaProtoNum, []byte{t.Proto}), nlattr(ctaProtoSrcPort, sp), nlattr(ctaProtoDstPort, dp)))...)
	msg := make([]byte, unix.NLMSG_HDRLEN, unix.NLMSG_HDRLEN+len(body))
	binary.NativeEndian.PutUint32(msg[0:4], uint32(unix.NLMSG_HDRLEN+len(body)))
	binary.NativeEndian.PutUint16(msg[4:6], unix.NFNL_SUBSYS_CTNETLINK<<8|ctMsgGet)
	binary.NativeEndian.PutUint16(msg[6:8], unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	binary.NativeEndian.PutUint32(msg[8:12], seq)
	return append(msg, body...)
}

// tracked asks the kernel whether it tracks one connection: it proves that
// connection tracking is active in this network namespace (the hooks are
// only registered while something, like a firewall, uses them).
func tracked(t ctTuple) (bool, error) {
	fd, err := nlSocket(0)
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	tv := unix.Timeval{Sec: 2}
	unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	if err := unix.Sendto(fd, buildGet(t, 1), 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return false, err
	}
	buf := make([]byte, 1<<14)
	found := false
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return found, err
		}
		done := false
		var rerr error
		parseMessages(buf[:n], func(typ, _ uint16, p []byte) {
			switch typ {
			case unix.NLMSG_ERROR:
				if len(p) >= 4 {
					if code := int32(binary.NativeEndian.Uint32(p[0:4])); code != 0 && !found {
						if -code != int32(unix.ENOENT) {
							rerr = unix.Errno(-code)
						}
					}
				}
				done = true
			case unix.NLMSG_DONE:
				done = true
			case unix.NFNL_SUBSYS_CTNETLINK<<8 | ctMsgNew:
				if _, ok := parseCT(p); ok {
					found = true
				}
			}
		})
		if done || rerr != nil {
			return found, rerr
		}
	}
}

// kernelConntrack is the real kernel interface.
type kernelConntrack struct{}

func (kernelConntrack) Usable() string { return conntrackUsable() }

func (kernelConntrack) Open() (Events, error) {
	c, err := openEvents()
	if err != nil {
		return nil, err
	}
	return kernelEvents{c}, nil
}

func (kernelConntrack) Tracked(proto uint8, src, dst netip.AddrPort) (bool, error) {
	return tracked(ctTuple{Proto: proto, Src: src.Addr(), Dst: dst.Addr(), SPort: src.Port(), DPort: dst.Port()})
}

type kernelEvents struct{ c *ctConn }

func (e kernelEvents) Read(fn func(proto uint8, src, dst netip.AddrPort)) error {
	return e.c.Read(func(t ctTuple) {
		fn(t.Proto, netip.AddrPortFrom(t.Src, t.SPort), netip.AddrPortFrom(t.Dst, t.DPort))
	})
}
func (e kernelEvents) Close() error { return e.c.Close() }
func (e kernelEvents) Lost() int64  { return e.c.Lost() }
