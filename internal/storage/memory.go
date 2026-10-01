package storage

import (
	"context"
	"strings"
	"sync"
	"time"

	"example.com/spillway-gate-coordinator/internal/domain"
)

type MemoryRepository struct {
	mu       sync.RWMutex
	gates    map[string]domain.Gate
	commands map[string]domain.GateCommand
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		gates:    make(map[string]domain.Gate),
		commands: make(map[string]domain.GateCommand),
	}
}

func (m *MemoryRepository) CreateGate(_ context.Context, gate domain.Gate) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.gates[gate.ID]; exists {
		return domain.Conflict("duplicate_gate_id", "gate id already exists")
	}
	m.gates[gate.ID] = gate
	return nil
}

func (m *MemoryRepository) Gate(_ context.Context, id string) (domain.Gate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	gate, exists := m.gates[id]
	if !exists {
		return domain.Gate{}, domain.NotFound("gate_not_found", "gate id was not found")
	}
	return gate, nil
}

func (m *MemoryRepository) CreateCommand(_ context.Context, command domain.GateCommand) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	gate, exists := m.gates[command.GateID]
	if !exists {
		return domain.NotFound("gate_not_found", "gate id was not found")
	}
	if gate.Mode == domain.ControlModeLocal {
		return domain.Conflict("gate_in_local_control", "central commands cannot be created while the gate is under local control")
	}
	if gate.Status == domain.GateFaulted {
		return domain.Conflict("gate_faulted", "faulted gates cannot receive new commands")
	}
	if command.StartingAperture != gate.Aperture {
		return domain.Conflict("gate_aperture_changed", "gate aperture changed before the command was stored")
	}
	if command.TargetAperture > gate.MaxAperture {
		return domain.Invalid("invalid_target_aperture", "target aperture is outside the gate limit")
	}
	if _, exists := m.commands[command.ID]; exists {
		return domain.Conflict("duplicate_command_id", "command id already exists")
	}

	m.commands[command.ID] = command
	return nil
}

func (m *MemoryRepository) Command(_ context.Context, id string) (domain.GateCommand, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	command, exists := m.commands[id]
	if !exists {
		return domain.GateCommand{}, domain.NotFound("command_not_found", "command id was not found")
	}
	return cloneCommand(command), nil
}

