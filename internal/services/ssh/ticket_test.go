package ssh

import (
	"errors"
	"testing"
	"time"
)

func TestTerminalTicketIsBoundAndSingleUse(t *testing.T) {
	store := NewTicketStore(30 * time.Second)
	issued := TicketClaims{UserID: 1, Username: "admin", ClientIP: "192.0.2.10"}
	ticket, expiresAt, err := store.Issue(issued)
	if err != nil {
		t.Fatal(err)
	}
	if ticket == "" || !expiresAt.After(time.Now()) {
		t.Fatalf("invalid ticket response: ticket=%q expiresAt=%v", ticket, expiresAt)
	}

	if _, err := store.Consume(ticket, "192.0.2.11"); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("different IP consume error = %v", err)
	}
	claims, err := store.Consume(ticket, issued.ClientIP)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if claims != issued {
		t.Fatalf("claims = %+v, want %+v", claims, issued)
	}
	if _, err := store.Consume(ticket, issued.ClientIP); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("reused ticket error = %v", err)
	}
}

func TestTerminalTicketExpires(t *testing.T) {
	store := NewTicketStore(time.Second)
	now := time.Now()
	store.now = func() time.Time { return now }
	ticket, _, err := store.Issue(TicketClaims{UserID: 1, Username: "admin", ClientIP: "192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(2 * time.Second) }
	if _, err := store.Consume(ticket, "192.0.2.10"); !errors.Is(err, ErrExpiredTicket) {
		t.Fatalf("expired ticket error = %v", err)
	}
}

func TestTerminalTicketRejectsIncompleteClaims(t *testing.T) {
	store := NewTicketStore(time.Second)
	if _, _, err := store.Issue(TicketClaims{}); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("Issue() error = %v", err)
	}
}
