//go:build linux

package collect

import (
	"encoding/binary"
	"net/netip"
	"testing"

	"golang.org/x/sys/unix"
)

// A ctnetlink "new" event has the same layout as a GET request (header,
// nfgenmsg, CTA_TUPLE_ORIG); the parser must read both families and skip
// what it doesn't know.
func TestParseCT(t *testing.T) {
	for _, tc := range []ctTuple{
		{Proto: 6, Src: netip.MustParseAddr("10.0.0.5"), Dst: netip.MustParseAddr("142.250.1.1"), SPort: 44310, DPort: 443},
		{Proto: 17, Src: netip.MustParseAddr("2001:db8::5"), Dst: netip.MustParseAddr("2a00:1450::1"), SPort: 5353, DPort: 53},
	} {
		msg := buildGet(tc, 7)
		// Turn the request into an event and add an attribute the parser
		// doesn't read (CTA_STATUS), plus a second message in the buffer.
		binary.NativeEndian.PutUint16(msg[4:6], unix.NFNL_SUBSYS_CTNETLINK<<8|ctMsgNew)
		binary.NativeEndian.PutUint16(msg[6:8], unix.NLM_F_CREATE|unix.NLM_F_EXCL)
		msg = append(msg, nlattr(3, []byte{0, 0, 0, 8})...)
		binary.NativeEndian.PutUint32(msg[0:4], uint32(len(msg)))
		buf := append(append([]byte{}, msg...), msg...)
		var got []ctTuple
		parseMessages(buf, func(typ, flags uint16, p []byte) {
			if typ == unix.NFNL_SUBSYS_CTNETLINK<<8|ctMsgNew && flags&unix.NLM_F_CREATE != 0 {
				if ct, ok := parseCT(p); ok {
					got = append(got, ct)
				}
			}
		})
		if len(got) != 2 || got[0] != tc {
			t.Fatalf("parsed %+v, want %+v", got, tc)
		}
	}
	// Truncated and garbage input: nothing, no panic.
	msg := buildGet(ctTuple{Proto: 6, Src: netip.MustParseAddr("10.0.0.1"), Dst: netip.MustParseAddr("10.0.0.2"), SPort: 1, DPort: 2}, 1)
	for i := 0; i < len(msg); i++ {
		parseMessages(msg[:i], func(_, _ uint16, p []byte) { parseCT(p) })
		b := append([]byte(nil), msg...)
		b[i] ^= 0xff
		parseMessages(b, func(_, _ uint16, p []byte) { parseCT(p) })
	}
}
