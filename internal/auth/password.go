package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are argon2id's cost settings. Every hash records the ones it was
// made with, so we can raise them later, and old hashes still verify.
type Params struct {
	Memory  uint32 // in KiB
	Time    uint32 // passes over the memory
	Threads uint8
}

// DefaultParams is RFC 9106's second recommendation: 64 MiB of memory, three
// passes and four lanes. About 50 ms here, and very expensive for anyone
// guessing passwords on GPUs.
var DefaultParams = Params{Memory: 64 * 1024, Time: 3, Threads: 4}

const (
	saltLen = 16 // bytes
	keyLen  = 32 // bytes
)

var errMalformedHash = errors.New("malformed password hash")

// hashSlots limits how many hashes run at once. Each one holds 64 MiB, so a
// burst of logins waits here, instead of running us out of memory.
var hashSlots = make(chan struct{}, runtime.GOMAXPROCS(0))

// b64 is the PHC string format's base64: standard alphabet, no padding.
var b64 = base64.RawStdEncoding

// HashPassword hashes password with argon2id and a new random salt. It
// returns a PHC string, like $argon2id$v=19$m=65536,t=3,p=4$salt$hash, which
// holds everything VerifyPassword needs.
func HashPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, saltLen)
	rand.Read(salt) // never fails: it crashes the program instead
	p := DefaultParams
	key, err := derive(ctx, password, salt, p, keyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches encoded, a hash made by
// HashPassword. It hashes password again, with the stored salt and params.
func VerifyPassword(ctx context.Context, password, encoded string) (bool, error) {
	p, salt, key, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	other, err := derive(ctx, password, salt, p, uint32(len(key)))
	if err != nil {
		return false, err
	}
	// Constant time: how long this takes says nothing about where the two
	// hashes start to differ.
	return subtle.ConstantTimeCompare(key, other) == 1, nil
}

// derive runs argon2id as soon as a slot is free, or gives up when ctx ends.
func derive(ctx context.Context, password string, salt []byte, p Params, n uint32) ([]byte, error) {
	select {
	case hashSlots <- struct{}{}:
		defer func() { <-hashSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, n), nil
}

// decodeHash splits a PHC string into its params, salt and hash.
func decodeHash(encoded string) (p Params, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$") // "", "argon2id", "v=19", params, salt, hash
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return p, nil, nil, errMalformedHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return p, nil, nil, errMalformedHash
	}
	salt, err = b64.DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, errMalformedHash
	}
	key, err = b64.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return p, nil, nil, errMalformedHash
	}
	return p, salt, key, nil
}
