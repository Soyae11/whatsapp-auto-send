package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"wa-shared/id"
)

const (
	SecretBytes = 20

	SecretBodyLen = 32

	HintLen = 4

	DefaultRateLimit = 600

	IDPrefix = "wak"
)

var (
	ErrNoCredentials = errors.New("no bearer credentials")

	ErrMalformedSecret = errors.New("malformed api key")
)

type Environment string

const (
	EnvLive Environment = "live"
	EnvTest Environment = "test"
)

var Environments = []Environment{EnvLive, EnvTest}

func (e Environment) Prefix() string { return "wsk_" + string(e) + "_" }

func ParseEnvironment(s string) (Environment, error) {
	for _, e := range Environments {
		if string(e) == s {
			return e, nil
		}
	}
	return "", fmt.Errorf("unknown environment %q (want live or test)", s)
}

type Secret struct {
	Plaintext string
	Hash      string
	Hint      string
}

func Generate(env Environment) (Secret, error) {
	if _, err := ParseEnvironment(string(env)); err != nil {
		return Secret{}, err
	}
	plaintext := id.Random(env.Prefix(), SecretBytes)
	return Secret{
		Plaintext: plaintext,
		Hash:      Hash(plaintext),
		Hint:      plaintext[len(plaintext)-HintLen:],
	}, nil
}

func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func SecretEnvironment(plaintext string) (Environment, error) {
	for _, e := range Environments {
		body, ok := strings.CutPrefix(plaintext, e.Prefix())
		if !ok {
			continue
		}
		switch {
		case len(body) != SecretBodyLen:
			return "", fmt.Errorf("%w: body is %d characters, want %d", ErrMalformedSecret, len(body), SecretBodyLen)
		case !id.Valid(body):
			return "", fmt.Errorf("%w: body has characters outside the key alphabet", ErrMalformedSecret)
		}
		return e, nil
	}
	return "", fmt.Errorf("%w: want a %s or %s prefix", ErrMalformedSecret, EnvLive.Prefix(), EnvTest.Prefix())
}

func Bearer(header string) (string, error) {
	scheme, credentials, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return "", ErrNoCredentials
	}
	credentials = strings.TrimSpace(credentials)
	if credentials == "" {
		return "", ErrNoCredentials
	}
	return credentials, nil
}

type Key struct {
	ID          string
	Name        string
	Project     string
	Environment Environment
	Hint        string
	Senders     []string
	RateLimit   int
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
	ExpiresAt   *time.Time
	CreatedAt   time.Time
}

func (k Key) MaySend(sender string) bool {
	return slices.Contains(k.Senders, sender)
}

func (k Key) Active(now time.Time) bool {
	if k.RevokedAt != nil {
		return false
	}
	return k.ExpiresAt == nil || k.ExpiresAt.After(now)
}

func (k Key) Display() string {
	return k.Environment.Prefix() + strings.Repeat(".", 6) + k.Hint
}

func NewID() string { return id.New(IDPrefix) }
