// Package mmdbtest writes small MaxMind DB files for tests: GeoIP and ASN
// fixtures without shipping a vendor database.
package mmdbtest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/netip"
	"sort"
)

// Record is one prefix's data (a map like MaxMind's: {"country":
// {"iso_code": "US", "names": {"en": "United States"}}} or
// {"autonomous_system_number": uint32(15169), ...}).
type Record struct {
	Prefix string
	Data   map[string]any
}

// Options describe the database.
type Options struct {
	DatabaseType string
	RecordSize   int // 24 (default), 28 or 32
	IPVersion    int // 6 (default) or 4
	BuildEpoch   uint64
}

type node struct {
	child [2]*node
	leaf  [2]int // data index + 1 when child is a leaf
}

// Build returns the database bytes. Insert shorter prefixes before longer
// ones they contain.
func Build(opt Options, recs []Record) ([]byte, error) {
	if opt.RecordSize == 0 {
		opt.RecordSize = 24
	}
	if opt.IPVersion == 0 {
		opt.IPVersion = 6
	}
	root := &node{}
	enc := &encoder{dedup: map[string]int{}}
	enc.Strings("en", "names", "iso_code") // later uses are pointers
	for _, r := range recs {
		p, err := netip.ParsePrefix(r.Prefix)
		if err != nil {
			return nil, err
		}
		p = p.Masked()
		var bits []byte
		n := p.Bits()
		if p.Addr().Is4() {
			b := p.Addr().As4()
			bits = b[:]
			if opt.IPVersion == 6 {
				b16 := netip.AddrFrom4(b).As16()
				for i := 10; i < 12; i++ {
					b16[i] = 0 // ::a.b.c.d, not ::ffff:a.b.c.d
				}
				bits, n = b16[:], n+96
			}
		} else {
			if opt.IPVersion == 4 {
				return nil, fmt.Errorf("IPv6 prefix in an IPv4 database")
			}
			b := p.Addr().As16()
			bits = b[:]
		}
		enc.value(r.Data)
		idx := len(enc.offsets)
		cur := root
		for i := 0; i < n; i++ {
			bit := (bits[i/8] >> (7 - uint(i%8))) & 1
			if i == n-1 {
				cur.child[bit], cur.leaf[bit] = nil, idx
				break
			}
			if cur.child[bit] == nil {
				nn := &node{}
				if l := cur.leaf[bit]; l != 0 { // split a shorter prefix
					nn.leaf = [2]int{l, l}
				}
				cur.child[bit], cur.leaf[bit] = nn, 0
			}
			cur = cur.child[bit]
		}
	}
	// Number nodes breadth first.
	var order []*node
	num := map[*node]int{}
	q := []*node{root}
	for len(q) > 0 {
		n := q[0]
		q = q[1:]
		num[n] = len(order)
		order = append(order, n)
		for _, c := range n.child {
			if c != nil {
				q = append(q, c)
			}
		}
	}
	nc := len(order)
	// The data section: the encoder's buffer holds every value; leaf i
	// points at its value's offset.
	offsets := enc.offsets
	var tree bytes.Buffer
	rec := func(n *node, b int) uint64 {
		switch {
		case n.child[b] != nil:
			return uint64(num[n.child[b]])
		case n.leaf[b] != 0:
			return uint64(nc) + 16 + uint64(offsets[n.leaf[b]-1])
		}
		return uint64(nc)
	}
	for _, n := range order {
		l, r := rec(n, 0), rec(n, 1)
		switch opt.RecordSize {
		case 24:
			tree.Write([]byte{byte(l >> 16), byte(l >> 8), byte(l), byte(r >> 16), byte(r >> 8), byte(r)})
		case 28:
			tree.Write([]byte{byte(l >> 16), byte(l >> 8), byte(l), byte((l>>24)&0x0f)<<4 | byte((r>>24)&0x0f), byte(r >> 16), byte(r >> 8), byte(r)})
		case 32:
			var b [8]byte
			binary.BigEndian.PutUint32(b[:4], uint32(l))
			binary.BigEndian.PutUint32(b[4:], uint32(r))
			tree.Write(b[:])
		default:
			return nil, fmt.Errorf("record size %d", opt.RecordSize)
		}
	}
	var out bytes.Buffer
	out.Write(tree.Bytes())
	out.Write(make([]byte, 16))
	out.Write(enc.buf.Bytes())
	out.WriteString("\xab\xcd\xefMaxMind.com")
	meta := &encoder{dedup: map[string]int{}, noPointers: true}
	meta.value(map[string]any{
		"binary_format_major_version": uint16(2), "binary_format_minor_version": uint16(0),
		"build_epoch": opt.BuildEpoch, "database_type": opt.DatabaseType,
		"description": map[string]any{"en": "DROOL test database"}, "ip_version": uint16(opt.IPVersion),
		"languages": []any{"en"}, "node_count": uint32(nc), "record_size": uint16(opt.RecordSize),
	})
	out.Write(meta.buf.Bytes())
	return out.Bytes(), nil
}

