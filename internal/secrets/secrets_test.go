package secrets

import (
	"bytes"
	"errors"
	"testing"
)

func mustBox(t *testing.T, cur, prev string) *Box {
	t.Helper()
	b, err := New(cur, prev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := mustBox(t, "0123456789abcdef0123456789abcdef", "")
	for _, pt := range [][]byte{{}, []byte("api-key-123"), bytes.Repeat([]byte{7}, 4096)} {
		ct, err := b.Encrypt(pt)
		if err != nil {
			t.Fatal(err)
		}
		if len(pt) > 0 && bytes.Contains(ct, pt) {
			t.Fatal("ciphertext contains plaintext")
		}
		got, err := b.Decrypt(ct)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("got %q, want %q", got, pt)
		}
	}
	a, _ := b.Encrypt([]byte("same"))
	c, _ := b.Encrypt([]byte("same"))
	if bytes.Equal(a, c) {
		t.Fatal("nonces must differ between encryptions")
	}
}

func TestTamperDetection(t *testing.T) {
	b := mustBox(t, "0123456789abcdef0123456789abcdef", "")
	ct, err := b.Encrypt([]byte("secret value"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range ct {
		bad := bytes.Clone(ct)
		bad[i] ^= 0x01
		if _, err := b.Decrypt(bad); err == nil {
			t.Fatalf("flipping byte %d was not detected", i)
		}
	}
	if _, err := b.Decrypt(ct[:10]); !errors.Is(err, ErrMalformed) {
		t.Fatalf("truncated: got %v", err)
	}
	if _, err := b.Decrypt(ct[:len(ct)-1]); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("short tag: got %v", err)
	}
}

func TestRotation(t *testing.T) {
	oldKey, newKey := "old-key-old-key-old-key-old-key-", "new-key-new-key-new-key-new-key-"
	old := mustBox(t, oldKey, "")
	ct, _ := old.Encrypt([]byte("token"))

	rotated := mustBox(t, newKey, oldKey)
	got, err := rotated.Decrypt(ct)
	if err != nil || string(got) != "token" {
		t.Fatalf("decrypt with previous key: %q, %v", got, err)
	}
	if !rotated.NeedsRotation(ct) {
		t.Fatal("old ciphertext should need rotation")
	}
	fresh, _ := rotated.Encrypt(got)
	if rotated.NeedsRotation(fresh) {
		t.Fatal("new ciphertext should use the current key")
	}
	if _, err := old.Decrypt(fresh); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("old box decrypting new ciphertext: %v", err)
	}

	newOnly := mustBox(t, newKey, "")
	if _, err := newOnly.Decrypt(ct); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("after dropping previous key: %v", err)
	}
}

func TestEmptyKey(t *testing.T) {
	if _, err := New("", ""); err == nil {
		t.Fatal("empty key must be rejected")
	}
}
