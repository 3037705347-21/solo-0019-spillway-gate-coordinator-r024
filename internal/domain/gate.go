package domain

import (
	"math"
	"regexp"
	"strings"
	"time"
)

type GateStatus string

const (
	GateIdle     GateStatus = "idle"
	GateReserved GateStatus = "reserved"
	GateMoving   GateStatus = "moving"
	GateFaulted  GateStatus = "faulted"
)

type ControlMode string

const (
	ControlCentral ControlMode = "central"
	ControlLocal   ControlMode = "local"
)

var gateIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,31}$`)

type Gate struct {
	ID              string
	Name            string
	Status          GateStatus
	Mode            ControlMode
	Aperture        float64
	MaxAperture     float64
	ActiveCommandID string
	ModeChangedBy   string
	ModeReason      string
	ModeChangedAt   time.Time
	UpdatedAt       time.Time
	Version         int64
}

func NewGate(id, name string, maxAperture, initialAperture float64, now time.Time) (Gate, error) {
	id = strings.TrimSpace(id)
	if !gateIDPattern.MatchString(id) {
		return Gate{}, Invalid("invalid_gate_id", "gate id must use lowercase letters, digits, and hyphens")
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return Gate{}, Invalid("invalid_gate_name", "gate name is required")
	}
	if maxAperture <= 0 || maxAperture > 1000 {
		return Gate{}, Invalid("invalid_max_aperture", "max aperture must be greater than zero")
	}
	if initialAperture < 0 || initialAperture > maxAperture {
		return Gate{}, Invalid("invalid_initial_aperture", "initial aperture must be within the gate limit")
	}

	return Gate{
		ID:            id,
		Name:          name,
		Status:        GateIdle,
		Mode:          ControlCentral,
		Aperture:      initialAperture,
		MaxAperture:   maxAperture,
		ModeChangedAt: now.UTC(),
		UpdatedAt:     now.UTC(),
		Version:       1,
	}, nil
}

func (g *Gate) Reserve(commandID string, now time.Time) error {
	if g.Status != GateIdle {
		return Conflict("gate_not_idle", "gate is not available for a new command")
	}
	if g.ActiveCommandID != "" {
		return Conflict("gate_already_reserved", "gate already references an active command")
	}
	if strings.TrimSpace(commandID) == "" {
		return Invalid("invalid_active_command", "active command id is required")
	}

	g.Status = GateReserved
	g.ActiveCommandID = commandID
	g.UpdatedAt = now.UTC()
	g.Version++
	return nil
}

func (g *Gate) StartMovement(commandID string, now time.Time) error {
	if g.Status != GateReserved {
		return Conflict("gate_not_reserved", "gate must be reserved before movement starts")
	}
	if g.ActiveCommandID != commandID {
		return Conflict("gate_command_mismatch", "gate is reserved by a different command")
	}

	g.Status = GateMoving
	g.UpdatedAt = now.UTC()
	g.Version++
	return nil
}

func (g *Gate) Settle(finalAperture float64, faulted bool, now time.Time) error {
	if g.Status != GateMoving {
		return Conflict("gate_not_moving", "gate must be moving before it can settle")
	}
	if finalAperture < 0 || finalAperture > g.MaxAperture {
		return Invalid("invalid_final_aperture", "final aperture must be within the gate limit")
	}

	g.Aperture = finalAperture
	g.ActiveCommandID = ""
	if faulted {
		g.Status = GateFaulted
	} else {
		g.Status = GateIdle
	}
	g.UpdatedAt = now.UTC()
	g.Version++
	return nil
}

// SwitchToLocal hands the gate to on-site manual control. It is only allowed
// while the gate is settled under central control: a moving or faulted gate
// cannot be handed over, and a gate still occupied by an approved central
// authorization must be released explicitly first. It never changes aperture
// or rewrites any command.
func (g *Gate) SwitchToLocal(operator, reason string, now time.Time) error {
	if g.Mode != ControlCentral {
		return Conflict("gate_already_local", "gate is already under local control")
	}
	if g.Status == GateFaulted {
		return Conflict("gate_faulted", "faulted gates cannot switch control mode")
	}
	if g.Status == GateMoving {
		return Conflict("gate_moving", "gate is moving; wait for movement to end before switching")
	}
	if g.Status == GateReserved || g.ActiveCommandID != "" {
		return Conflict(
			"gate_has_active_authorization",
			"revoke the outstanding central authorization before switching to local control",
		)
	}

	operator = strings.TrimSpace(operator)
	if operator == "" {
		return Invalid("invalid_operator", "operator is required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Invalid("invalid_mode_reason", "reason for the mode switch is required")
	}

	g.Mode = ControlLocal
	g.ModeChangedBy = operator
	g.ModeReason = reason
	g.ModeChangedAt = now.UTC()
	g.UpdatedAt = now.UTC()
	g.Version++
	return nil
}

// ReturnToCentral restores central control. Because field conditions may have
// changed during manual operation, the dispatcher must reconfirm against the
// real situation at switch time: conditions must be explicitly confirmed and a
// freshly measured aperture must be supplied, which becomes the new aperture
// of record. Only an idle, locally controlled gate can return.
func (g *Gate) ReturnToCentral(
	operator string,
	reason string,
	confirmedAperture float64,
	conditionsConfirmed bool,
	now time.Time,
) error {
	if g.Mode != ControlLocal {
		return Conflict("gate_not_local", "gate is already under central control")
	}
	if g.Status != GateIdle {
		if g.Status == GateFaulted {
			return Conflict("gate_faulted", "faulted gates cannot switch control mode")
		}
		return Conflict("gate_not_idle", "gate must be settled before returning to central control")
	}

	operator = strings.TrimSpace(operator)
	if operator == "" {
		return Invalid("invalid_operator", "operator is required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Invalid("invalid_mode_reason", "reason for the mode switch is required")
	}
	if !conditionsConfirmed {
		return Invalid(
			"conditions_not_confirmed",
			"real field conditions must be explicitly confirmed before returning to central control",
		)
	}
	if math.IsNaN(confirmedAperture) || math.IsInf(confirmedAperture, 0) ||
		confirmedAperture < 0 || confirmedAperture > g.MaxAperture {
		return Invalid("invalid_confirmed_aperture", "confirmed aperture must be within the gate limit")
	}

	g.Mode = ControlCentral
	g.ModeChangedBy = operator
	g.ModeReason = reason
	g.ModeChangedAt = now.UTC()
	g.Aperture = confirmedAperture
	g.UpdatedAt = now.UTC()
	g.Version++
	return nil
}

// ReleaseReservation clears a central reservation without settling movement.
// It is used when an approved but unexecuted authorization is revoked.
func (g *Gate) ReleaseReservation(commandID string, now time.Time) error {
	if g.Status != GateReserved {
		return Conflict("gate_not_reserved", "gate is not reserved")
	}
	if g.ActiveCommandID != commandID {
		return Conflict("gate_command_mismatch", "gate is reserved by a different command")
	}

	g.Status = GateIdle
	g.ActiveCommandID = ""
	g.UpdatedAt = now.UTC()
	g.Version++
	return nil
}
