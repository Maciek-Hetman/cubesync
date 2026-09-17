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
	"sync"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory      = 64 * 1024
	argonIterations  = 3
	argonParallelism = 2
	argonSaltLength  = 16
	argonKeyLength   = 32
)

var (
	argonSem = make(chan struct{}, max(2, min(4, runtime.NumCPU())))

	dummyHash string
	hashOnce  sync.Once
)

func getDummyHash() string {
	hashOnce.Do(func() {
		h, _ := hashPasswordInternal("dummy-password-for-timing-mitigation")
		dummyHash = h
	})
	return dummyHash
}

func hashPassword(ctx context.Context, password string) (string, error) {
	select {
	case argonSem <- struct{}{}:
		defer func() { <-argonSem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return hashPasswordInternal(password)
}

func hashPasswordInternal(password string) (string, error) {
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)
	return fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory,
		argonIterations,
		argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func verifyPassword(ctx context.Context, password, encoded string) (bool, error) {
	select {
	case argonSem <- struct{}{}:
		defer func() { <-argonSem }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return verifyPasswordInternal(password, encoded)
}

func verifyPasswordInternal(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, errors.New("invalid password hash format")
	}
	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func validatePassword(password string) error {
	if len(password) < 10 {
		return errors.New("password must contain at least 10 characters")
	}
	if len(password) > 128 {
		return errors.New("password cannot exceed 128 characters")
	}
	return nil
}
