package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Maciek-Hetman/cubing-sync-backend/internal/config"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

type FederatedInput struct {
	IDToken      string `json:"id_token,omitempty"`
	Code         string `json:"code,omitempty"`
	ClientID     string `json:"client_id"`
	RedirectURI  string `json:"redirect_uri,omitempty"`
	CodeVerifier string `json:"code_verifier,omitempty"`
	Nonce        string `json:"nonce"`
}

type FederatedIdentity struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
}

type FederatedVerifier interface {
	Verify(context.Context, string, FederatedInput) (FederatedIdentity, error)
}

type OIDCVerifier struct {
	config    config.Config
	providers map[string]oidcProviderMetadata
	keySets   map[string]oidc.KeySet
}

type oidcProviderMetadata struct {
	issuer  string
	jwksURL string
}

func NewOIDCVerifier(cfg config.Config) *OIDCVerifier {
	ctx := context.Background()
	providers := map[string]oidcProviderMetadata{
		"google": {issuer: "https://accounts.google.com", jwksURL: "https://www.googleapis.com/oauth2/v3/certs"},
		"apple":  {issuer: "https://appleid.apple.com", jwksURL: "https://appleid.apple.com/auth/keys"},
	}
	keySets := make(map[string]oidc.KeySet, len(providers))
	for name, p := range providers {
		keySets[name] = oidc.NewRemoteKeySet(ctx, p.jwksURL)
	}
	return &OIDCVerifier{
		config:    cfg,
		providers: providers,
		keySets:   keySets,
	}
}

func (v *OIDCVerifier) Verify(ctx context.Context, provider string, input FederatedInput) (FederatedIdentity, error) {
	if input.Nonce == "" {
		return FederatedIdentity{}, authError("invalid_social_token", "nonce is required")
	}
	allowed := v.allowedClientIDs(provider)
	if input.ClientID == "" || !slices.Contains(allowed, input.ClientID) {
		return FederatedIdentity{}, authError("invalid_social_token", "client_id is not allowed")
	}
	rawToken := input.IDToken
	if rawToken == "" {
		var err error
		rawToken, err = v.exchangeCode(ctx, provider, input)
		if err != nil {
			return FederatedIdentity{}, err
		}
	}
	if rawToken == "" {
		return FederatedIdentity{}, authError("invalid_social_token", "id_token or code is required")
	}

	metadata, ok := v.providers[provider]
	if !ok {
		return FederatedIdentity{}, authError("unsupported_provider", "provider must be google or apple")
	}
	keySet, ok := v.keySets[provider]
	if !ok {
		keySet = oidc.NewRemoteKeySet(ctx, metadata.jwksURL)
	}
	verifier := oidc.NewVerifier(metadata.issuer, keySet, &oidc.Config{SkipClientIDCheck: true})
	token, err := verifier.Verify(ctx, rawToken)
	if err != nil {
		return FederatedIdentity{}, fmt.Errorf("verify id token: %w", err)
	}
	var claims struct {
		Subject       string           `json:"sub"`
		Email         string           `json:"email"`
		EmailVerified json.RawMessage  `json:"email_verified"`
		Nonce         string           `json:"nonce"`
		Audience      jwt.ClaimStrings `json:"aud"`
	}
	if err := token.Claims(&claims); err != nil {
		return FederatedIdentity{}, authError("invalid_social_token", "identity claims are invalid")
	}
	if !slices.Contains([]string(claims.Audience), input.ClientID) || claims.Nonce != input.Nonce {
		return FederatedIdentity{}, authError("invalid_social_token", "identity token audience or nonce is invalid")
	}
	verified := parseBooleanClaim(claims.EmailVerified)
	if claims.Subject == "" || claims.Email == "" || !verified {
		return FederatedIdentity{}, authError("invalid_social_token", "provider must supply a verified email")
	}
	return FederatedIdentity{
		Provider: provider, Subject: claims.Subject, Email: strings.ToLower(strings.TrimSpace(claims.Email)),
		EmailVerified: true,
	}, nil
}

func (v *OIDCVerifier) exchangeCode(ctx context.Context, provider string, input FederatedInput) (string, error) {
	if input.Code == "" || input.RedirectURI == "" || input.CodeVerifier == "" {
		return "", authError("invalid_social_code", "code, redirect_uri, and code_verifier are required")
	}
	clientSecret := ""
	endpoint := oauth2.Endpoint{}
	switch provider {
	case "google":
		clientSecret = v.config.GoogleClientSecret
		endpoint = oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		}
	case "apple":
		var err error
		clientSecret, err = v.appleClientSecret(input.ClientID)
		if err != nil {
			return "", err
		}
		endpoint = oauth2.Endpoint{
			AuthURL:  "https://appleid.apple.com/auth/authorize",
			TokenURL: "https://appleid.apple.com/auth/token",
		}
	default:
		return "", authError("unsupported_provider", "provider must be google or apple")
	}
	oauthConfig := oauth2.Config{
		ClientID: input.ClientID, ClientSecret: clientSecret, RedirectURL: input.RedirectURI,
		Endpoint: endpoint, Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
	}
	token, err := oauthConfig.Exchange(ctx, input.Code, oauth2.SetAuthURLParam("code_verifier", input.CodeVerifier))
	if err != nil {
		return "", authError("invalid_social_code", "authorization code exchange failed")
	}
	raw, _ := token.Extra("id_token").(string)
	return raw, nil
}

func (v *OIDCVerifier) appleClientSecret(clientID string) (string, error) {
	block, _ := pem.Decode([]byte(strings.ReplaceAll(v.config.ApplePrivateKey, `\n`, "\n")))
	if block == nil {
		return "", authError("provider_not_configured", "Apple private key is not configured")
	}
	keyValue, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse Apple private key: %w", err)
	}
	key, ok := keyValue.(*ecdsa.PrivateKey)
	if !ok {
		return "", errors.New("Apple private key must be an EC private key")
	}
	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{
		Issuer: v.config.AppleTeamID, Subject: clientID, Audience: jwt.ClaimStrings{"https://appleid.apple.com"},
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = v.config.AppleKeyID
	return token.SignedString(key)
}

func (v *OIDCVerifier) allowedClientIDs(provider string) []string {
	switch provider {
	case "google":
		return v.config.GoogleClientIDs
	case "apple":
		return v.config.AppleClientIDs
	default:
		return nil
	}
}

func parseBooleanClaim(raw json.RawMessage) bool {
	var value bool
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text == "true"
	}
	return false
}
