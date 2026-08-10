package senders

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"wa-shared/tasks"
)

const MaxNameLen = 64

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

var ErrUnknown = errors.New("unknown sender")

const DryRunToken = "dry-run"

type Sender struct {
	Name            string
	SessionID       string
	DefaultPriority tasks.Priority
	DryRun          bool
}

type Registry struct {
	byName map[string]Sender
	names  []string
}

func (r *Registry) Get(name string) (Sender, error) {
	s, ok := r.byName[name]
	if !ok {
		return Sender{}, fmt.Errorf("%w %q (known senders: %s)", ErrUnknown, name, strings.Join(r.names, ", "))
	}
	return s, nil
}

func (r *Registry) Has(name string) bool {
	_, ok := r.byName[name]
	return ok
}

func (r *Registry) Names() []string {
	return slices.Clone(r.names)
}

// ForceDryRun marks every sender dry-run-only. This is what makes a whole staging
// environment safe by configuration rather than by every developer remembering a header.
func (r *Registry) ForceDryRun() {
	for name, s := range r.byName {
		s.DryRun = true
		r.byName[name] = s
	}
}

func ValidName(name string) error {
	switch {
	case name == "":
		return errors.New("sender name is empty")
	case len(name) > MaxNameLen:
		return fmt.Errorf("sender name %q is %d characters, over the %d limit", name, len(name), MaxNameLen)
	case !namePattern.MatchString(name):
		return fmt.Errorf("sender name %q must be lowercase letters, digits, and inner dashes, e.g. hr-notifications", name)
	}
	return nil
}

func Parse(raw string, sessions []string) (*Registry, error) {
	r := &Registry{byName: map[string]Sender{}}

	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		s, err := parseEntry(entry, sessions)
		if err != nil {
			return nil, err
		}
		if _, dup := r.byName[s.Name]; dup {
			return nil, fmt.Errorf("sender %q is listed twice", s.Name)
		}
		r.byName[s.Name] = s
		r.names = append(r.names, s.Name)
	}

	if len(r.names) == 0 {
		return nil, errors.New("no senders configured; every consumer request names a sender, so an empty registry can serve nothing")
	}
	slices.Sort(r.names)
	return r, nil
}

func parseEntry(entry string, sessions []string) (Sender, error) {
	fields := strings.Split(entry, ":")
	if len(fields) > 4 {
		return Sender{}, fmt.Errorf("sender %q has %d fields, want name:session[:priority][:dry-run]", entry, len(fields))
	}

	s := Sender{
		Name:            strings.TrimSpace(fields[0]),
		DefaultPriority: tasks.PriorityDefault,
	}
	if err := ValidName(s.Name); err != nil {
		return Sender{}, err
	}

	if len(fields) < 2 {
		return Sender{}, fmt.Errorf("sender %q has no session; want name:session[:priority]", s.Name)
	}
	s.SessionID = strings.TrimSpace(fields[1])
	if s.SessionID == "" {
		return Sender{}, fmt.Errorf("sender %q has an empty session", s.Name)
	}
	if len(sessions) > 0 && !slices.Contains(sessions, s.SessionID) {
		return Sender{}, fmt.Errorf("sender %q maps to session %q, which is not in WA_SESSIONS (%s)",
			s.Name, s.SessionID, strings.Join(sessions, ", "))
	}

	// Everything after the session is an optional modifier, so a sender can be marked
	// dry-run without having to spell out a priority it does not care about.
	for _, field := range fields[2:] {
		field = strings.TrimSpace(field)
		if field == DryRunToken {
			s.DryRun = true
			continue
		}
		p, err := tasks.ParsePriority(field)
		if err != nil {
			return Sender{}, fmt.Errorf("sender %q: %w (or %q)", s.Name, err, DryRunToken)
		}
		s.DefaultPriority = p
	}

	return s, nil
}
