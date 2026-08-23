package auth

import "testing"

func TestPasswordHashRoundTrip(t *testing.T) {
	t.Parallel()
	encoded, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := verifyPassword("correct horse battery staple", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatal("expected password to verify")
	}
	valid, err = verifyPassword("wrong password", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if valid {
		t.Fatal("wrong password verified")
	}
}

func TestPasswordValidation(t *testing.T) {
	t.Parallel()
	if err := validatePassword("short"); err == nil {
		t.Fatal("expected short password to fail")
	}
	if err := validatePassword("long enough password"); err != nil {
		t.Fatalf("expected valid password: %v", err)
	}
}

func TestMalformedPasswordHash(t *testing.T) {
	t.Parallel()
	if _, err := verifyPassword("password", "not-an-argon-hash"); err == nil {
		t.Fatal("expected malformed hash to fail")
	}
}
