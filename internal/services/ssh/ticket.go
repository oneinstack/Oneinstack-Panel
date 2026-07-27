package ssh

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

var (
	ErrInvalidTicket = errors.New("invalid terminal ticket")
	ErrExpiredTicket = errors.New("expired terminal ticket")
	DefaultTickets   = NewTicketStore(30 * time.Second)
)

type TicketClaims struct {
	UserID   int64
	Username string
	ClientIP string
}

type ticketRecord struct {
	claims    TicketClaims
	expiresAt time.Time
}

type TicketStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[[sha256.Size]byte]ticketRecord
	now     func() time.Time
}

func NewTicketStore(ttl time.Duration) *TicketStore {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &TicketStore{
		ttl:     ttl,
		entries: make(map[[sha256.Size]byte]ticketRecord),
		now:     time.Now,
	}
}

func (store *TicketStore) Issue(claims TicketClaims) (string, time.Time, error) {
	if store == nil || claims.UserID <= 0 || claims.Username == "" || claims.ClientIP == "" {
		return "", time.Time{}, ErrInvalidTicket
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	key := sha256.Sum256([]byte(token))

	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now()
	store.removeExpiredLocked(now)
	expiresAt := now.Add(store.ttl)
	store.entries[key] = ticketRecord{claims: claims, expiresAt: expiresAt}
	return token, expiresAt, nil
}

func (store *TicketStore) Consume(token, clientIP string) (TicketClaims, error) {
	if store == nil || token == "" || clientIP == "" {
		return TicketClaims{}, ErrInvalidTicket
	}
	key := sha256.Sum256([]byte(token))

	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now()
	record, exists := store.entries[key]
	if !exists {
		return TicketClaims{}, ErrInvalidTicket
	}
	if !record.expiresAt.After(now) {
		delete(store.entries, key)
		return TicketClaims{}, ErrExpiredTicket
	}
	if record.claims.ClientIP != clientIP {
		return TicketClaims{}, ErrInvalidTicket
	}
	delete(store.entries, key)
	return record.claims, nil
}

func (store *TicketStore) removeExpiredLocked(now time.Time) {
	for key, record := range store.entries {
		if !record.expiresAt.After(now) {
			delete(store.entries, key)
		}
	}
}
