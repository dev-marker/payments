package auth_test

import (
	"strings"
	"testing"

	"github.com/fiston-user/payments/internal/auth"
)

const password = "correct horse battery staple"

func TestPassword(t *testing.T) {
	t.Parallel()
	hash, err := auth.HashPassword(t.Context(), password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("hash = %s, want a PHC string with the default params", hash)
	}
	// The same password again: a new salt, so a different hash.
	again, err := auth.HashPassword(t.Context(), password)
	if err != nil || again == hash {
		t.Fatalf("second hash = %s, %v: want a different hash", again, err)
	}

	tests := []struct {
		name, password, hash string
		match                bool
	}{
		{"right password", password, hash, true},
		{"right password, other salt", password, again, true},
		{"one letter more", password + "s", hash, false},
		{"other case", strings.ToUpper(password), hash, false},
		{"empty", "", hash, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, err := auth.VerifyPassword(t.Context(), tt.password, tt.hash)
			if err != nil || match != tt.match {
				t.Errorf("match = %t, %v; want %t", match, err, tt.match)
			}
		})
	}

	if _, err := auth.VerifyPassword(t.Context(), password, "$2a$10$notargon"); err == nil {
		t.Error("a hash in another format verified without an error")
	}
}
