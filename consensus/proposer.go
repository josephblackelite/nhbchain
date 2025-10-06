package consensus

const (
	// BPSDenominator represents one hundred percent in basis points.
	BPSDenominator = 10_000
	// DefaultPOSReservationBPS reserves fifteen percent of the proposal window
	// for POS-tagged transactions.
	DefaultPOSReservationBPS = 1_500
)

// POSQuota encapsulates how much of a block should be reserved for the POS lane.
type POSQuota struct {
	ReservationBPS uint32
}

// Normalized returns the configured reservation percentage capped to a valid
// basis-point range. Zero indicates no reservation.
func (q POSQuota) Normalized() uint32 {
	if q.ReservationBPS == 0 {
		return 0
	}
	if q.ReservationBPS > BPSDenominator {
		return BPSDenominator
	}
	return q.ReservationBPS
}

// ReservedSlots computes how many transaction slots should be earmarked for the
// POS lane for a block bounded by maxTxs.
func (q POSQuota) ReservedSlots(maxTxs int) int {
	if maxTxs <= 0 {
		return 0
	}
	reservation := q.Normalized()
	if reservation == 0 {
		return 0
	}
	product := int(reservation) * maxTxs
	slots := product / BPSDenominator
	if product%BPSDenominator != 0 {
		slots++
	}
	if slots > maxTxs {
		return maxTxs
	}
	return slots
}

// WithDefault sets the reservation to the network default when no explicit
// value has been provided.
func (q POSQuota) WithDefault() POSQuota {
	if q.ReservationBPS == 0 {
		q.ReservationBPS = DefaultPOSReservationBPS
	}
	return q
}
