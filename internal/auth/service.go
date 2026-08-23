package auth

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/Maciek-Hetman/cubing-sync-backend/internal/config"
	storedb "github.com/Maciek-Hetman/cubing-sync-backend/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	verificationTokenTTL = 24 * time.Hour
	passwordResetTTL     = time.Hour
)

type Service struct {
	pool      *pgxpool.Pool
	config    config.Config
	tokens    *TokenManager
	mailer    Mailer
	federated FederatedVerifier
	now       func() time.Time
}

type User struct {
	ID            uuid.UUID `json:"id"`
	Email         string    `json:"email"`
	EmailVerified bool      `json:"email_verified"`
}

type Session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	User         User   `json:"user"`
}

func NewService(cfg config.Config, pool *pgxpool.Pool, mailer Mailer, federated FederatedVerifier) *Service {
	return &Service{
		pool: pool, config: cfg, tokens: NewTokenManager(cfg.JWTSecret, cfg.PublicURL, cfg.AccessTokenTTL),
		mailer: mailer, federated: federated, now: time.Now,
	}
}

func (s *Service) TokenManager() *TokenManager { return s.tokens }

func (s *Service) Register(ctx context.Context, email, password string) error {
	email = normalizeEmail(email)
	if !validEmail(email) {
		return authError("invalid_email", "email address is invalid")
	}
	if err := validatePassword(password); err != nil {
		return authError("invalid_password", err.Error())
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return err
	}
	rawToken, hash, err := randomToken()
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := storedb.New(tx)
	userID := uuid.New()
	if _, err := q.CreateUser(ctx, storedb.CreateUserParams{ID: userID, Email: email}); err != nil {
		if isUniqueViolation(err) {
			return authError("email_in_use", "an account already exists for this email")
		}
		return err
	}
	if err := q.CreatePasswordCredential(ctx, storedb.CreatePasswordCredentialParams{
		UserID: userID, PasswordHash: passwordHash,
	}); err != nil {
		return err
	}
	if err := q.CreateOneTimeToken(ctx, storedb.CreateOneTimeTokenParams{
		ID: uuid.New(), UserID: userID, Kind: "verify_email", TokenHash: hash,
		ExpiresAt: s.now().UTC().Add(verificationTokenTTL),
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if err := s.mailer.SendVerification(ctx, email, rawToken); err != nil {
		return authError("email_delivery_failed", "account created, but verification email could not be sent")
	}
	return nil
}

func (s *Service) ResendVerification(ctx context.Context, email string) error {
	email = normalizeEmail(email)
	q := storedb.New(s.pool)
	user, err := q.GetUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) || user.EmailVerifiedAt != nil {
		return nil
	}
	if err != nil {
		return err
	}
	rawToken, hash, err := randomToken()
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tq := storedb.New(tx)
	if err := tq.InvalidateUserOneTimeTokens(ctx, storedb.InvalidateUserOneTimeTokensParams{
		UserID: user.ID, Kind: "verify_email",
	}); err != nil {
		return err
	}
	if err := tq.CreateOneTimeToken(ctx, storedb.CreateOneTimeTokenParams{
		ID: uuid.New(), UserID: user.ID, Kind: "verify_email", TokenHash: hash,
		ExpiresAt: s.now().UTC().Add(verificationTokenTTL),
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.mailer.SendVerification(ctx, user.Email, rawToken)
}

func (s *Service) VerifyEmail(ctx context.Context, rawToken string) (Session, error) {
	return s.consumeOneTimeToken(ctx, rawToken, "verify_email", func(ctx context.Context, q *storedb.Queries, token storedb.OneTimeToken) error {
		return q.SetUserEmailVerified(ctx, token.UserID)
	})
}

func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	q := storedb.New(s.pool)
	credential, err := q.GetPasswordCredentialByEmail(ctx, normalizeEmail(email))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, invalidCredentials()
		}
		return Session{}, err
	}
	valid, err := verifyPassword(password, credential.PasswordHash)
	if err != nil || !valid {
		return Session{}, invalidCredentials()
	}
	if credential.EmailVerifiedAt == nil {
		return Session{}, authError("email_not_verified", "verify your email before signing in")
	}
	return s.issueSession(ctx, User{
		ID: credential.ID, Email: credential.Email, EmailVerified: true,
	})
}

