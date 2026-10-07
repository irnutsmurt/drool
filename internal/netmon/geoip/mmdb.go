package geoip

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/netip"
)

// A minimal reader for the MaxMind DB format (MMDB, format version 2), the
// format of MaxMind GeoLite2/GeoIP2, DB-IP and IPinfo databases. It reads
// the whole file into memory and checks every offset: a corrupt or
// truncated database is an error, never a panic.

var metaMarker = []byte("\xab\xcd\xefMaxMind.com")

// ErrCorrupt reports a malformed database.
var ErrCorrupt = errors.New("invalid MaxMind DB data")

// Meta is a database's metadata.
type Meta struct {
	DatabaseType string
	Description  string
	IPVersion    int
	RecordSize   int
	NodeCount    uint32
	BuildEpoch   uint64
	Languages    []string
}

// DB is an opened MMDB database.
type DB struct {
	buf      []byte
	data     []byte // the data section
	meta     Meta
	treeSize int
	ipv4Root uint32 // node reached after 96 zero bits (IPv4 in an IPv6 tree)
}

func corrupt(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(f, a...))
}

// Open parses buf (the whole database file).
func Open(buf []byte) (*DB, error) {
	i := bytes.LastIndex(buf, metaMarker)
	if i < 0 {
		return nil, corrupt("no metadata marker (not a MaxMind DB file)")
	}
	d := decoder{buf: buf[i+len(metaMarker):]}
	mv, _, err := d.decode(0, 0)
	if err != nil {
		return nil, corrupt("metadata: %v", err)
	}
	m, ok := mv.(map[string]any)
	if !ok {
		return nil, corrupt("metadata is not a map")
	}
	db := &DB{buf: buf}
	db.meta.DatabaseType, _ = m["database_type"].(string)
	if desc, ok := m["description"].(map[string]any); ok {
		db.meta.Description, _ = desc["en"].(string)
	}
	db.meta.IPVersion = int(asUint(m["ip_version"]))
	db.meta.RecordSize = int(asUint(m["record_size"]))
	nc := asUint(m["node_count"])
	db.meta.BuildEpoch = asUint(m["build_epoch"])
	if l, ok := m["languages"].([]any); ok {
		for _, v := range l {
			if s, ok := v.(string); ok {
				db.meta.Languages = append(db.meta.Languages, s)
			}
		}
	}
	if major := asUint(m["binary_format_major_version"]); major != 2 {
		return nil, corrupt("unsupported format version %d", major)
	}
	switch db.meta.RecordSize {
	case 24, 28, 32:
	default:
		return nil, corrupt("unsupported record size %d", db.meta.RecordSize)
	}
	if db.meta.IPVersion != 4 && db.meta.IPVersion != 6 {
		return nil, corrupt("unsupported IP version %d", db.meta.IPVersion)
	}
	if nc == 0 || nc > math.MaxUint32/2 {
		return nil, corrupt("bad node count %d", nc)
	}
	db.meta.NodeCount = uint32(nc)
	db.treeSize = int(nc) * db.meta.RecordSize / 4
	if db.treeSize+16 > i {
		return nil, corrupt("search tree larger than the file")
	}
	db.data = buf[db.treeSize+16 : i]
	if db.meta.IPVersion == 6 {
		n := uint32(0)
		for k := 0; k < 96 && n < db.meta.NodeCount; k++ {
			if n, err = db.record(n, 0); err != nil {
				return nil, err
			}
		}
		db.ipv4Root = n
	}
	return db, nil
}

// Meta returns the database's metadata.
func (db *DB) Meta() Meta { return db.meta }

func asUint(v any) uint64 {
	switch x := v.(type) {
	case uint64:
		return x
	case int64:
		if x >= 0 {
			return uint64(x)
		}
	}
	return 0
}

// record reads one of a node's two records (bit 0 = left).
func (db *DB) record(node uint32, bit uint) (uint32, error) {
	rs := db.meta.RecordSize
	off := int(node) * rs / 4
	if off+rs/4 > db.treeSize {
		return 0, corrupt("node %d outside the tree", node)
	}
	b := db.buf[off : off+rs/4]
	switch rs {
	case 24:
		if bit == 0 {
			return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]), nil
		}
		return uint32(b[3])<<16 | uint32(b[4])<<8 | uint32(b[5]), nil
	case 28:
		if bit == 0 {
			return uint32(b[3]&0xf0)<<20 | uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]), nil
		}
		return uint32(b[3]&0x0f)<<24 | uint32(b[4])<<16 | uint32(b[5])<<8 | uint32(b[6]), nil
	default:
		if bit == 0 {
			return binary.BigEndian.Uint32(b[0:4]), nil
		}
		return binary.BigEndian.Uint32(b[4:8]), nil
	}
}

// Lookup returns the record for a, nil when the database has none.
func (db *DB) Lookup(a netip.Addr) (any, error) {
	a = a.Unmap()
	var bits []byte
	node := uint32(0)
	if a.Is4() {
		b := a.As4()
		bits = b[:]
		if db.meta.IPVersion == 6 {
			node = db.ipv4Root
		}
	} else {
		if db.meta.IPVersion == 4 {
			return nil, nil // an IPv4-only database knows nothing about IPv6
		}
		b := a.As16()
		bits = b[:]
	}
	nc := db.meta.NodeCount
	for i := 0; i < len(bits)*8 && node < nc; i++ {
		bit := uint(bits[i/8]>>(7-uint(i%8))) & 1
		var err error
		if node, err = db.record(node, bit); err != nil {
			return nil, err
		}
	}
	switch {
	case node == nc:
		return nil, nil
	case node < nc:
		return nil, corrupt("search ended inside the tree")
	}
	off := int(node-nc) - 16
	if off < 0 || off >= len(db.data) {
		return nil, corrupt("data pointer %d outside the data section", off)
	}
	d := decoder{buf: db.data}
	v, _, err := d.decode(off, 0)
	return v, err
}

