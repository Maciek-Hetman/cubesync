package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment         string
	HTTPAddress         string
	DatabaseURL         string
	PublicURL           string
	ClientURL           string
	JWTSecret           []byte
	AccessTokenTTL      time.Duration
	RefreshTokenTTL     time.Duration
	AllowedOrigins      []string
	GoogleClientIDs     []string
	GoogleClientSecret  string
	AppleClientIDs      []string
	AppleTeamID         string
	AppleKeyID          string
	ApplePrivateKey     string
	SMTPHost            string
	SMTPPort            int
	SMTPUsername        string
	SMTPPassword        string
	SMTPFrom            string
	SMTPStartTLS        bool
	LogOneTimeLinks     bool
	MaxSyncMutations    int
	MaxSyncChanges      int
	MaxSyncResponseBytes int
	InactiveDeviceWindow time.Duration
	RetentionRunInterval time.Duration
	EnableCompression    bool
	ReadinessTimeout    time.Duration
	ShutdownGracePeriod time.Duration
}

func Load() (Config, error) {
	secret, err := loadJWTSecret(os.Getenv("JWT_SECRET"))
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Environment:         env("APP_ENV", "development"),
		HTTPAddress:         env("HTTP_ADDRESS", ":43781"),
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		PublicURL:           strings.TrimRight(env("PUBLIC_URL", "http://127.0.0.1:43781"), "/"),
		ClientURL:           strings.TrimRight(env("CLIENT_URL", "cubetimer://auth"), "/"),
		JWTSecret:           secret,
		AccessTokenTTL:      durationEnv("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL:     durationEnv("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		AllowedOrigins:      csvEnv("ALLOWED_ORIGINS"),
		GoogleClientIDs:     csvEnv("GOOGLE_CLIENT_IDS"),
		GoogleClientSecret:  os.Getenv("GOOGLE_CLIENT_SECRET"),
		AppleClientIDs:      csvEnv("APPLE_CLIENT_IDS"),
		AppleTeamID:         os.Getenv("APPLE_TEAM_ID"),
		AppleKeyID:          os.Getenv("APPLE_KEY_ID"),
		ApplePrivateKey:     os.Getenv("APPLE_PRIVATE_KEY"),
		SMTPHost:            os.Getenv("SMTP_HOST"),
		SMTPPort:            intEnv("SMTP_PORT", 587),
		SMTPUsername:        os.Getenv("SMTP_USERNAME"),
		SMTPPassword:        os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:            env("SMTP_FROM", "CubeTimer <noreply@localhost>"),
		SMTPStartTLS:        boolEnv("SMTP_STARTTLS", true),
		LogOneTimeLinks:     boolEnv("LOG_ONE_TIME_LINKS", true),
		MaxSyncMutations:    intEnv("MAX_SYNC_MUTATIONS", 500),
		MaxSyncChanges:      intEnv("MAX_SYNC_CHANGES", 1000),
		MaxSyncResponseBytes: intEnv("MAX_SYNC_RESPONSE_BYTES", 512*1024),
		InactiveDeviceWindow: durationEnv("INACTIVE_DEVICE_WINDOW", 90*24*time.Hour),
		RetentionRunInterval: durationEnv("RETENTION_RUN_INTERVAL", 1*time.Hour),
		EnableCompression:    boolEnv("ENABLE_COMPRESSION", true),
		ReadinessTimeout:    durationEnv("READINESS_TIMEOUT", 2*time.Second),
		ShutdownGracePeriod: durationEnv("SHUTDOWN_GRACE_PERIOD", 10*time.Second),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.Environment == "production" {
		if len(cfg.JWTSecret) < 32 {
			return Config{}, errors.New("JWT_SECRET must decode to at least 32 bytes in production")
		}
		if cfg.LogOneTimeLinks {
			return Config{}, errors.New("LOG_ONE_TIME_LINKS must be false in production")
		}
	}
	return cfg, nil
}

func loadJWTSecret(value string) ([]byte, error) {
	if value == "" {
		return []byte("development-only-secret-change-me"), nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return []byte(value), nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func csvEnv(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func intEnv(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func boolEnv(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func (c Config) SMTPAddress() string {
	return fmt.Sprintf("%s:%d", c.SMTPHost, c.SMTPPort)
}
