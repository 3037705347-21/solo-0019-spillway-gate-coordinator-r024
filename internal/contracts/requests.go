package contracts

type RegisterGateRequest struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	MaxAperture     float64 `json:"max_aperture"`
	InitialAperture float64 `json:"initial_aperture"`
}

type RequestCommandRequest struct {
	GateID         string  `json:"gate_id"`
	TargetAperture float64 `json:"target_aperture"`
	Reason         string  `json:"reason"`
	RequestedBy    string  `json:"requested_by"`
}

type ReviewInterlockRequest struct {
	Verdict  string `json:"verdict"`
	Verifier string `json:"verifier"`
	Note     string `json:"note"`
}

type ExecuteCommandRequest struct {
	Operator      string `json:"operator"`
	Token         string `json:"token"`
	SimulateFault bool   `json:"simulate_fault"`
}

type RevokeCommandRequest struct {
	RevokedBy string `json:"revoked_by"`
	Reason    string `json:"reason"`
}

type SwitchGateModeRequest struct {
	RequestedBy string `json:"requested_by"`
	// ConfirmedAperture is required when switching back to central control:
	// zero is a valid fully-closed reading, so a pointer distinguishes an
	// explicit zero from a field that was simply omitted.
	ConfirmedAperture *float64 `json:"confirmed_aperture"`
}
