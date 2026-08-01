package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	journalName = "events.jsonl"
	lockName    = ".lock"
)

var ErrSessionLocked = errors.New("session is already open")

type CreateOptions struct {
	Home            string
	Workspace       string
	Model           string
	ReasoningEffort string
}

type Session struct {
	mu           sync.Mutex
	id           string
	home         string
	dir          string
	journalPath  string
	journal      *os.File
	lock         *os.File
	seq          uint64
	replay       State
	replayValid  bool
	hasUserTurn  bool
	tailRepaired bool
	writeErr     error
	closed       bool
}

func DefaultHome() (string, error) {
	if value := strings.TrimSpace(os.Getenv("CY_HOME")); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".cy"), nil
}

func ResolveHome(home string) (string, error) {
	return resolveHome(home)
}

func Create(opts CreateOptions) (*Session, error) {
	home, err := resolveHome(opts.Home)
	if err != nil {
		return nil, err
	}
	if err := ensurePrivateDir(home); err != nil {
		return nil, err
	}
	sessionsDir := filepath.Join(home, "sessions")
	if err := ensurePrivateDir(sessionsDir); err != nil {
		return nil, err
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(sessionsDir, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	s, err := openAt(home, id, dir, true)
	if err != nil {
		return nil, errors.Join(err, removeCreatedSession(dir))
	}
	if _, err := s.Append(RecordSessionStarted, SessionStarted{
		Workspace:       opts.Workspace,
		Model:           opts.Model,
		ReasoningEffort: opts.ReasoningEffort,
	}); err != nil {
		_ = s.Close()
		return nil, errors.Join(err, removeCreatedSession(dir))
	}
	return s, nil
}

func removeCreatedSession(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove incomplete session directory: %w", err)
	}
	return nil
}

func Open(home, idOrPrefix string) (*Session, error) {
	home, err := resolveHome(home)
	if err != nil {
		return nil, err
	}
	id, dir, err := resolveSession(home, idOrPrefix)
	if err != nil {
		return nil, err
	}
	s, err := openAt(home, id, dir, false)
	if err != nil {
		return nil, err
	}
	if err := s.Reconcile(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func openAt(home, id, dir string, createJournal bool) (*Session, error) {
	if err := ensurePrivateDir(dir); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open session lock: %w", err)
	}
	if err := os.Chmod(filepath.Join(dir, lockName), 0o600); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("set session lock mode: %w", err)
	}
	if err := acquireLock(lock); err != nil {
		_ = lock.Close()
		if errors.Is(err, errWouldBlock) {
			return nil, fmt.Errorf("%w: %s", ErrSessionLocked, id)
		}
		return nil, fmt.Errorf("lock session %s: %w", id, err)
	}

	flags := os.O_RDWR | os.O_APPEND
	if createJournal {
		flags |= os.O_CREATE | os.O_EXCL
	}
	journalPath := filepath.Join(dir, journalName)
	journal, err := os.OpenFile(journalPath, flags, 0o600)
	if err != nil {
		_ = releaseLock(lock)
		_ = lock.Close()
		return nil, fmt.Errorf("open session journal: %w", err)
	}
	if err := os.Chmod(journalPath, 0o600); err != nil {
		_ = journal.Close()
		_ = releaseLock(lock)
		_ = lock.Close()
		return nil, fmt.Errorf("set session journal mode: %w", err)
	}
	tailRepaired := false
	if !createJournal {
		tailRepaired, err = repairJournalTail(journal)
		if err != nil {
			_ = journal.Close()
			_ = releaseLock(lock)
			_ = lock.Close()
			return nil, err
		}
	}
	s := &Session{id: id, home: home, dir: dir, journalPath: journalPath, journal: journal, lock: lock, tailRepaired: tailRepaired}
	if !createJournal {
		state, err := s.Replay()
		if err != nil {
			_ = s.Close()
			return nil, err
		}
		s.seq = state.LastSeq
	}
	return s, nil
}

func (s *Session) ID() string { return s.id }

func (s *Session) TailRepaired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tailRepaired
}

// HasUserTurn reports whether the session contains a submitted prompt.
func (s *Session) HasUserTurn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasUserTurn
}

