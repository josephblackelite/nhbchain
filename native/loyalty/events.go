package loyalty

import "nhbchain/core/events"

func newProgramPausedEvent(program *Program, caller [20]byte) events.LoyaltyProgramPaused {
	if program == nil {
		return events.LoyaltyProgramPaused{Caller: caller}
	}
	return events.LoyaltyProgramPaused{
		ID:     program.ID,
		Owner:  program.Owner,
		Caller: caller,
	}
}

func newProgramResumedEvent(program *Program, caller [20]byte) events.LoyaltyProgramResumed {
	if program == nil {
		return events.LoyaltyProgramResumed{Caller: caller}
	}
	return events.LoyaltyProgramResumed{
		ID:     program.ID,
		Owner:  program.Owner,
		Caller: caller,
	}
}

func newPaymasterRotatedEvent(business *Business, caller, oldPaymaster, newPaymaster [20]byte) events.LoyaltyPaymasterRotated {
	if business == nil {
		return events.LoyaltyPaymasterRotated{Caller: caller, OldPaymaster: oldPaymaster, NewPaymaster: newPaymaster}
	}
	return events.LoyaltyPaymasterRotated{
		BusinessID:   business.ID,
		Owner:        business.Owner,
		Caller:       caller,
		OldPaymaster: oldPaymaster,
		NewPaymaster: newPaymaster,
	}
}