// encoder writes MMDB data values. Identical values after the first are
// written as pointers, as real databases do.
type encoder struct {
	buf        bytes.Buffer
	dedup      map[string]int
	offsets    []int // offset of each top-level value
	noPointers bool
}

func ctrl(typ, size int) []byte {
	var b []byte
	first := byte(0)
	if typ <= 7 {
		first = byte(typ << 5)
	}
	var ext []byte
	if typ > 7 {
		ext = []byte{byte(typ - 7)}
	}
	var sz []byte
	switch {
	case size < 29:
		first |= byte(size)
	case size < 285:
		first |= 29
		sz = []byte{byte(size - 29)}
	case size < 65821:
		first |= 30
		v := size - 285
		sz = []byte{byte(v >> 8), byte(v)}
	default:
		first |= 31
		v := size - 65821
		sz = []byte{byte(v >> 16), byte(v >> 8), byte(v)}
	}
	b = append(b, first)
	b = append(b, ext...)
	return append(b, sz...)
}

func uintBytes(v uint64) []byte {
	var b []byte
	for v > 0 {
		b = append([]byte{byte(v)}, b...)
		v >>= 8
	}
	return b
}

func pointer(p int) []byte {
	switch {
	case p < 2048:
		return []byte{1<<5 | byte(p>>8)&7, byte(p)}
	case p < 526336:
		v := p - 2048
		return []byte{1<<5 | 1<<3 | byte(v>>16)&7, byte(v >> 8), byte(v)}
	case p < 134744064:
		v := p - 526336
		return []byte{1<<5 | 2<<3 | byte(v>>24)&7, byte(v >> 16), byte(v >> 8), byte(v)}
	}
	return []byte{1<<5 | 3<<3, byte(p >> 24), byte(p >> 16), byte(p >> 8), byte(p)}
}

// value appends v at top level (identical records share one copy).
func (e *encoder) value(v any) {
	b := e.raw(v)
	if off, ok := e.dedup[string(b)]; ok && !e.noPointers {
		e.offsets = append(e.offsets, off)
		return
	}
	e.dedup[string(b)] = e.buf.Len()
	e.offsets = append(e.offsets, e.buf.Len())
	e.buf.Write(b)
}

// raw serializes v (nested strings already seen become pointers).
func (e *encoder) raw(v any) []byte {
	var b []byte
	switch x := v.(type) {
	case string:
		if off, ok := e.dedup["s:"+x]; ok && !e.noPointers {
			return pointer(off)
		}
		b = append(ctrl(2, len(x)), x...)
	case uint16:
		u := uintBytes(uint64(x))
		b = append(ctrl(5, len(u)), u...)
	case uint32:
		u := uintBytes(uint64(x))
		b = append(ctrl(6, len(u)), u...)
	case uint64:
		u := uintBytes(x)
		b = append(ctrl(9, len(u)), u...)
	case int:
		u := uintBytes(uint64(uint32(int32(x))))
		b = append(ctrl(8, len(u)), u...)
	case bool:
		n := 0
		if x {
			n = 1
		}
		b = ctrl(14, n)
	case []any:
		b = ctrl(11, len(x))
		for _, it := range x {
			b = append(b, e.raw(it)...)
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b = ctrl(7, len(x))
		for _, k := range keys {
			b = append(b, e.raw(k)...)
			b = append(b, e.raw(x[k])...)
		}
	default:
		panic(fmt.Sprintf("mmdbtest: unsupported %T", v))
	}
	return b
}

// Strings registers s as a standalone value so later uses become pointers
// (exercises the reader's pointer decoding).
func (e *encoder) Strings(s ...string) {
	for _, x := range s {
		if _, ok := e.dedup["s:"+x]; !ok {
			e.dedup["s:"+x] = e.buf.Len()
			e.buf.Write(append(ctrl(2, len(x)), x...))
		}
	}
}

// Country is a MaxMind-style country record.
func Country(code, name string) map[string]any {
	return map[string]any{"country": map[string]any{"iso_code": code, "names": map[string]any{"en": name}}}
}

// ASN is a MaxMind-style ASN record.
func ASN(n uint32, org string) map[string]any {
	return map[string]any{"autonomous_system_number": n, "autonomous_system_organization": org}
}
