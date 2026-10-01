package main

import (
	"fmt"
	"net/http"
)

// runLocalModeControl verifies the full field handover lifecycle:
//   - dispatchers can see the current control mode;
//   - an outstanding approved central authorization must be handled (revoked)
//     before switching to local;
//   - while local, interlock review cannot occupy the gate and an approved
//     authorization cannot start movement;
//   - returning to central requires a fresh reconfirmation of real conditions,
//     and the measured aperture becomes the new aperture of record;
//   - history (the revoked and completed commands) is never rewritten.
func runLocalModeControl(client *apiClient) error {
	if _, err := fixtureGate(client, "gate-local-a", "现场闸A", 5, 0.5); err != nil {
		return err
	}

	gateResult, err := client.get("/gates/gate-local-a")
	if err != nil {
		return err
	}
	if err := requireStatus(gateResult, http.StatusOK, "read gate before switch"); err != nil {
		return err
	}
	gate, err := objectField(gateResult.Body, "gate")
	if err != nil {
		return err
	}
	mode, err := stringField(gate, "mode")
	if err != nil {
		return err
	}
	if mode != "central" {
		return fmt.Errorf("initial mode: expected central, got %q", mode)
	}

	// An approved, unexecuted authorization still occupies the gate.
	approved, err := fixtureCommand(client, "gate-local-a", 2.0, "中控开度调整", "dispatcher-zhou")
	if err != nil {
		return err
	}
	approvedID, err := stringField(approved, "id")
	if err != nil {
		return err
	}
	approved, err = fixtureClearReview(client, approvedID, "safety-wu")
	if err != nil {
		return err
	}
	token, err := stringField(approved, "execution_token")
	if err != nil {
		return err
	}

	// Switching to local while the gate is reserved must be refused; the
	// reservation and the authorization must remain intact.
	blocked, err := client.post("/gates/gate-local-a/mode", map[string]any{
		"mode":     "local",
		"operator": "operator-chen",
		"reason":   "现场要求手动操作",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(blocked, http.StatusConflict, "reject local switch with active authorization"); err != nil {
		return err
	}
	code, err := stringField(blocked.Body, "code")
	if err != nil {
		return err
	}
	if code != "gate_has_active_authorization" {
		return fmt.Errorf("local switch conflict code: expected gate_has_active_authorization, got %q", code)
	}

	// Dispatcher handles the outstanding authorization: revoke it. The
	// reservation must be released atomically.
	revoked, err := client.post("/commands/"+approvedID+"/revoke", map[string]any{
		"operator": "dispatcher-zhou",
		"reason":   "闸门移交现场手动控制",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(revoked, http.StatusOK, "revoke outstanding authorization"); err != nil {
		return err
	}
	revokedCommand, err := objectField(revoked.Body, "command")
	if err != nil {
		return err
	}
	revokedStatus, err := stringField(revokedCommand, "status")
	if err != nil {
		return err
	}
	if revokedStatus != "revoked" {
		return fmt.Errorf("revoked command status: expected revoked, got %q", revokedStatus)
	}
	revokedGate, err := objectField(revoked.Body, "gate")
	if err != nil {
		return err
	}
	revokedGateStatus, err := stringField(revokedGate, "status")
	if err != nil {
		return err
	}
	if revokedGateStatus != "idle" {
		return fmt.Errorf("gate after revoke: expected idle, got %q", revokedGateStatus)
	}
	if _, exists := revokedGate["active_command_id"]; exists {
		return fmt.Errorf("gate still references active command after revoke: %s", prettyBody(revokedGate))
	}

	// The old token must not execute anymore.
	stale, err := client.post("/commands/"+approvedID+"/execute", map[string]any{
		"operator": "operator-chen",
		"token":    token,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(stale, http.StatusConflict, "reject execution with revoked token"); err != nil {
		return err
	}

	// Now the gate can be handed to local control.
	local, err := client.post("/gates/gate-local-a/mode", map[string]any{
		"mode":     "local",
		"operator": "operator-chen",
		"reason":   "现场手动操作闸门",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(local, http.StatusOK, "switch to local control"); err != nil {
		return err
	}
	localGate, err := objectField(local.Body, "gate")
	if err != nil {
		return err
	}
	localMode, err := stringField(localGate, "mode")
	if err != nil {
		return err
	}
	if localMode != "local" {
		return fmt.Errorf("mode after switch: expected local, got %q", localMode)
	}
	localStatus, err := stringField(localGate, "status")
	if err != nil {
		return err
	}
	if localStatus != "idle" {
		return fmt.Errorf("gate status after local switch: expected idle, got %q", localStatus)
	}
	if by, err := stringField(localGate, "mode_changed_by"); err != nil || by != "operator-chen" {
		return fmt.Errorf("mode_changed_by mismatch: %v", err)
	}

	// A second switch to the same mode is a stable conflict.
	again, err := client.post("/gates/gate-local-a/mode", map[string]any{
		"mode":     "local",
		"operator": "operator-chen",
		"reason":   "重复切换",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(again, http.StatusConflict, "reject duplicate local switch"); err != nil {
		return err
	}

	// While local, a clear interlock review must not occupy the gate.
	pending, err := fixtureCommand(client, "gate-local-a", 3.0, "本地期间提交的中控命令", "dispatcher-zhou")
	if err != nil {
		return err
	}
	pendingID, err := stringField(pending, "id")
	if err != nil {
		return err
	}
	deniedReview, err := client.post("/commands/"+pendingID+"/interlock", map[string]any{
		"verdict":  "clear",
		"verifier": "safety-wu",
		"note":     "本地模式下尝试占用",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(deniedReview, http.StatusConflict, "reject interlock review under local mode"); err != nil {
		return err
	}
	reviewCode, err := stringField(deniedReview.Body, "code")
	if err != nil {
		return err
	}
	if reviewCode != "gate_local_mode" {
		return fmt.Errorf("local review code: expected gate_local_mode, got %q", reviewCode)
	}

	// The rejected review must not have changed anything: command still pending,
	// gate still idle with no reservation.
	unchangedCommand, err := client.get("/commands/" + pendingID)
	if err != nil {
		return err
	}
	commandBody, err := objectField(unchangedCommand.Body, "command")
	if err != nil {
		return err
	}
	pendingStatus, err := stringField(commandBody, "status")
	if err != nil {
		return err
	}
	if pendingStatus != "pending" {
		return fmt.Errorf("command after local review attempt: expected pending, got %q", pendingStatus)
	}
	unchangedGate, err := client.get("/gates/gate-local-a")
	if err != nil {
		return err
	}
	gateBody, err := objectField(unchangedGate.Body, "gate")
	if err != nil {
		return err
	}
	gateStatus, err := stringField(gateBody, "status")
	if err != nil {
		return err
	}
	if gateStatus != "idle" {
		return fmt.Errorf("gate after local review attempt: expected idle, got %q", gateStatus)
	}

	// Returning without reconfirming real conditions must fail.
	missingConfirm, err := client.post("/gates/gate-local-a/mode", map[string]any{
		"mode":                 "central",
		"operator":             "dispatcher-zhou",
		"reason":               "现场操作完毕",
		"confirmed_aperture":   1.25,
		"conditions_confirmed": false,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(missingConfirm, http.StatusBadRequest, "reject central return without reconfirmation"); err != nil {
		return err
	}
	outOfRange, err := client.post("/gates/gate-local-a/mode", map[string]any{
		"mode":                 "central",
		"operator":             "dispatcher-zhou",
		"reason":               "现场操作完毕",
		"confirmed_aperture":   9.0,
		"conditions_confirmed": true,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(outOfRange, http.StatusBadRequest, "reject central return with out-of-range aperture"); err != nil {
		return err
	}

	// A valid return reconfirms conditions and adopts the measured aperture as
	// the new truth (field crew moved the gate manually to 1.25).
	returned, err := client.post("/gates/gate-local-a/mode", map[string]any{
		"mode":                 "central",
		"operator":             "dispatcher-zhou",
		"reason":               "现场操作完毕，恢复中控",
		"confirmed_aperture":   1.25,
		"conditions_confirmed": true,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(returned, http.StatusOK, "return to central control"); err != nil {
		return err
	}
	returnedGate, err := objectField(returned.Body, "gate")
	if err != nil {
		return err
	}
	returnedMode, err := stringField(returnedGate, "mode")
	if err != nil {
		return err
	}
	if returnedMode != "central" {
		return fmt.Errorf("mode after return: expected central, got %q", returnedMode)
	}
	aperture, err := floatField(returnedGate, "aperture")
	if err != nil {
		return err
	}
	if aperture != 1.25 {
		return fmt.Errorf("aperture after return: expected 1.25 (measured), got %v", aperture)
	}

	// After return the earlier clear review is still impossible on the revoked
	// command, and its history has not been rewritten.
	oldCommand, err := client.get("/commands/" + approvedID)
	if err != nil {
		return err
	}
	oldBody, err := objectField(oldCommand.Body, "command")
	if err != nil {
		return err
	}
	oldStatus, err := stringField(oldBody, "status")
	if err != nil {
		return err
	}
	if oldStatus != "revoked" {
		return fmt.Errorf("revoked command history was rewritten to %q", oldStatus)
	}
	oldInterlock, err := objectField(oldBody, "interlock")
	if err != nil {
		return err
	}
	oldVerdict, err := stringField(oldInterlock, "verdict")
	if err != nil {
		return err
	}
	if oldVerdict != "clear" {
		return fmt.Errorf("recorded interlock decision was rewritten: got %q", oldVerdict)
	}
	oldRevokedBy, err := stringField(oldBody, "revoked_by")
	if err != nil {
		return err
	}
	if oldRevokedBy != "dispatcher-zhou" {
		return fmt.Errorf("revoked_by: expected dispatcher-zhou, got %q", oldRevokedBy)
	}

	fmt.Println("local-mode-control: authorization revoked before handover, local blocks review/execution, central return reconfirmed")
	return nil
}
