package domaintrust

import (
	"time"

	"src.solsynth.dev/sosys/stargate/model"
)

// BlockView is the wire shape of SnDomainBlock (snake_case, like the C#
// serializer). MatchedRule reuses it.
type BlockView struct {
	ID                 string      `json:"id"`
	DomainPattern      string      `json:"domain_pattern"`
	Protocol           *string     `json:"protocol"`
	PortRestriction    *int        `json:"port_restriction"`
	Reason             *string     `json:"reason"`
	Priority           int         `json:"priority"`
	TrustLevel         int         `json:"trust_level"`
	IsActive           bool        `json:"is_active"`
	CreatedByAccountID *string     `json:"created_by_account_id"`
	CreatedAt          *model.Time `json:"created_at"`
	UpdatedAt          *model.Time `json:"updated_at"`
	DeletedAt          *model.Time `json:"deleted_at"`
}

func BlockViewFrom(entity *DomainBlockEntity) BlockView {
	view := BlockView{
		ID:              entity.ID.String(),
		DomainPattern:   entity.DomainPattern,
		Protocol:        entity.Protocol,
		PortRestriction: entity.PortRestriction,
		Reason:          entity.Reason,
		Priority:        entity.Priority,
		TrustLevel:      entity.TrustLevel,
		IsActive:        entity.IsActive,
		CreatedAt:       model.NewTime(entity.CreatedAt),
		UpdatedAt:       model.NewTime(entity.UpdatedAt),
	}
	if entity.CreatedByAccountID != nil {
		id := entity.CreatedByAccountID.String()
		view.CreatedByAccountID = &id
	}
	if entity.DeletedAt.Valid {
		view.DeletedAt = model.NewTime(entity.DeletedAt.Time)
	}
	return view
}

// MetricView is DomainValidationMetricResponse (and the metric row the list
// endpoint returns).
type MetricView struct {
	Domain        string      `json:"domain"`
	CheckCount    int64       `json:"check_count"`
	BlockedCount  int64       `json:"blocked_count"`
	VerifiedCount int64       `json:"verified_count"`
	LastCheckedAt *model.Time `json:"last_checked_at"`
}

func MetricViewFrom(entity *DomainValidationMetricEntity) MetricView {
	return MetricView{
		Domain:        entity.Domain,
		CheckCount:    entity.CheckCount,
		BlockedCount:  entity.BlockedCount,
		VerifiedCount: entity.VerifiedCount,
		LastCheckedAt: timePtr(entity.LastCheckedAt),
	}
}

func timePtr(value *time.Time) *model.Time {
	if value == nil {
		return nil
	}
	return model.NewTime(*value)
}
