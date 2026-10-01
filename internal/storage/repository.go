package storage

import (
	"context"
	"time"

	"example.com/spillway-gate-coordinator/internal/domain"
)

type Repository interface {
	CreateGate(ctx context.Context, gate domain.Gate) error
	Gate(ctx context.Context, id string) (domain.Gate, error)
	CreateCommand(ctx context.Context, command domain.GateCommand) error
	Command(ctx context.Context, id string) (domain.GateCommand, error)
	ReviewCommand(
		ctx context.Context,
		commandID string,
		decision domain.InterlockDecision,
		token string,
		now time.Time,
	) (domain.GateCommand, domain.Gate, error)
	ExecuteCommand(
		ctx context.Context,
		commandID string,
		token string,
		operator string,
		simulateFault bool,
		now time.Time,
	) (domain.GateCommand, domain.Gate, error)
	RevokeCommand(
		ctx context.Context,
		commandID string,
		operator string,
		reason string,
		now time.Time,
	) (domain.GateCommand, domain.Gate, error)
	SwitchGateToLocal(
		ctx context.Context,
		gateID string,
		operator string,
		reason string,
		now time.Time,
	) (domain.Gate, error)
	ReturnGateToCentral(
		ctx context.Context,
		gateID string,
		operator string,
		reason string,
		confirmedAperture float64,
		conditionsConfirmed bool,
		now time.Time,
	) (domain.Gate, error)
}
