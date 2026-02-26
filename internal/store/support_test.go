package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupportStore_SubmitTicket(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	ticket := entity.SupportTicketInsert{
		Topic:          "Order Issue",
		Subject:        "Problem with my order",
		Civility:       "Mr",
		Email:          "test@example.com",
		FirstName:      "John",
		LastName:       "Doe",
		OrderReference: "ORD-123",
		Notes:          "I haven't received my order yet",
		Category:       "shipping",
		Priority:       entity.PriorityHigh,
	}

	caseNumber, err := store.Support().SubmitTicket(ctx, ticket)
	require.NoError(t, err)
	assert.NotEmpty(t, caseNumber)
	assert.Contains(t, caseNumber, "CS-")

	retrieved, err := store.Support().GetSupportTicketByCaseNumber(ctx, caseNumber)
	require.NoError(t, err)
	assert.Equal(t, ticket.Email, retrieved.Email)
	assert.Equal(t, ticket.Subject, retrieved.Subject)
	assert.Equal(t, entity.SupportStatusSubmitted, retrieved.Status)
	assert.Equal(t, entity.PriorityHigh, retrieved.Priority)
}

func TestSupportStore_CaseNumberGeneration(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	ticket := entity.SupportTicketInsert{
		Topic:     "Test",
		Subject:   "Test ticket",
		Civility:  "Mr",
		Email:     "test@example.com",
		FirstName: "John",
		LastName:  "Doe",
		Notes:     "Test notes",
		Priority:  entity.PriorityMedium,
	}

	caseNumber1, err := store.Support().SubmitTicket(ctx, ticket)
	require.NoError(t, err)

	caseNumber2, err := store.Support().SubmitTicket(ctx, ticket)
	require.NoError(t, err)

	assert.NotEqual(t, caseNumber1, caseNumber2)
	
	year := time.Now().Year()
	assert.Contains(t, caseNumber1, "CS-")
	assert.Contains(t, caseNumber1, string(rune(year)))
}

func TestSupportStore_GetSupportTicketById(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	ticket := entity.SupportTicketInsert{
		Topic:     "Test",
		Subject:   "Test ticket",
		Civility:  "Ms",
		Email:     "jane@example.com",
		FirstName: "Jane",
		LastName:  "Smith",
		Notes:     "Test notes",
		Priority:  entity.PriorityLow,
	}

	caseNumber, err := store.Support().SubmitTicket(ctx, ticket)
	require.NoError(t, err)

	retrieved, err := store.Support().GetSupportTicketByCaseNumber(ctx, caseNumber)
	require.NoError(t, err)

	byId, err := store.Support().GetSupportTicketById(ctx, retrieved.Id)
	require.NoError(t, err)
	assert.Equal(t, retrieved.Id, byId.Id)
	assert.Equal(t, retrieved.Email, byId.Email)
}

func TestSupportStore_UpdateStatus(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	ticket := entity.SupportTicketInsert{
		Topic:     "Test",
		Subject:   "Test ticket",
		Civility:  "Mr",
		Email:     "test@example.com",
		FirstName: "John",
		LastName:  "Doe",
		Notes:     "Test notes",
		Priority:  entity.PriorityMedium,
	}

	caseNumber, err := store.Support().SubmitTicket(ctx, ticket)
	require.NoError(t, err)

	retrieved, err := store.Support().GetSupportTicketByCaseNumber(ctx, caseNumber)
	require.NoError(t, err)
	assert.Equal(t, entity.SupportStatusSubmitted, retrieved.Status)

	err = store.Support().UpdateStatus(ctx, retrieved.Id, entity.SupportStatusInProgress)
	require.NoError(t, err)

	updated, err := store.Support().GetSupportTicketById(ctx, retrieved.Id)
	require.NoError(t, err)
	assert.Equal(t, entity.SupportStatusInProgress, updated.Status)
	assert.False(t, updated.ResolvedAt.Valid)

	err = store.Support().UpdateStatus(ctx, retrieved.Id, entity.SupportStatusResolved)
	require.NoError(t, err)

	resolved, err := store.Support().GetSupportTicketById(ctx, retrieved.Id)
	require.NoError(t, err)
	assert.Equal(t, entity.SupportStatusResolved, resolved.Status)
	assert.True(t, resolved.ResolvedAt.Valid)
}

func TestSupportStore_UpdatePriority(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	ticket := entity.SupportTicketInsert{
		Topic:     "Test",
		Subject:   "Test ticket",
		Civility:  "Mr",
		Email:     "test@example.com",
		FirstName: "John",
		LastName:  "Doe",
		Notes:     "Test notes",
		Priority:  entity.PriorityLow,
	}

	caseNumber, err := store.Support().SubmitTicket(ctx, ticket)
	require.NoError(t, err)

	retrieved, err := store.Support().GetSupportTicketByCaseNumber(ctx, caseNumber)
	require.NoError(t, err)

	err = store.Support().UpdatePriority(ctx, retrieved.Id, entity.PriorityUrgent)
	require.NoError(t, err)

	updated, err := store.Support().GetSupportTicketById(ctx, retrieved.Id)
	require.NoError(t, err)
	assert.Equal(t, entity.PriorityUrgent, updated.Priority)
}

