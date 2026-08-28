package sync

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSyncRejectsDuplicateMutationIDsBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()
	mutationID := uuid.New()
	service := NewService(nil, 10, 10, 512*1024)
	_, err := service.Sync(context.Background(), uuid.New(), Request{
		Device: Device{ID: uuid.New()},
		Mutations: []Mutation{
			{ID: mutationID, Entity: "session", EntityID: uuid.New(), Operation: "delete"},
			{ID: mutationID, Entity: "solve", EntityID: uuid.New(), Operation: "delete"},
		},
	}, 1)
	var clientErr ClientError
	if !errors.As(err, &clientErr) || clientErr.Code != "duplicate_mutation_id" {
		t.Fatalf("expected duplicate_mutation_id, got %v", err)
	}
}

func TestSessionValidation(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	valid := Session{
		ID: id, Name: "Morning practice", Event: "3x3", Kind: "manual",
		StartedAt: time.Now().UTC(),
	}
	if message := validateSession(valid, id); message != "" {
		t.Fatalf("valid session rejected: %s", message)
	}
	invalid := valid
	invalid.Kind = "server-managed"
	if message := validateSession(invalid, id); message == "" {
		t.Fatal("invalid session kind accepted")
	}
}

func TestSolveValidation(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	valid := Solve{
		ID: id, DurationMS: 12_345, Penalty: "none", SolvedAt: time.Now().UTC(),
		Scramble: "R U R' U'", Event: "3x3",
	}
	if message := validateSolve(valid, id); message != "" {
		t.Fatalf("valid solve rejected: %s", message)
	}
	invalid := valid
	invalid.Penalty = "+2"
	if message := validateSolve(invalid, id); message == "" {
		t.Fatal("invalid penalty accepted")
	}
}

func TestMutationPayloadRejectsMismatchedID(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(Session{
		ID: uuid.New(), Name: "Practice", Event: "3x3", Kind: "manual", StartedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var value Session
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if message := validateSession(value, uuid.New()); message != "data.id must match entity_id" {
		t.Fatalf("unexpected message: %q", message)
	}
}
