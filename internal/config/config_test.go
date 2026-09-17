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

func TestConfigRejectsMalformedDuration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("ACCESS_TOKEN_TTL", "not-a-duration")
	_, err := Load()
	if err == nil {
		t.Fatal("expected malformed ACCESS_TOKEN_TTL to be rejected")
	}
}

func TestConfigRejectsMalformedInt(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("SMTP_PORT", "not-an-int")
	_, err := Load()
	if err == nil {
		t.Fatal("expected malformed SMTP_PORT to be rejected")
	}
}

func TestConfigRejectsNonPositiveAccessTokenTTL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("ACCESS_TOKEN_TTL", "0s")
	_, err := Load()
	if err == nil {
		t.Fatal("expected non-positive ACCESS_TOKEN_TTL to be rejected")
	}
}

func TestConfigRejectsNonPositiveSMTPPort(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("SMTP_PORT", "0")
	_, err := Load()
	if err == nil {
		t.Fatal("expected non-positive SMTP_PORT to be rejected")
	}
}

func TestConfigParsesTrustedProxies(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("TRUSTED_PROXIES", "172.30.0.10/32,192.168.1.0/24")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustedProxies) != 2 {
		t.Fatalf("expected 2 trusted proxies, got %d", len(cfg.TrustedProxies))
	}
}

func TestConfigParsesTrustedProxiesAsBareIPs(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("TRUSTED_PROXIES", "172.30.0.10")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustedProxies) != 1 {
		t.Fatalf("expected 1 trusted proxy, got %d", len(cfg.TrustedProxies))
	}
}

func TestConfigRejectsInvalidTrustedProxy(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("TRUSTED_PROXIES", "not-an-ip")
	_, err := Load()
	if err == nil {
		t.Fatal("expected invalid TRUSTED_PROXIES to be rejected")
	}
}
