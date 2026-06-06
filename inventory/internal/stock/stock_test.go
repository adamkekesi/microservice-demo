package stock

import (
	"testing"
	"time"

	"github.com/adamkekesi/microservice-demo/inventory/internal/model"
	"github.com/stretchr/testify/require"
)

var (
	now    = time.Now()
	future = now.Add(time.Hour)
	past   = now.Add(-time.Hour)
)

func res(qty int, status model.ReservationStatus, expires time.Time) model.Reservation {
	return model.Reservation{Quantity: qty, Status: status, ExpiresAt: expires}
}

func TestActive(t *testing.T) {
	require.True(t, Active(res(1, model.StatusPending, future), now), "pending + not expired = active")
	require.False(t, Active(res(1, model.StatusPending, past), now), "pending + expired = inactive (lazy expiry)")
	require.False(t, Active(res(1, model.StatusCommitted, future), now), "committed = inactive")
	require.False(t, Active(res(1, model.StatusReleased, future), now), "released = inactive")
}

// Acceptance 5: with on_hand=100, reserve 30 => available=70, on_hand=100.
func TestReserveLeavesOnHandDropsAvailable(t *testing.T) {
	rs := []model.Reservation{res(30, model.StatusPending, future)}
	require.Equal(t, 30, Reserved(rs, now))
	require.Equal(t, 70, Available(100, rs, now))
}

// Acceptance 6: commit that reservation => on_hand=70, available=70.
// After commit the reservation is COMMITTED (inactive) and on_hand dropped by 30.
func TestCommitDropsOnHandKeepsAvailable(t *testing.T) {
	rs := []model.Reservation{res(30, model.StatusCommitted, future)}
	require.Equal(t, 0, Reserved(rs, now))
	require.Equal(t, 70, Available(70, rs, now))
}

// Acceptance 7: reserve 30 then release => available=100, on_hand=100.
func TestReleaseRestoresAvailable(t *testing.T) {
	rs := []model.Reservation{res(30, model.StatusReleased, future)}
	require.Equal(t, 0, Reserved(rs, now))
	require.Equal(t, 100, Available(100, rs, now))
}

// Acceptance 8: reserve 60 against on_hand=100 leaves available=40 (so a second
// reserve of 60 cannot fit).
func TestAvailabilityAfterFirstReserve(t *testing.T) {
	rs := []model.Reservation{res(60, model.StatusPending, future)}
	require.Equal(t, 40, Available(100, rs, now))
}

func TestLazyExpiryExcludesExpiredPending(t *testing.T) {
	rs := []model.Reservation{
		res(30, model.StatusPending, future), // active
		res(50, model.StatusPending, past),   // expired -> excluded
	}
	require.Equal(t, 30, Reserved(rs, now))
	require.Equal(t, 70, Available(100, rs, now))
}

func TestMultipleActiveReservationsSum(t *testing.T) {
	rs := []model.Reservation{
		res(10, model.StatusPending, future),
		res(25, model.StatusPending, future),
		res(5, model.StatusReleased, future), // inactive
	}
	require.Equal(t, 35, Reserved(rs, now))
	require.Equal(t, 65, Available(100, rs, now))
}