// --- data section decoder ---

// decoder reads values; budget bounds the work one lookup may do, so a
// crafted database with pointer cycles can't stall the controller.
type decoder struct {
	buf    []byte
	budget int
}

const (
	maxDepth  = 32
	maxValues = 100000
)

func (d *decoder) need(off, n int) error {
	if off < 0 || n < 0 || off+n > len(d.buf) {
		return corrupt("unexpected end of data at %d", off)
	}
	return nil
}

func (d *decoder) uintN(off, n int) (uint64, error) {
	if n > 8 {
		return 0, corrupt("integer of %d bytes", n)
	}
	if err := d.need(off, n); err != nil {
		return 0, err
	}
	var v uint64
	for _, c := range d.buf[off : off+n] {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

// decode reads the value at off and returns it with the offset after it.
func (d *decoder) decode(off, depth int) (any, int, error) {
	if depth > maxDepth {
		return nil, 0, corrupt("nesting too deep")
	}
	if d.budget++; d.budget > maxValues {
		return nil, 0, corrupt("record too large")
	}
	if err := d.need(off, 1); err != nil {
		return nil, 0, err
	}
	ctrl := d.buf[off]
	off++
	typ := int(ctrl >> 5)
	if typ == 1 { // pointer
		ss := int(ctrl>>3) & 3
		v := uint64(ctrl & 7)
		var p uint64
		switch ss {
		case 0:
			b, err := d.uintN(off, 1)
			if err != nil {
				return nil, 0, err
			}
			p, off = v<<8|b, off+1
		case 1:
			b, err := d.uintN(off, 2)
			if err != nil {
				return nil, 0, err
			}
			p, off = (v<<16|b)+2048, off+2
		case 2:
			b, err := d.uintN(off, 3)
			if err != nil {
				return nil, 0, err
			}
			p, off = (v<<24|b)+526336, off+3
		default:
			b, err := d.uintN(off, 4)
			if err != nil {
				return nil, 0, err
			}
			p, off = b, off+4
		}
		if p >= uint64(len(d.buf)) {
			return nil, 0, corrupt("pointer outside the data section")
		}
		if d.buf[p]>>5 == 1 {
			return nil, 0, corrupt("pointer to a pointer")
		}
		v2, _, err := d.decode(int(p), depth+1)
		return v2, off, err
	}
	if typ == 0 { // extended
		if err := d.need(off, 1); err != nil {
			return nil, 0, err
		}
		typ = 7 + int(d.buf[off])
		off++
		if typ < 8 || typ > 15 {
			return nil, 0, corrupt("unknown extended type %d", typ)
		}
	}
	size := int(ctrl & 0x1f)
	if size >= 29 {
		n := size - 28
		b, err := d.uintN(off, n)
		if err != nil {
			return nil, 0, err
		}
		off += n
		switch n {
		case 1:
			size = 29 + int(b)
		case 2:
			size = 285 + int(b)
		default:
			size = 65821 + int(b)
		}
	}
	switch typ {
	case 2: // UTF-8 string
		if err := d.need(off, size); err != nil {
			return nil, 0, err
		}
		return string(d.buf[off : off+size]), off + size, nil
	case 4: // bytes
		if err := d.need(off, size); err != nil {
			return nil, 0, err
		}
		return append([]byte(nil), d.buf[off:off+size]...), off + size, nil
	case 3: // double
		if size != 8 {
			return nil, 0, corrupt("double of %d bytes", size)
		}
		b, err := d.uintN(off, 8)
		return math.Float64frombits(b), off + 8, err
	case 15: // float
		if size != 4 {
			return nil, 0, corrupt("float of %d bytes", size)
		}
		b, err := d.uintN(off, 4)
		return float64(math.Float32frombits(uint32(b))), off + 4, err
	case 5, 6, 9: // uint16, uint32, uint64
		b, err := d.uintN(off, size)
		return b, off + size, err
	case 10: // uint128: only the low 64 bits matter for anything we read
		if size > 16 {
			return nil, 0, corrupt("uint128 of %d bytes", size)
		}
		if err := d.need(off, size); err != nil {
			return nil, 0, err
		}
		lo := size - 8
		if lo < 0 {
			lo = 0
		}
		b, err := d.uintN(off+lo, size-lo)
		return b, off + size, err
	case 8: // int32
		if size > 4 {
			return nil, 0, corrupt("int32 of %d bytes", size)
		}
		b, err := d.uintN(off, size)
		return int64(int32(uint32(b))), off + size, err
	case 14: // bool
		return size != 0, off, nil
	case 7: // map
		m := make(map[string]any, min(size, 64))
		for i := 0; i < size; i++ {
			k, next, err := d.decode(off, depth+1)
			if err != nil {
				return nil, 0, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, 0, corrupt("map key is not a string")
			}
			v, next2, err := d.decode(next, depth+1)
			if err != nil {
				return nil, 0, err
			}
			m[ks], off = v, next2
		}
		return m, off, nil
	case 11: // array
		a := make([]any, 0, min(size, 64))
		for i := 0; i < size; i++ {
			v, next, err := d.decode(off, depth+1)
			if err != nil {
				return nil, 0, err
			}
			a, off = append(a, v), next
		}
		return a, off, nil
	case 12, 13: // data cache container, end marker
		return nil, off, nil
	}
	return nil, 0, corrupt("unknown type %d", typ)
}