func (s *Service) Refresh(ctx context.Context, rawToken string) (Session, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := storedb.New(tx)
	record, err := q.GetRefreshTokenForUpdate(ctx, tokenHash(rawToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, invalidRefreshToken()
		}
		return Session{}, err
	}
	now := s.now().UTC()
	if record.UsedAt != nil || record.RevokedAt != nil {
		if err := q.RevokeRefreshFamily(ctx, record.FamilyID); err != nil {
			return Session{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Session{}, err
		}
		return Session{}, authError("refresh_token_reused", "refresh token reuse detected; sign in again")
	}
	if !record.ExpiresAt.After(now) {
		return Session{}, invalidRefreshToken()
	}
	if err := q.MarkRefreshTokenUsed(ctx, record.ID); err != nil {
		return Session{}, err
	}
	userRow, err := q.GetUserByID(ctx, record.UserID)
	if err != nil {
		return Session{}, err
	}
	user := userFromDB(userRow)
	session, err := s.issueSessionWithQueries(ctx, q, user, record.FamilyID)
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *Service) Logout(ctx context.Context, rawToken string) error {
	return storedb.New(s.pool).RevokeRefreshToken(ctx, tokenHash(rawToken))
}

func (s *Service) ForgotPassword(ctx context.Context, email string) error {
	user, err := storedb.New(s.pool).GetUserByEmail(ctx, normalizeEmail(email))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rawToken, hash, err := randomToken()
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := storedb.New(tx)
	if err := q.InvalidateUserOneTimeTokens(ctx, storedb.InvalidateUserOneTimeTokensParams{
		UserID: user.ID, Kind: "reset_password",
	}); err != nil {
		return err
	}
	if err := q.CreateOneTimeToken(ctx, storedb.CreateOneTimeTokenParams{
		ID: uuid.New(), UserID: user.ID, Kind: "reset_password", TokenHash: hash,
		ExpiresAt: s.now().UTC().Add(passwordResetTTL),
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.mailer.SendPasswordReset(ctx, user.Email, rawToken)
}

func (s *Service) ResetPassword(ctx context.Context, rawToken, newPassword string) (Session, error) {
	if err := validatePassword(newPassword); err != nil {
		return Session{}, authError("invalid_password", err.Error())
	}
	hash, err := hashPassword(newPassword)
	if err != nil {
		return Session{}, err
	}
	return s.consumeOneTimeToken(ctx, rawToken, "reset_password", func(ctx context.Context, q *storedb.Queries, token storedb.OneTimeToken) error {
		if err := q.UpsertPasswordCredential(ctx, storedb.UpsertPasswordCredentialParams{
			UserID: token.UserID, PasswordHash: hash,
		}); err != nil {
			return err
		}
		return q.RevokeAllUserRefreshTokens(ctx, token.UserID)
	})
}

func (s *Service) FederatedLogin(ctx context.Context, provider string, input FederatedInput) (Session, error) {
	identity, err := s.federated.Verify(ctx, provider, input)
	if err != nil {
		return Session{}, err
	}
	q := storedb.New(s.pool)
	userRow, err := q.GetUserByIdentity(ctx, storedb.GetUserByIdentityParams{
		Provider: provider, Subject: identity.Subject,
	})
	if err == nil {
		return s.issueSession(ctx, userFromDB(userRow))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Session{}, err
	}
	if _, err := q.GetUserByEmail(ctx, identity.Email); err == nil {
		return Session{}, authError("account_link_required", "sign in to the existing account, then link this provider")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Session{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tq := storedb.New(tx)
	userID := uuid.New()
	userRow, err = tq.CreateUser(ctx, storedb.CreateUserParams{ID: userID, Email: identity.Email})
	if err != nil {
		if isUniqueViolation(err) {
			return Session{}, authError("account_link_required", "sign in to the existing account, then link this provider")
		}
		return Session{}, err
	}
	if err := tq.SetUserEmailVerified(ctx, userID); err != nil {
		return Session{}, err
	}
	email := identity.Email
	if err := tq.CreateIdentity(ctx, storedb.CreateIdentityParams{
		ID: uuid.New(), UserID: userID, Provider: provider, Subject: identity.Subject, Email: &email,
	}); err != nil {
		return Session{}, err
	}
	session, err := s.issueSessionWithQueries(ctx, tq, User{
		ID: userID, Email: identity.Email, EmailVerified: true,
	}, uuid.New())
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *Service) LinkFederated(ctx context.Context, userID uuid.UUID, provider string, input FederatedInput) error {
	identity, err := s.federated.Verify(ctx, provider, input)
	if err != nil {
		return err
	}
	q := storedb.New(s.pool)
	if existing, err := q.GetUserByIdentity(ctx, storedb.GetUserByIdentityParams{
		Provider: provider, Subject: identity.Subject,
	}); err == nil {
		if existing.ID == userID {
			return nil
		}
		return authError("identity_in_use", "this identity belongs to another account")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	email := identity.Email
	if err := q.CreateIdentity(ctx, storedb.CreateIdentityParams{
		ID: uuid.New(), UserID: userID, Provider: provider, Subject: identity.Subject, Email: &email,
	}); err != nil {
		if isUniqueViolation(err) {
			return authError("provider_already_linked", "an identity from this provider is already linked")
		}
		return err
	}
	return nil
}

func (s *Service) SetPassword(ctx context.Context, userID uuid.UUID, password string) error {
	if err := validatePassword(password); err != nil {
		return authError("invalid_password", err.Error())
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return storedb.New(s.pool).UpsertPasswordCredential(ctx, storedb.UpsertPasswordCredentialParams{
		UserID: userID, PasswordHash: hash,
	})
}

func (s *Service) User(ctx context.Context, userID uuid.UUID) (User, error) {
	row, err := storedb.New(s.pool).GetUserByID(ctx, userID)
	if err != nil {
		return User{}, err
	}
	return userFromDB(row), nil
}

func (s *Service) consumeOneTimeToken(
	ctx context.Context,
	rawToken, kind string,
	apply func(context.Context, *storedb.Queries, storedb.OneTimeToken) error,
) (Session, error) {
	if rawToken == "" {
		return Session{}, authError("invalid_token", "token is invalid or expired")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := storedb.New(tx)
	record, err := q.GetOneTimeTokenForUpdate(ctx, storedb.GetOneTimeTokenForUpdateParams{
		TokenHash: tokenHash(rawToken), Kind: kind,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, authError("invalid_token", "token is invalid or expired")
		}
		return Session{}, err
	}
	if record.UsedAt != nil || !record.ExpiresAt.After(s.now().UTC()) {
		return Session{}, authError("invalid_token", "token is invalid or expired")
	}
	if err := apply(ctx, q, record); err != nil {
		return Session{}, err
	}
	if err := q.MarkOneTimeTokenUsed(ctx, record.ID); err != nil {
		return Session{}, err
	}
	userRow, err := q.GetUserByID(ctx, record.UserID)
	if err != nil {
		return Session{}, err
	}
	user := userFromDB(userRow)
	if kind == "verify_email" {
		user.EmailVerified = true
	}
	session, err := s.issueSessionWithQueries(ctx, q, user, uuid.New())
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *Service) issueSession(ctx context.Context, user User) (Session, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	session, err := s.issueSessionWithQueries(ctx, storedb.New(tx), user, uuid.New())
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *Service) issueSessionWithQueries(
	ctx context.Context,
	q *storedb.Queries,
	user User,
	familyID uuid.UUID,
) (Session, error) {
	access, _, err := s.tokens.IssueAccessToken(user.ID, user.EmailVerified)
	if err != nil {
		return Session{}, err
	}
	refresh, hash, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	if err := q.CreateRefreshToken(ctx, storedb.CreateRefreshTokenParams{
		ID: uuid.New(), FamilyID: familyID, UserID: user.ID, TokenHash: hash,
		ExpiresAt: s.now().UTC().Add(s.config.RefreshTokenTTL),
	}); err != nil {
		return Session{}, err
	}
	return Session{
		AccessToken: access, RefreshToken: refresh, TokenType: "Bearer",
		ExpiresIn: int64(s.config.AccessTokenTTL.Seconds()), User: user,
	}, nil
}

func userFromDB(row storedb.User) User {
	return User{ID: row.ID, Email: row.Email, EmailVerified: row.EmailVerifiedAt != nil}
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validEmail(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value && len(value) <= 254
}

type Error struct {
	Code    string
	Message string
}

func (e Error) Error() string { return e.Message }

func authError(code, message string) error {
	return Error{Code: code, Message: message}
}

func invalidCredentials() error {
	return authError("invalid_credentials", "email or password is incorrect")
}

func invalidRefreshToken() error {
	return authError("invalid_refresh_token", "refresh token is invalid or expired")
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
