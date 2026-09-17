package config

import (
	"encoding/base64"
	"testing"
)

func TestProductionConfigRejectsOneTimeLinkLogging(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("JWT_SECRET", base64.StdEncoding.EncodeToString(make([]byte, 48)))
	t.Setenv("LOG_ONE_TIME_LINKS", "true")
	if _, err := Load(); err == nil {
		t.Fatal("expected production one-time link logging to be rejected")
	}
}

func TestProductionConfigAcceptsStrongSecret(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("JWT_SECRET", base64.StdEncoding.EncodeToString(make([]byte, 48)))
	t.Setenv("LOG_ONE_TIME_LINKS", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.JWTSecret) != 48 {
		t.Fatalf("decoded secret length is %d", len(cfg.JWTSecret))
	}
}

func TestProductionConfigRejectsUnsetSecret(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("LOG_ONE_TIME_LINKS", "false")
	if _, err := Load(); err == nil {
		t.Fatal("expected production config with unset JWT_SECRET to be rejected")
	}
}

func TestProductionConfigRejectsDevelopmentDefaultSecret(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("JWT_SECRET", developmentJWTSecret)
	t.Setenv("LOG_ONE_TIME_LINKS", "false")
	if _, err := Load(); err == nil {
		t.Fatal("expected production config using the development default secret to be rejected")
	}
}

func TestDevelopmentConfigAllowsUnsetSecret(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("JWT_SECRET", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.JWTSecret) != developmentJWTSecret {
		t.Fatalf("expected development fallback secret, got %q", cfg.JWTSecret)
	}
}