func (m *MemoryRepository) ReviewCommand(
	_ context.Context,
	commandID string,
	decision domain.InterlockDecision,
	token string,
	now time.Time,
) (domain.GateCommand, domain.Gate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	command, exists := m.commands[commandID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("command_not_found", "command id was not found")
	}
	gate, exists := m.gates[command.GateID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("gate_not_found", "gate id was not found")
	}

	nextCommand := cloneCommand(command)
	if err := nextCommand.Review(decision, token, now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	nextGate := gate
	if decision.IsClear() {
		// A clear decision occupies the gate, so it must be confirmed against
		// the gate's real current conditions. Local manual movement before
		// handback can invalidate the starting aperture snapshot; such a
		// command must be re-issued instead of silently targeting stale data.
		if nextGate.Aperture != nextCommand.StartingAperture {
			return domain.GateCommand{}, domain.Gate{}, domain.Conflict(
				"gate_conditions_changed",
				"gate aperture changed since the command was requested; reconfirm and reissue the command",
			)
		}
		if err := nextGate.Reserve(nextCommand.ID, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
	}

	m.commands[nextCommand.ID] = nextCommand
	m.gates[nextGate.ID] = nextGate
	return cloneCommand(nextCommand), nextGate, nil
}

func (m *MemoryRepository) ExecuteCommand(
	_ context.Context,
	commandID string,
	token string,
	operator string,
	simulateFault bool,
	now time.Time,
) (domain.GateCommand, domain.Gate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	command, exists := m.commands[commandID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("command_not_found", "command id was not found")
	}
	gate, exists := m.gates[command.GateID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("gate_not_found", "gate id was not found")
	}

	nextCommand := cloneCommand(command)
	nextGate := gate

	// Reconfirm against the gate's real state before any transition: central
	// execution is forbidden under local control, and an authorization whose
	// starting snapshot no longer matches the gate must not start a movement.
	if nextGate.Mode != domain.ControlModeCentral {
		return domain.GateCommand{}, domain.Gate{}, domain.Conflict(
			"gate_in_local_control",
			"gate is under local control and cannot execute a central command",
		)
	}
	if nextGate.Aperture != nextCommand.StartingAperture {
		return domain.GateCommand{}, domain.Gate{}, domain.Conflict(
			"gate_conditions_changed",
			"gate aperture changed since the command was requested; reconfirm and reissue the command",
		)
	}

	if err := nextCommand.BeginExecution(token, operator, now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}
	if err := nextGate.StartMovement(nextCommand.ID, now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	if simulateFault {
		if err := nextGate.Settle(nextCommand.TargetAperture, true, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
		if err := nextCommand.Abort("simulated_gate_fault", now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
	} else {
		if err := nextGate.Settle(nextCommand.TargetAperture, false, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
		if err := nextCommand.Complete(now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
	}

	m.commands[nextCommand.ID] = nextCommand
	m.gates[nextGate.ID] = nextGate
	return cloneCommand(nextCommand), nextGate, nil
}

func (m *MemoryRepository) RevokeCommand(
	_ context.Context,
	commandID string,
	reason string,
	revokedBy string,
	now time.Time,
) (domain.GateCommand, domain.Gate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	command, exists := m.commands[commandID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("command_not_found", "command id was not found")
	}
	gate, exists := m.gates[command.GateID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("gate_not_found", "gate id was not found")
	}

	nextCommand := cloneCommand(command)
	if err := nextCommand.Revoke(reason, revokedBy, now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	nextGate := gate
	if nextGate.Status == domain.GateReserved && nextGate.ActiveCommandID == nextCommand.ID {
		if err := nextGate.ReleaseReservation(nextCommand.ID, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
	}

	m.commands[nextCommand.ID] = nextCommand
	m.gates[nextGate.ID] = nextGate
	return cloneCommand(nextCommand), nextGate, nil
}

func (m *MemoryRepository) SwitchGateToLocal(
	_ context.Context,
	gateID string,
	requestedBy string,
	now time.Time,
) (domain.Gate, []domain.GateCommand, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if strings.TrimSpace(requestedBy) == "" {
		return domain.Gate{}, nil, domain.Invalid("invalid_requested_by", "requested_by is required")
	}
	gate, exists := m.gates[gateID]
	if !exists {
		return domain.Gate{}, nil, domain.NotFound("gate_not_found", "gate id was not found")
	}

	// Validate the mode switch on a copy first so a rejected takeover (for
	// example a moving or faulted gate) leaves every command untouched.
	nextGate := gate
	if err := nextGate.SwitchToLocal(now); err != nil {
		return domain.Gate{}, nil, err
	}

	// Prepare the revocation of every central authorization still waiting on
	// this gate so no pending or approved command can survive the takeover.
	reason := "gate_switched_to_local_control"
	type revokedEntry struct {
		id      string
		command domain.GateCommand
	}
	entries := make([]revokedEntry, 0, 1)
	for id, command := range m.commands {
		if command.GateID != gateID {
			continue
		}
		if command.Status != domain.CommandPending && command.Status != domain.CommandApproved {
			continue
		}
		nextCommand := cloneCommand(command)
		if err := nextCommand.Revoke(reason, requestedBy, now); err != nil {
			return domain.Gate{}, nil, err
		}
		entries = append(entries, revokedEntry{id: id, command: nextCommand})
	}

	// All validations passed: commit the gate and the revocations together.
	m.gates[gateID] = nextGate
	revoked := make([]domain.GateCommand, 0, len(entries))
	for _, entry := range entries {
		m.commands[entry.id] = entry.command
		revoked = append(revoked, cloneCommand(entry.command))
	}
	return nextGate, revoked, nil
}

func (m *MemoryRepository) SwitchGateToCentral(
	_ context.Context,
	gateID string,
	requestedBy string,
	confirmedAperture float64,
	now time.Time,
) (domain.Gate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	gate, exists := m.gates[gateID]
	if !exists {
		return domain.Gate{}, domain.NotFound("gate_not_found", "gate id was not found")
	}
	if strings.TrimSpace(requestedBy) == "" {
		return domain.Gate{}, domain.Invalid("invalid_requested_by", "requested_by is required")
	}

	nextGate := gate
	if err := nextGate.SwitchToCentral(confirmedAperture, now); err != nil {
		return domain.Gate{}, err
	}

	// No pending central command may remain at this point: all of them were
	// revoked during takeover, so the gate returns with a clean command table.
	m.gates[gateID] = nextGate
	return nextGate, nil
}

func cloneCommand(command domain.GateCommand) domain.GateCommand {
	if command.Interlock == nil {
		return command
	}
	decision := *command.Interlock
	command.Interlock = &decision
	return command
}
