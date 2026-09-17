package creator

import (
	"nhbchain/core/events"
	"nhbchain/core/types"
)

const (
	// EventTypeContentPublished is emitted when a creator publishes new content.
	EventTypeContentPublished = "creator.content.published"
	// EventTypeContentTipped is emitted when a fan tips a piece of content.
	EventTypeContentTipped = "creator.content.tipped"
	// EventTypeCreatorStaked is emitted when a fan stakes behind a creator.
	EventTypeCreatorStaked = "creator.fan.staked"
	// EventTypeCreatorUnstaked is emitted when a fan unstakes from a creator.
	EventTypeCreatorUnstaked = "creator.fan.unstaked"
	// EventTypeCreatorPayoutAccrued is emitted when payouts accrue for a creator.
	EventTypeCreatorPayoutAccrued = "creator.payout.accrued"
)

type eventEnvelope struct {
	evt *types.Event
}

func (e eventEnvelope) EventType() string {
	if e.evt == nil {
		return ""
	}
	return e.evt.Type
}

func (e eventEnvelope) Event() *types.Event { return e.evt }

// WrapEvent converts a raw event payload into the emitter-friendly envelope.
func WrapEvent(evt *types.Event) events.Event { return eventEnvelope{evt: evt} }

// ContentPublishedEvent returns the structured event payload for publication announcements.
func ContentPublishedEvent(contentID string, creator string, uri string) *types.Event {
	return &types.Event{
		Type: EventTypeContentPublished,
		Attributes: map[string]string{
			"contentId": contentID,
			"creator":   creator,
			"uri":       uri,
		},
	}
}

// ContentTippedEvent returns the structured event payload for tip activity.
func ContentTippedEvent(contentID string, creator string, fan string, amount string) *types.Event {
	return &types.Event{
		Type: EventTypeContentTipped,
		Attributes: map[string]string{
			"contentId": contentID,
			"creator":   creator,
			"fan":       fan,
			"amount":    amount,
		},
	}
}

// CreatorStakedEvent captures when a fan stakes behind a creator.
func CreatorStakedEvent(creator string, fan string, amount string, shares string) *types.Event {
	return &types.Event{
		Type: EventTypeCreatorStaked,
		Attributes: map[string]string{
			"creator": creator,
			"fan":     fan,
			"amount":  amount,
			"shares":  shares,
		},
	}
}

// CreatorUnstakedEvent captures when a fan withdraws their stake.
func CreatorUnstakedEvent(creator string, fan string, amount string) *types.Event {
	return &types.Event{
		Type: EventTypeCreatorUnstaked,
		Attributes: map[string]string{
			"creator": creator,
			"fan":     fan,
			"amount":  amount,
		},
	}
}

// CreatorPayoutAccruedEvent captures when a creator's pending payout balance changes.
func CreatorPayoutAccruedEvent(creator string, pending string, totalTips string, totalYield string) *types.Event {
	return &types.Event{
		Type: EventTypeCreatorPayoutAccrued,
		Attributes: map[string]string{
			"creator":    creator,
			"pending":    pending,
			"totalTips":  totalTips,
			"totalYield": totalYield,
		},
	}
}
