package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (ARCHITECTURE-ALPHA §12.1).
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 2
	argonKeyLen    = 32
	argonSaltLen   = 16
)

// MinPasswordLength is the minimum accepted password length in characters.
const MinPasswordLength = 12

// MaxPasswordLength bounds hashing cost for hostile input.
const MaxPasswordLength = 1024

var ErrWeakPassword = fmt.Errorf("password must be between %d and %d characters", MinPasswordLength, MaxPasswordLength)

// ValidatePassword checks password policy.
func ValidatePassword(pw string) error {
	n := utf8.RuneCountInString(pw)
	if n < MinPasswordLength || len(pw) > MaxPasswordLength {
		return ErrWeakPassword
	}
	return nil
}

// HashPassword returns an argon2id PHC-format hash.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

var errBadHash = errors.New("invalid password hash format")

// VerifyPassword reports whether pw matches the encoded hash.
func VerifyPassword(encoded, pw string) (bool, error) {
	if len(pw) > MaxPasswordLength {
		return false, nil
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false, errBadHash
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(pw), salt, t, mem, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified against when a username does not exist, so that
// response timing does not reveal which usernames are valid.
var dummyHash = func() string {
	h, err := HashPassword("not-a-real-password-placeholder")
	if err != nil {
		panic(err)
	}
	return h
}()
