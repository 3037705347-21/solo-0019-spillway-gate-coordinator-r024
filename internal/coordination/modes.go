package coordination

import (
	"context"
	"strings"

	"example.com/spillway-gate-coordinator/internal/contracts"
	"example.com/spillway-gate-coordinator/internal/domain"
)

// SwitchGateMode changes the control mode of a gate. Switching to local hands
// the gate to on-site manual control; switching back to central requires the
// dispatcher to reconfirm the real field conditions measured at switch time.
// The repository applies the full transition atomically under its lock, so a
// switch racing with a review or an execution has exactly one winner.
func (s *Service) SwitchGateMode(
	ctx context.Context,
	gateID string,
	request contracts.SwitchGateModeRequest,
) (domain.Gate, error) {
	mode := domain.ControlMode(strings.ToLower(strings.TrimSpace(request.Mode)))
	if mode != domain.ControlLocal && mode != domain.ControlCentral {
		return domain.Gate{}, domain.Invalid("invalid_control_mode", "mode must be local or central")
	}

	if mode == domain.ControlLocal {
		return s.repository.SwitchGateToLocal(
			ctx,
			gateID,
			request.Operator,
			request.Reason,
			s.now(),
		)
	}

	return s.repository.ReturnGateToCentral(
		ctx,
		gateID,
		request.Operator,
		request.Reason,
		request.ConfirmedAperture,
		request.ConditionsConfirmed,
		s.now(),
	)
}

func (s *Service) RevokeCommand(
	ctx context.Context,
	commandID string,
	request contracts.RevokeCommandRequest,
) (domain.GateCommand, domain.Gate, error) {
	return s.repository.RevokeCommand(
		ctx,
		commandID,
		request.Operator,
		request.Reason,
		s.now(),
	)
}
