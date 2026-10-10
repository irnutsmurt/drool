package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP per RFC 6238 with the parameters every authenticator app supports:
// HMAC-SHA1, 6 digits, 30-second steps, a 160-bit secret.
const (
	totpDigits = 6
	totpPeriod = 30
	// totpSkew accepts the previous and the next code too (clocks of
	// phones and servers differ a little).
	totpSkew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// newTOTPSecret returns 20 random bytes.
func newTOTPSecret() ([]byte, error) {
	b := make([]byte, 20)
	_, err := rand.Read(b)
	return b, err
}

// totpStep is the time step a moment falls in.
func totpStep(t time.Time) int64 { return t.Unix() / totpPeriod }

func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}

// checkTOTP returns the matching step for code at now (within the skew), or
// 0. Steps at or before lastStep never match: a code works once.
func checkTOTP(secret []byte, code string, now time.Time, lastStep int64) int64 {
	code = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, code)
	if len(code) != totpDigits {
		return 0
	}
	cur := totpStep(now)
	for d := -totpSkew; d <= totpSkew; d++ {
		st := cur + int64(d)
		if st <= lastStep {
			continue
		}
		if hmac.Equal([]byte(totpCode(secret, st)), []byte(code)) {
			return st
		}
	}
	return 0
}

// otpauthURI is what authenticator apps scan.
func otpauthURI(secret []byte, issuer, account string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", b32.EncodeToString(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// Recovery codes: 16 characters from an alphabet without look-alikes
// (about 79 bits), shown as four groups of four.
const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // 31 symbols
const recoveryCodes = 10

func newRecoveryCode() (string, error) {
	// 16 symbols of 31 > 2^79; rejection sampling keeps them uniform.
	var b strings.Builder
	buf := make([]byte, 1)
	for b.Len() < 19 {
		if b.Len() == 4 || b.Len() == 9 || b.Len() == 14 {
			b.WriteByte('-')
			continue
		}
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if int(buf[0]) >= 31*8 {
			continue
		}
		b.WriteByte(recoveryAlphabet[int(buf[0])%31])
	}
	return b.String(), nil
}

// normalizeRecoveryCode accepts the code with or without dashes, spaces,
// any case.
func normalizeRecoveryCode(c string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r == '-' || r == ' ':
			return -1
		}
		return r
	}, c)
}

func hashRecoveryCode(c string) []byte {
	s := sha256.Sum256([]byte("drool-recovery-code\x00" + normalizeRecoveryCode(c)))
	return s[:]
}
