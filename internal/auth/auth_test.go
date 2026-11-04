package auth

import (
	"errors"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("hunter2hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPassword("hunter2hunter2", h); !ok || err != nil {
		t.Fatalf("correct password rejected: %v %v", ok, err)
	}
	if ok, _ := VerifyPassword("wrong", h); ok {
		t.Fatal("wrong password accepted")
	}
	if _, err := VerifyPassword("x", "garbage"); !errors.Is(err, ErrBadHash) {
		t.Fatalf("want ErrBadHash, got %v", err)
	}
}

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                 "/",
		"/library?kind=1":  "/library?kind=1",
		"//evil.example":   "/",
		"/\\evil.example":  "/",
		"https://evil.com": "/",
	}
	for in, want := range cases {
		if got := SafeNext(in); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
