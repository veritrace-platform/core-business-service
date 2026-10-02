// Package event keeps the event log of every shipment (shipment-lifecycle.md §9): an append-only list of
// events, each chained to the previous one by its hash (ADR-0006), and queued for the shipment.events topic
// through the outbox in the same transaction (ADR-0005).
package event

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/canonicaljson"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// Type names what happened to a shipment.
type Type string

// Event types of M1.
const (
	TypeCreated            Type = "shipment.created"
	TypeParticipantAdded   Type = "shipment.participant_added"
	TypeDriverAssigned     Type = "shipment.driver_assigned"
	TypePickupConfirmed    Type = "shipment.pickup_confirmed"
	TypeCheckpointRecorded Type = "shipment.checkpoint_recorded"
	TypeDeliveryConfirmed  Type = "shipment.delivery_confirmed"
	TypeCancelled          Type = "shipment.cancelled"
	TypeRecalled           Type = "shipment.recalled"
)

// Message envelope constants (messaging.md §2.1, §3).
const (
	// Version is the payload version of every event type.
	Version = 1
	// Topic receives every shipment event, keyed by SSCC.
	Topic = "shipment.events"
	// Producer names this service in published events.
	Producer = "core-business-service"
	// TimeLayout writes event times in UTC with six fractional digits, so that every producer and verifier
	// hashes the same text.
	TimeLayout = "2006-01-02T15:04:05.000000Z"
)

// ZeroHash is the previous hash of the first event of a shipment: 32 zero bytes in hex.
var ZeroHash = strings.Repeat("0", 64)

// Subject identifies the shipment of an event and its status after the event.
type Subject struct {
	ShipmentID uuid.UUID             `json:"shipment_id"`
	SSCC       string                `json:"sscc"`
	Status     policy.ShipmentStatus `json:"status"`
}

// Actor is the user whose command caused an event.
type Actor struct {
	TenantID uuid.UUID `json:"tenant_id"`
	UserID   uuid.UUID `json:"user_id"`
}

// Event is one entry of a shipment's log, with the members that its hash covers and the hash itself.
type Event struct {
	ID         uuid.UUID `json:"event_id"`
	Type       Type      `json:"event_type"`
	Version    int       `json:"event_version"`
	OccurredAt string    `json:"occurred_at"`
	Subject    Subject   `json:"subject"`
	// Actor is nil for events that the system produces on its own.
	Actor         *Actor          `json:"actor"`
	Sequence      int             `json:"sequence"`
	PrevEventHash string          `json:"prev_event_hash"`
	EventHash     string          `json:"event_hash"`
	Data          json.RawMessage `json:"data"`
}

// hashed holds the members of an event that its hash covers: all of them but the hash.
type hashed struct {
	ID            uuid.UUID       `json:"event_id"`
	Type          Type            `json:"event_type"`
	Version       int             `json:"event_version"`
	OccurredAt    string          `json:"occurred_at"`
	Subject       Subject         `json:"subject"`
	Actor         *Actor          `json:"actor"`
	Sequence      int             `json:"sequence"`
	PrevEventHash string          `json:"prev_event_hash"`
	Data          json.RawMessage `json:"data"`
}

// Canonical returns what the event hash covers: the RFC 8785 canonical JSON of the event without its hash
// (messaging.md §2.1).
func (e Event) Canonical() ([]byte, error) {
	canonical, err := canonicaljson.Marshal(hashed{
		ID: e.ID, Type: e.Type, Version: e.Version, OccurredAt: e.OccurredAt, Subject: e.Subject, Actor: e.Actor,
		Sequence: e.Sequence, PrevEventHash: e.PrevEventHash, Data: e.Data,
	})
	if err != nil {
		return nil, fmt.Errorf("canonicalize event %s: %w", e.ID, err)
	}
	return canonical, nil
}

// Hash returns the SHA-256 of the event's canonical JSON in lowercase hex.
func (e Event) Hash() (string, error) {
	canonical, err := e.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// Integrity is the result of recomputing a shipment's hash chain.
type Integrity struct {
	Valid      bool    `json:"valid"`
	EventCount int     `json:"event_count"`
	HeadHash   *string `json:"head_hash"`
	// FirstInvalidSequence is the first position where the chain breaks: a missing, altered, or misplaced event.
	FirstInvalidSequence *int `json:"first_invalid_sequence"`
}

// Verify recomputes the chain of one shipment's events, given in sequence order: every event must have the
// next sequence number, link to the hash of the event before it, and hash to its stored hash.
func Verify(events []Event) Integrity {
	result := Integrity{Valid: true, EventCount: len(events)}
	prev := ZeroHash
	for i, e := range events {
		position := i + 1
		hash, err := e.Hash()
		if e.Sequence != position || e.PrevEventHash != prev || err != nil || hash != e.EventHash {
			result.Valid, result.FirstInvalidSequence = false, &position
			break
		}
		prev = e.EventHash
	}
	if len(events) > 0 {
		head := events[len(events)-1].EventHash
		result.HeadHash = &head
	}
	return result
}