func (s *Session) Append(recordType RecordType, payload any) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return Record{}, errors.New("session is closed")
	}
	if s.writeErr != nil {
		return Record{}, fmt.Errorf("session journal is unavailable after a previous write failure: %w", s.writeErr)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return Record{}, fmt.Errorf("marshal %s payload: %w", recordType, err)
	}
	record := Record{
		Seq:       s.seq + 1,
		Timestamp: time.Now().UTC(),
		Type:      recordType,
		Payload:   payloadJSON,
	}
	line, err := json.Marshal(record)
	if err != nil {
		return Record{}, fmt.Errorf("marshal journal record: %w", err)
	}
	line = append(line, '\n')
	if n, err := s.journal.Write(line); err != nil || n != len(line) {
		if err == nil {
			err = io.ErrShortWrite
		}
		s.writeErr = fmt.Errorf("append session journal: %w", err)
		return Record{}, s.writeErr
	}
	s.seq = record.Seq
	if s.replayValid {
		if err := applyRecord(&s.replay, record); err != nil {
			s.replayValid = false
		}
	}
	if recordType == RecordUserMessage {
		s.hasUserTurn = true
	}
	return record, nil
}

func (s *Session) Records() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recordsLocked()
}

func (s *Session) recordsLocked() ([]Record, error) {
	file, err := os.Open(s.journalPath)
	if err != nil {
		return nil, fmt.Errorf("open journal for replay: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	var records []Record
	var wantSeq uint64 = 1
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			completeLine := line[len(line)-1] == '\n'
			trimmed := strings.TrimSpace(string(line))
			if trimmed != "" {
				var record Record
				if err := json.Unmarshal([]byte(trimmed), &record); err != nil {
					if errors.Is(readErr, io.EOF) && !completeLine {
						break
					}
					return nil, fmt.Errorf("decode journal record %d: %w", wantSeq, err)
				}
				if record.Seq != wantSeq {
					return nil, fmt.Errorf("journal sequence is %d, want %d", record.Seq, wantSeq)
				}
				if strings.TrimSpace(string(record.Type)) == "" {
					return nil, fmt.Errorf("journal record %d has an empty type", record.Seq)
				}
				records = append(records, record)
				wantSeq++
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read session journal: %w", readErr)
		}
	}
	return records, nil
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeLocked()
}

func (s *Session) closeLocked() error {
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	if s.journal != nil {
		errs = append(errs, s.journal.Close())
	}
	if s.lock != nil {
		errs = append(errs, releaseLock(s.lock), s.lock.Close())
	}
	return errors.Join(errs...)
}

// ClosePruningEmpty closes the session and removes it when no user turn was
// ever recorded. Authentication and saved defaults live outside the session,
// so an abandoned initial screen has no durable state worth listing.
func (s *Session) ClosePruningEmpty() error {
	s.mu.Lock()
	empty := !s.hasUserTurn
	closeErr := s.closeLocked()
	s.mu.Unlock()
	if !empty || closeErr != nil {
		return closeErr
	}
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("remove empty session %s: %w", s.id, err)
	}
	return nil
}

func resolveHome(home string) (string, error) {
	if strings.TrimSpace(home) == "" {
		return DefaultHome()
	}
	return filepath.Abs(home)
}

func resolveSession(home, idOrPrefix string) (string, string, error) {
	prefix := strings.TrimSpace(idOrPrefix)
	if prefix == "" || strings.ContainsAny(prefix, `/\\`) {
		return "", "", errors.New("session id or prefix is required")
	}
	sessionsDir := filepath.Join(home, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return "", "", fmt.Errorf("read sessions: %w", err)
	}
	var matches []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			matches = append(matches, entry.Name())
		}
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("session %q not found", prefix)
	}
	if len(matches) > 1 {
		return "", "", fmt.Errorf("session prefix %q is ambiguous", prefix)
	}
	return matches[0], filepath.Join(sessionsDir, matches[0]), nil
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private directory %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("set private directory mode %s: %w", path, err)
	}
	return nil
}

func repairJournalTail(file *os.File) (bool, error) {
	info, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("stat session journal: %w", err)
	}
	if info.Size() == 0 {
		return false, nil
	}
	last := []byte{0}
	if _, err := file.ReadAt(last, info.Size()-1); err != nil {
		return false, fmt.Errorf("read session journal tail: %w", err)
	}
	if last[0] == '\n' {
		return false, nil
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return false, fmt.Errorf("read session journal: %w", err)
	}
	lastNewline := strings.LastIndexByte(string(data), '\n')
	if err := file.Truncate(int64(lastNewline + 1)); err != nil {
		return false, fmt.Errorf("discard partial session journal tail: %w", err)
	}
	return true, nil
}

func newID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return id.String(), nil
}
