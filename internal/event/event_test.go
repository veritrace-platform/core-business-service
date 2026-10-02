package event_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/veritrace-platform/core-business-service/internal/event"
)

// vectors is testdata/shipment-events.json, a copy of veritrace/docs/contracts/test-vectors/shipment-events.json.
type vectors struct {
	ZeroHash string `json:"zero_hash"`
	Events   []struct {
		Envelope struct {
			event.Event
			Producer string `json:"producer"`
		} `json:"envelope"`
		HashInput string `json:"hash_input"`
		EventHash string `json:"event_hash"`
	} `json:"events"`
}

func chain(t *testing.T) ([]event.Event, vectors) {
	t.Helper()
	b, err := os.ReadFile("testdata/shipment-events.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	events := make([]event.Event, len(v.Events))
	for i, e := range v.Events {
		events[i] = e.Envelope.Event
	}
	return events, v
}

func TestHashMatchesTheSharedVectors(t *testing.T) {
	events, v := chain(t)
	if v.ZeroHash != event.ZeroHash {
		t.Errorf("zero hash = %s, want %s", event.ZeroHash, v.ZeroHash)
	}
	for i, e := range events {
		canonical, err := e.Canonical()
		if err != nil || string(canonical) != v.Events[i].HashInput {
			t.Errorf("event %d: Canonical() = %s, %v\nwant %s", e.Sequence, canonical, err, v.Events[i].HashInput)
		}
		if hash, err := e.Hash(); err != nil || hash != v.Events[i].EventHash || hash != e.EventHash {
			t.Errorf("event %d: Hash() = %s, %v; want %s", e.Sequence, hash, err, v.Events[i].EventHash)
		}
		if v.Events[i].Envelope.Producer != event.Producer {
			t.Errorf("event %d: producer = %q", e.Sequence, v.Events[i].Envelope.Producer)
		}
	}
}

func TestVerify(t *testing.T) {
	events, _ := chain(t)
	got := event.Verify(events)
	if !got.Valid || got.EventCount != 4 || got.HeadHash == nil || *got.HeadHash != events[3].EventHash || got.FirstInvalidSequence != nil {
		t.Errorf("Verify(intact) = %+v", got)
	}
	if got := event.Verify(nil); !got.Valid || got.EventCount != 0 || got.HeadHash != nil {
		t.Errorf("Verify(empty) = %+v", got)
	}

	tampered := func(change func([]event.Event) []event.Event) []event.Event {
		copied := append([]event.Event(nil), events...)
		return change(copied)
	}
	tests := map[string]struct {
		events []event.Event
		first  int
	}{
		"altered payload": {tampered(func(e []event.Event) []event.Event {
			e[1].Data = json.RawMessage(`{"driver_user_id":"0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d00"}`)
			return e
		}), 2},
		"altered status": {tampered(func(e []event.Event) []event.Event {
			e[2].Subject.Status = "DELIVERED"
			return e
		}), 3},
		"removed event": {tampered(func(e []event.Event) []event.Event { return append(e[:1], e[2:]...) }), 2},
		"reordered events": {tampered(func(e []event.Event) []event.Event {
			e[2], e[3] = e[3], e[2]
			return e
		}), 3},
		"rehashed but unlinked": {tampered(func(e []event.Event) []event.Event {
			e[0].Data = json.RawMessage(`{}`)
			e[0].EventHash, _ = e[0].Hash()
			return e
		}), 2},
	}
	for name, tt := range tests {
		got := event.Verify(tt.events)
		if got.Valid || got.FirstInvalidSequence == nil || *got.FirstInvalidSequence != tt.first {
			t.Errorf("%s: Verify() = %+v, want the chain to break at %d", name, got, tt.first)
		}
	}
}
