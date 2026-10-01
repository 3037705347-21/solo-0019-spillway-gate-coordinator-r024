package domain

import (
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
	ControlModeCentral ControlMode = "central"
	ControlModeLocal   ControlMode = "local"
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
		ID:          id,
		Name:        name,
		Status:      GateIdle,
		Mode:        ControlModeCentral,
		Aperture:    initialAperture,
		MaxAperture: maxAperture,
		UpdatedAt:   now.UTC(),
		Version:     1,
	}, nil
}

// SwitchToLocal hands the gate to field personnel for manual control.
// A moving gate cannot be taken over because the service must not interrupt a
// movement it started; a faulted gate keeps its latched status and must be
// cleared on site first. When the gate is reserved by a central authorization,
// the reservation is released atomically so local commands can never occupy it.
func (g *Gate) SwitchToLocal(now time.Time) error {
	if g.Mode == ControlModeLocal {
		return Conflict("gate_already_local", "gate is already under local control")
	}
	switch g.Status {
	case GateIdle:
		g.ActiveCommandID = ""
	case GateReserved:
		g.ActiveCommandID = ""
		g.Status = GateIdle
	case GateMoving:
		return Conflict("gate_is_moving", "gate is moving; wait for the movement to finish before local takeover")
	case GateFaulted:
		return Conflict("gate_faulted", "faulted gates must be cleared on site before local takeover")
	default:
		return Conflict("gate_takeover_unavailable", "gate cannot switch to local control in its current state")
	}

	g.Mode = ControlModeLocal
	g.UpdatedAt = now.UTC()
	g.Version++
	return nil
}

// SwitchToCentral returns the gate to coordinated control. The dispatcher must
// supply the real aperture measured on site at handover time, because local
// manual operations may have moved the gate while it was away from the service.
func (g *Gate) SwitchToCentral(confirmedAperture float64, now time.Time) error {
	if g.Mode == ControlModeCentral {
		return Conflict("gate_already_central", "gate is already under central control")
	}
	if g.Status == GateFaulted {
		return Conflict("gate_faulted", "faulted gates must be cleared on site before central handback")
	}
	if g.Status != GateIdle {
		return Conflict("gate_not_idle", "gate must be idle before returning to central control")
	}
	if confirmedAperture < 0 || confirmedAperture > g.MaxAperture {
		return Invalid("invalid_confirmed_aperture", "confirmed aperture must be within the gate limit")
	}

	g.Mode = ControlModeCentral
	g.Aperture = confirmedAperture
	g.ActiveCommandID = ""
	g.UpdatedAt = now.UTC()
	g.Version++
	return nil
}

// ReleaseReservation frees a gate reserved by a revoked central command without
// changing its control mode or aperture.
func (g *Gate) ReleaseReservation(commandID string, now time.Time) error {
	if g.Status != GateReserved {
		return Conflict("gate_not_reserved", "gate is not reserved by the command")
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

func (g *Gate) Reserve(commandID string, now time.Time) error {
	if g.Mode == ControlModeLocal {
		return Conflict("gate_in_local_control", "gate is under local control and cannot be reserved")
	}
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
	if g.Mode == ControlModeLocal {
		return Conflict("gate_in_local_control", "gate is under local control and cannot start a central movement")
	}
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
