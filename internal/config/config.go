package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

const developmentJWTSecret = "development-only-secret-change-me"

type Config struct {
	Environment          string
	HTTPAddress          string
	DatabaseURL          string
	PublicURL            string
	ClientURL            string
	JWTSecret            []byte
	AccessTokenTTL       time.Duration
	RefreshTokenTTL      time.Duration
	AllowedOrigins       []string
	GoogleClientIDs      []string
	GoogleClientSecret   string
	SMTPHost             string
	SMTPPort             int
	SMTPUsername         string
	SMTPPassword         string
	SMTPFrom             string
	SMTPStartTLS         bool
	LogOneTimeLinks      bool
	MaxSyncMutations     int
	MaxSyncChanges       int
	MaxSyncResponseBytes int
	InactiveDeviceWindow time.Duration
	RetentionRunInterval time.Duration
	EnableCompression    bool
	ReadinessTimeout     time.Duration
	ShutdownGracePeriod  time.Duration
	TrustedProxies       []netip.Prefix
}

func Load() (Config, error) {
	rawSecret := os.Getenv("JWT_SECRET")
	secret, err := loadJWTSecret(rawSecret)
	if err != nil {
		return Config{}, err
	}

	var errs []error
	accessTokenTTL := durationEnv("ACCESS_TOKEN_TTL", 15*time.Minute, false, &errs)
	refreshTokenTTL := durationEnv("REFRESH_TOKEN_TTL", 30*24*time.Hour, false, &errs)
	smtpPort := intEnv("SMTP_PORT", 587, false, &errs)
	maxSyncMutations := intEnv("MAX_SYNC_MUTATIONS", 500, false, &errs)
	maxSyncChanges := intEnv("MAX_SYNC_CHANGES", 1000, false, &errs)
	maxSyncResponseBytes := intEnv("MAX_SYNC_RESPONSE_BYTES", 512*1024, true, &errs)
	inactiveDeviceWindow := durationEnv("INACTIVE_DEVICE_WINDOW", 90*24*time.Hour, true, &errs)
	retentionRunInterval := durationEnv("RETENTION_RUN_INTERVAL", time.Hour, false, &errs)
	readinessTimeout := durationEnv("READINESS_TIMEOUT", 2*time.Second, false, &errs)
	shutdownGracePeriod := durationEnv("SHUTDOWN_GRACE_PERIOD", 10*time.Second, false, &errs)
	trustedProxies, err := trustedProxiesEnv("TRUSTED_PROXIES")
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}

	cfg := Config{
		Environment:          env("APP_ENV", "development"),
		HTTPAddress:          env("HTTP_ADDRESS", ":43781"),
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		PublicURL:            strings.TrimRight(env("PUBLIC_URL", "http://127.0.0.1:43781"), "/"),
		ClientURL:            strings.TrimRight(env("CLIENT_URL", "cubetimer://auth"), "/"),
		JWTSecret:            secret,
		AccessTokenTTL:       accessTokenTTL,
		RefreshTokenTTL:      refreshTokenTTL,
		AllowedOrigins:       csvEnv("ALLOWED_ORIGINS"),
		GoogleClientIDs:      csvEnv("GOOGLE_CLIENT_IDS"),
		GoogleClientSecret:   os.Getenv("GOOGLE_CLIENT_SECRET"),
		SMTPHost:             os.Getenv("SMTP_HOST"),
		SMTPPort:             smtpPort,
		SMTPUsername:         os.Getenv("SMTP_USERNAME"),
		SMTPPassword:         os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:             env("SMTP_FROM", "CubeTimer <noreply@localhost>"),
		SMTPStartTLS:         boolEnv("SMTP_STARTTLS", true),
		LogOneTimeLinks:      boolEnv("LOG_ONE_TIME_LINKS", true),
		MaxSyncMutations:     maxSyncMutations,
		MaxSyncChanges:       maxSyncChanges,
		MaxSyncResponseBytes: maxSyncResponseBytes,
		InactiveDeviceWindow: inactiveDeviceWindow,
		RetentionRunInterval: retentionRunInterval,
		EnableCompression:    boolEnv("ENABLE_COMPRESSION", true),
		ReadinessTimeout:     readinessTimeout,
		ShutdownGracePeriod:  shutdownGracePeriod,
		TrustedProxies:       trustedProxies,
	}

	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.Environment == "production" {
		if rawSecret == "" {
			return Config{}, errors.New("JWT_SECRET must be set to a random value in production (openssl rand -base64 48)")
		}
		if string(secret) == developmentJWTSecret {
			return Config{}, errors.New("JWT_SECRET must not use the development default in production (openssl rand -base64 48)")
		}
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
		return []byte(developmentJWTSecret), nil
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

// durationEnv and intEnv record malformed or out-of-range values in errs
// instead of silently falling back to the default.
func durationEnv(key string, fallback time.Duration, allowZero bool, errs *[]error) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: invalid duration: %w", key, err))
	} else if parsed < 0 || (parsed == 0 && !allowZero) {
		*errs = append(*errs, fmt.Errorf("%s must be positive", key))
	}
	return parsed
}

func intEnv(key string, fallback int, allowZero bool, errs *[]error) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: invalid integer: %w", key, err))
	} else if parsed < 0 || (parsed == 0 && !allowZero) {
		*errs = append(*errs, fmt.Errorf("%s must be positive", key))
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

func trustedProxiesEnv(key string) ([]netip.Prefix, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	var prefixes []netip.Prefix
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			addr, errAddr := netip.ParseAddr(part)
			if errAddr != nil {
				return nil, fmt.Errorf("%s: invalid CIDR or IP address: %q", key, part)
			}
			if addr.Is6() {
				prefix = netip.PrefixFrom(addr, 128)
			} else {
				prefix = netip.PrefixFrom(addr, 32)
			}
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func (c Config) SMTPAddress() string {
	return fmt.Sprintf("%s:%d", c.SMTPHost, c.SMTPPort)
}
