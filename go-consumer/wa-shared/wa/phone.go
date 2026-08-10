package wa

import (
	"fmt"
	"strings"
)

const (
	MinPhoneDigits = 8
	MaxPhoneDigits = 15

	DefaultCountryCode = "62"
)

func NormalisePhone(input string) (string, error) {
	raw := strings.TrimSpace(input)

	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()

	switch {
	case digits == "":
		return "", fmt.Errorf("%w: %q contains no digits", ErrInvalidPayload, raw)

	case strings.HasPrefix(digits, "00"):
		return "", fmt.Errorf("%w: %q starts with the 00 international prefix; drop it and send the country code alone, e.g. 6287713848500",
			ErrInvalidPayload, raw)

	// A leading zero means the national format, and the only country this API serves
	// nationally is Indonesia. A number that is already international is never
	// reinterpreted — see openapi.yaml PhoneNumber.
	case strings.HasPrefix(digits, "0"):
		digits = DefaultCountryCode + digits[1:]
	}

	if len(digits) < MinPhoneDigits || len(digits) > MaxPhoneDigits {
		return "", fmt.Errorf("%w: %q has %d digits; a full international number has %d–%d",
			ErrInvalidPayload, raw, len(digits), MinPhoneDigits, MaxPhoneDigits)
	}

	return digits, nil
}