func TestSupportStore_UpdateCategory(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	ticket := entity.SupportTicketInsert{
		Topic:     "Test",
		Subject:   "Test ticket",
		Civility:  "Mr",
		Email:     "test@example.com",
		FirstName: "John",
		LastName:  "Doe",
		Notes:     "Test notes",
		Category:  "general",
		Priority:  entity.PriorityMedium,
	}

	caseNumber, err := store.Support().SubmitTicket(ctx, ticket)
	require.NoError(t, err)

	retrieved, err := store.Support().GetSupportTicketByCaseNumber(ctx, caseNumber)
	require.NoError(t, err)

	err = store.Support().UpdateCategory(ctx, retrieved.Id, "technical")
	require.NoError(t, err)

	updated, err := store.Support().GetSupportTicketById(ctx, retrieved.Id)
	require.NoError(t, err)
	assert.Equal(t, "technical", updated.Category)
}

func TestSupportStore_UpdateInternalNotes(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	ticket := entity.SupportTicketInsert{
		Topic:     "Test",
		Subject:   "Test ticket",
		Civility:  "Mr",
		Email:     "test@example.com",
		FirstName: "John",
		LastName:  "Doe",
		Notes:     "Test notes",
		Priority:  entity.PriorityMedium,
	}

	caseNumber, err := store.Support().SubmitTicket(ctx, ticket)
	require.NoError(t, err)

	retrieved, err := store.Support().GetSupportTicketByCaseNumber(ctx, caseNumber)
	require.NoError(t, err)

	internalNotes := "Customer called, will follow up tomorrow"
	err = store.Support().UpdateInternalNotes(ctx, retrieved.Id, internalNotes)
	require.NoError(t, err)

	updated, err := store.Support().GetSupportTicketById(ctx, retrieved.Id)
	require.NoError(t, err)
	assert.Equal(t, internalNotes, updated.InternalNotes)
}

func TestSupportStore_GetSupportTicketsPaged(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	tickets := []entity.SupportTicketInsert{
		{
			Topic:     "Order",
			Subject:   "Order issue 1",
			Civility:  "Mr",
			Email:     "user1@example.com",
			FirstName: "User",
			LastName:  "One",
			Notes:     "Notes 1",
			Category:  "shipping",
			Priority:  entity.PriorityHigh,
		},
		{
			Topic:     "Product",
			Subject:   "Product question",
			Civility:  "Ms",
			Email:     "user2@example.com",
			FirstName: "User",
			LastName:  "Two",
			Notes:     "Notes 2",
			Category:  "product",
			Priority:  entity.PriorityMedium,
		},
		{
			Topic:     "Account",
			Subject:   "Account help",
			Civility:  "Mr",
			Email:     "user3@example.com",
			FirstName: "User",
			LastName:  "Three",
			Notes:     "Notes 3",
			Category:  "account",
			Priority:  entity.PriorityLow,
		},
	}

	for _, ticket := range tickets {
		_, err := store.Support().SubmitTicket(ctx, ticket)
		require.NoError(t, err)
	}

	t.Run("get all tickets", func(t *testing.T) {
		results, total, err := store.Support().GetSupportTicketsPaged(
			ctx, 10, 0, entity.OrderFactorDESC, entity.SupportTicketFilters{},
		)
		require.NoError(t, err)
		assert.Len(t, results, 3)
		assert.Equal(t, 3, total)
	})

	t.Run("pagination", func(t *testing.T) {
		results, total, err := store.Support().GetSupportTicketsPaged(
			ctx, 2, 0, entity.OrderFactorDESC, entity.SupportTicketFilters{},
		)
		require.NoError(t, err)
		assert.Len(t, results, 2)
		assert.Equal(t, 3, total)

		results, total, err = store.Support().GetSupportTicketsPaged(
			ctx, 2, 2, entity.OrderFactorDESC, entity.SupportTicketFilters{},
		)
		require.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, 3, total)
	})

	t.Run("filter by status", func(t *testing.T) {
		status := entity.SupportStatusSubmitted
		results, total, err := store.Support().GetSupportTicketsPaged(
			ctx, 10, 0, entity.OrderFactorDESC, entity.SupportTicketFilters{
				Status: &status,
			},
		)
		require.NoError(t, err)
		assert.Len(t, results, 3)
		assert.Equal(t, 3, total)
	})

	t.Run("filter by email", func(t *testing.T) {
		results, total, err := store.Support().GetSupportTicketsPaged(
			ctx, 10, 0, entity.OrderFactorDESC, entity.SupportTicketFilters{
				Email: "user1@example.com",
			},
		)
		require.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, 1, total)
		assert.Equal(t, "user1@example.com", results[0].Email)
	})

	t.Run("filter by category", func(t *testing.T) {
		results, total, err := store.Support().GetSupportTicketsPaged(
			ctx, 10, 0, entity.OrderFactorDESC, entity.SupportTicketFilters{
				Category: "shipping",
			},
		)
		require.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, 1, total)
		assert.Equal(t, "shipping", results[0].Category)
	})

	t.Run("filter by priority", func(t *testing.T) {
		priority := entity.PriorityHigh
		results, total, err := store.Support().GetSupportTicketsPaged(
			ctx, 10, 0, entity.OrderFactorDESC, entity.SupportTicketFilters{
				Priority: &priority,
			},
		)
		require.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, 1, total)
		assert.Equal(t, entity.PriorityHigh, results[0].Priority)
	})
}
