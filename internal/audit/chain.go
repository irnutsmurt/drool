package audit

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"strconv"
)

// The audit history is a hash chain (ARCHITECTURE-ALPHA §33.4). Each record
// commits to the one before it, so changing, removing or reordering a stored
// record is detectable by recomputing the chain. It is tamper-evident, not
// tamper-proof: whoever can rewrite the whole database (and holds the master
// key, for checkpoints) can rewrite the whole chain. Copies outside the
// controller (the remote audit sink) are what survive that.

// ChainFormat names the canonical encoding below. It is part of every hash,
// so a future format can't be confused with this one.
const ChainFormat = "drool-audit-v1"

// Chain actions written by DROOL itself.
const (
	ActionChainStart = "audit.chain.start" // the first link; commits to the records written before the chain
	ActionCheckpoint = "audit.checkpoint"  // an authenticated seal over everything before it
)

// Link is one chained audit record exactly as stored.
type Link struct {
	Seq        int64
	ID         string
	Ts         int64 // Unix ms
	ActorType  string
	ActorID    string
	ActorName  string
	Action     string
	TargetType string
	TargetID   string
	Result     string
	SourceIP   string
	RequestID  string
	Details    string // details_json, byte for byte as stored (never re-serialized)
	PrevHash   []byte // the previous link's hash; 32 zero bytes for the first link
	Hash       []byte
}

// zeroHash is the first link's prev_hash.
var zeroHash = make([]byte, sha256.Size)

// netstring appends "<len>:<bytes>," to b. Length-prefixed fields can't be
// confused with each other whatever they contain.
func netstring(b *bytes.Buffer, s string) {
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
	b.WriteByte(',')
}

// Canonical returns the bytes a link's hash covers: netstrings of the format
// name, the previous hash in lowercase hex, and every field in this order:
// seq, id, ts, actor_type, actor_id, actor_name, action, target_type,
// target_id, result, source_ip, request_id, details. Numbers are decimal.
// The encoding is documented in the user guide (docs/wiki/Remote-Audit-Copy-Reference.md) so receivers of the remote copy
// can verify it without DROOL.
func (l *Link) Canonical() []byte {
	var b bytes.Buffer
	netstring(&b, ChainFormat)
	netstring(&b, hex.EncodeToString(l.PrevHash))
	netstring(&b, strconv.FormatInt(l.Seq, 10))
	netstring(&b, l.ID)
	netstring(&b, strconv.FormatInt(l.Ts, 10))
	netstring(&b, l.ActorType)
	netstring(&b, l.ActorID)
	netstring(&b, l.ActorName)
	netstring(&b, l.Action)
	netstring(&b, l.TargetType)
	netstring(&b, l.TargetID)
	netstring(&b, l.Result)
	netstring(&b, l.SourceIP)
	netstring(&b, l.RequestID)
	netstring(&b, l.Details)
	return b.Bytes()
}

// ComputeHash is SHA-256 over Canonical.
func (l *Link) ComputeHash() []byte {
	s := sha256.Sum256(l.Canonical())
	return s[:]
}

// legacyFormat names the encoding of records written before the chain.
const legacyFormat = "drool-audit-legacy-v1"

// legacyDigest accumulates the digest the first link records over every
// record written before the chain existed, in id order.
type legacyDigest struct {
	h     hash.Hash
	n     int64
	first string
	last  string
}

func newLegacyDigest() *legacyDigest {
	h := sha256.New()
	h.Write([]byte(legacyFormat))
	return &legacyDigest{h: h}
}

// add hashes one legacy record (its stored prev_hash column is included:
// it was reserved and should be empty).
func (d *legacyDigest) add(l *Link) {
	var b bytes.Buffer
	netstring(&b, hex.EncodeToString(l.PrevHash))
	netstring(&b, l.ID)
	netstring(&b, strconv.FormatInt(l.Ts, 10))
	netstring(&b, l.ActorType)
	netstring(&b, l.ActorID)
	netstring(&b, l.ActorName)
	netstring(&b, l.Action)
	netstring(&b, l.TargetType)
	netstring(&b, l.TargetID)
	netstring(&b, l.Result)
	netstring(&b, l.SourceIP)
	netstring(&b, l.RequestID)
	netstring(&b, l.Details)
	d.h.Write(b.Bytes())
	if d.n == 0 {
		d.first = l.ID
	}
	d.last = l.ID
	d.n++
}

func (d *legacyDigest) hex() string { return hex.EncodeToString(d.h.Sum(nil)) }

// checkpointFormat names the message a checkpoint's MAC covers.
const checkpointFormat = "drool-audit-checkpoint-v1"

// CheckpointPurpose derives the checkpoint key from the master key
// (secrets.Box.DeriveKey), so the key is never stored anywhere.
const CheckpointPurpose = "drool audit checkpoint v1"

// checkpointMAC authenticates "the chain chainID up to and including seq,
// whose hash is covered" with key.
func checkpointMAC(key []byte, chainID string, seq int64, covered []byte) []byte {
	var b bytes.Buffer
	netstring(&b, checkpointFormat)
	netstring(&b, chainID)
	netstring(&b, strconv.FormatInt(seq, 10))
	netstring(&b, hex.EncodeToString(covered))
	m := hmac.New(sha256.New, key)
	m.Write(b.Bytes())
	return m.Sum(nil)
}
