// Package stock holds the pure stock-availability math (Feature Spec §4.2),
// deliberately free of GORM/DB so it is the single, exhaustively unit-testable
// source of truth used by the repository transactions.
package stock

import (
	"time"

	"github.com/adamkekesi/microservice-demo/inventory/internal/model"
)

// Active reports whether a reservation counts against availability: it must be
// PENDING and not yet expired (lazy expiry — an expired PENDING row is simply
// excluded, no background job required).
//
// NOTE: the hot path sums active reservations in SQL (repository.sumActive:
// "status = PENDING AND expires_at > now"). Keep that predicate in sync with this
// function — they are two encodings of the same rule.
func Active(r model.Reservation, now time.Time) bool {
	return r.Status == model.StatusPending && r.ExpiresAt.After(now)
}

// Reserved sums the quantities of the active reservations in rs.
func Reserved(rs []model.Reservation, now time.Time) int {
	total := 0
	for _, r := range rs {
		if Active(r, now) {
			total += r.Quantity
		}
	}
	return total
}

// Available returns on_hand minus the active reserved quantity.
func Available(onHand int, rs []model.Reservation, now time.Time) int {
	return onHand - Reserved(rs, now)
}
