package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Maciek-Hetman/cubing-sync-backend/internal/auth"
	"github.com/Maciek-Hetman/cubing-sync-backend/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/term"
)

type terminal interface {
	IsTerminal(fd int) bool
	ReadPassword(fd int) ([]byte, error)
}

type systemTerminal struct{}

func (systemTerminal) IsTerminal(fd int) bool {
	return term.IsTerminal(fd)
}

func (systemTerminal) ReadPassword(fd int) ([]byte, error) {
	return term.ReadPassword(fd)
}

func runCreateAdmin(cfg config.Config, args []string, stdin *os.File, stdout, stderr io.Writer) error {
	return createAdmin(cfg, args, stdin, stdout, stderr, systemTerminal{})
}

func createAdmin(cfg config.Config, args []string, stdin *os.File, stdout, stderr io.Writer, tty terminal) error {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "create-admin does not accept arguments; enter credentials when prompted")
		return errors.New("unexpected arguments")
	}
	if stdin == nil || !tty.IsTerminal(int(stdin.Fd())) {
		fmt.Fprintln(stderr, "create-admin requires an interactive terminal")
		return errors.New("interactive terminal required")
	}

	creds, err := promptAdminCredentials(stdin, stdout, tty)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "database is unavailable")
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintln(stderr, "database is unavailable")
		return err
	}

	service := auth.NewService(cfg, pool, nil, nil)
	if _, err := service.CreateAdmin(ctx, creds.email, creds.password); err != nil {
		fmt.Fprintln(stderr, publicCreateAdminError(err))
		return err
	}
	fmt.Fprintln(stdout, "admin account created")
	return nil
}

type adminCredentials struct {
	email    string
	password string
}

func promptAdminCredentials(stdin *os.File, stdout io.Writer, tty terminal) (adminCredentials, error) {
	fmt.Fprint(stdout, "Email: ")
	email, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil {
		return adminCredentials{}, errors.New("email could not be read")
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return adminCredentials{}, errors.New("email is required")
	}

	fmt.Fprint(stdout, "Password: ")
	password, err := readSecret(stdin, tty)
	fmt.Fprintln(stdout)
	if err != nil {
		return adminCredentials{}, errors.New("password could not be read")
	}
	fmt.Fprint(stdout, "Confirm password: ")
	confirm, err := readSecret(stdin, tty)
	fmt.Fprintln(stdout)
	if err != nil {
		return adminCredentials{}, errors.New("password could not be read")
	}
	if password != confirm {
		return adminCredentials{}, errors.New("passwords do not match")
	}
	return adminCredentials{email: email, password: password}, nil
}

func readSecret(stdin *os.File, tty terminal) (string, error) {
	raw, err := tty.ReadPassword(int(stdin.Fd()))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func publicCreateAdminError(err error) string {
	var authErr auth.Error
	if errors.As(err, &authErr) {
		return authErr.Message
	}
	if strings.Contains(strings.ToLower(err.Error()), "request_stats_hourly") ||
		strings.Contains(strings.ToLower(err.Error()), "user_role") {
		return "database schema is not up to date; run migrate first"
	}
	return "admin account could not be created"
}
