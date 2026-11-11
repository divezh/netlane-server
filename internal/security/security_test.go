package security

import (
	"bytes"
	"testing"
)

func TestPasswordAndSealedCredentials(t *testing.T) {
	hash, err := HashPassword("correct-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "correct-password-123") || CheckPassword(hash, "wrong") {
		t.Fatal("password verification failed")
	}
	key := bytes.Repeat([]byte{7}, 32)
	sealed, err := Seal(key, "user-a", "vpn-password-123")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Open(key, "user-a", sealed)
	if err != nil || plain != "vpn-password-123" {
		t.Fatal("cannot open credential")
	}
	if _, err = Open(key, "user-b", sealed); err == nil {
		t.Fatal("ciphertext was not bound to user ID")
	}
	if _, err = Open(bytes.Repeat([]byte{9}, 32), "user-a", sealed); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err = HashPassword("short"); err == nil {
		t.Fatal("weak password accepted")
	}
}
