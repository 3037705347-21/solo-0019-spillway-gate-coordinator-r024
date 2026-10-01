package coordination

import (
	"context"
	"math"
	"sort"

	"example.com/spillway-gate-coordinator/internal/contracts"
	"example.com/spillway-gate-coordinator/internal/domain"
)

func (s *Service) RevokeCommand(
	ctx context.Context,
	commandID string,
	request contracts.RevokeCommandRequest,
) (domain.GateCommand, domain.Gate, error) {
	return s.repository.RevokeCommand(
		ctx,
		commandID,
		request.Reason,
		request.RevokedBy,
		s.now(),
	)
}

func (s *Service) SwitchGateToLocal(
	ctx context.Context,
	gateID string,
	request contracts.SwitchGateModeRequest,
) (domain.Gate, []domain.GateCommand, error) {
	gate, revoked, err := s.repository.SwitchGateToLocal(ctx, gateID, request.RequestedBy, s.now())
	if err != nil {
		return domain.Gate{}, nil, err
	}
	// Give revoked authorizations a stable order regardless of repository
	// iteration order.
	sort.Slice(revoked, func(i, j int) bool {
		if revoked[i].CreatedAt.Equal(revoked[j].CreatedAt) {
			return revoked[i].ID < revoked[j].ID
		}
		return revoked[i].CreatedAt.Before(revoked[j].CreatedAt)
	})
	return gate, revoked, nil
}

func (s *Service) SwitchGateToCentral(
	ctx context.Context,
	gateID string,
	request contracts.SwitchGateModeRequest,
) (domain.Gate, error) {
	if request.ConfirmedAperture == nil {
		return domain.Gate{}, domain.Invalid(
			"missing_confirmed_aperture",
			"confirmed_aperture with the real on-site aperture is required before central handback",
		)
	}
	confirmed := *request.ConfirmedAperture
	if math.IsNaN(confirmed) || math.IsInf(confirmed, 0) {
		return domain.Gate{}, domain.Invalid(
			"invalid_confirmed_aperture",
			"confirmed aperture must be a finite number",
		)
	}
	return s.repository.SwitchGateToCentral(
		ctx,
		gateID,
		request.RequestedBy,
		confirmed,
		s.now(),
	)
}
