package main

import (
	"fmt"
	"net/http"
	"sync"
)

func runSwitchGateControl(client *apiClient) error {
	if _, err := fixtureGate(client, "gate-local", "本地接管闸", 5, 1.0); err != nil {
		return err
	}

	// One pending authorization and one approved (reserved) authorization.
	pending, err := fixtureCommand(client, "gate-local", 1.5, "待复核授权", "dispatcher-lin")
	if err != nil {
		return err
	}
	pendingID, err := stringField(pending, "id")
	if err != nil {
		return err
	}
	approved, err := fixtureCommand(client, "gate-local", 2.5, "已批准授权", "dispatcher-lin")
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
	oldToken, err := stringField(approved, "execution_token")
	if err != nil {
		return err
	}

	// Completed history on the same gate must survive the mode change.
	if _, err := fixtureGate(client, "gate-done", "历史完成闸", 5, 1.0); err != nil {
		return err
	}
	completedID, err := completeACommand(client, "gate-done", 3.0)
	if err != nil {
		return err
	}

	takeover, err := client.post("/gates/gate-local/local", map[string]any{
		"requested_by": "field-chen",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(takeover, http.StatusOK, "switch gate to local"); err != nil {
		return err
	}
	takenGate, err := objectField(takeover.Body, "gate")
	if err != nil {
		return err
	}
	mode, err := stringField(takenGate, "mode")
	if err != nil {
		return err
	}
	if mode != "local" {
		return fmt.Errorf("after takeover mode: expected local, got %q", mode)
	}
	status, err := stringField(takenGate, "status")
	if err != nil {
		return err
	}
	if status != "idle" {
		return fmt.Errorf("after takeover status: expected idle, got %q", status)
	}
	if _, present := takenGate["active_command_id"]; present {
		return fmt.Errorf("after takeover gate must not reference an active command: %s", prettyBody(takenGate))
	}
	revokedList, ok := takeover.Body["revoked_commands"].([]any)
	if !ok || len(revokedList) != 2 {
		return fmt.Errorf("takeover should revoke 2 outstanding authorizations, got %s", prettyBody(takeover.Body))
	}

	// Dispatcher can see the mode and the revoked authorizations afterwards.
	readGate, err := client.get("/gates/gate-local")
	if err != nil {
		return err
	}
	if err := requireStatus(readGate, http.StatusOK, "read gate under local control"); err != nil {
		return err
	}
	gateObj, err := objectField(readGate.Body, "gate")
	if err != nil {
		return err
	}
	if mode, _ := stringField(gateObj, "mode"); mode != "local" {
		return fmt.Errorf("read gate mode: expected local, got %q", mode)
	}

	for _, id := range []string{pendingID, approvedID} {
		commandResult, err := client.get("/commands/" + id)
		if err != nil {
			return err
		}
		command, err := objectField(commandResult.Body, "command")
		if err != nil {
			return err
		}
		gotStatus, err := stringField(command, "status")
		if err != nil {
			return err
		}
		if gotStatus != "revoked" {
			return fmt.Errorf("command %s after takeover: expected revoked, got %q", id, gotStatus)
		}
		if revokedBy, _ := stringField(command, "revoked_by"); revokedBy != "field-chen" {
			return fmt.Errorf("command %s revoked_by: expected field-chen, got %q", id, revokedBy)
		}
		if _, present := command["execution_token"]; present {
			return fmt.Errorf("revoked command %s must not carry an execution token", id)
		}
	}

	// The old authorization cannot occupy or move the gate under local control.
	staleExecute, err := client.post("/commands/"+approvedID+"/execute", map[string]any{
		"operator": "operator-chen",
		"token":    oldToken,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(staleExecute, http.StatusConflict, "reject stale token after takeover"); err != nil {
		return err
	}
	localCommand, err := client.post("/commands", map[string]any{
		"gate_id":         "gate-local",
		"target_aperture": 3.5,
		"reason":          "本地模式下的中控申请",
		"requested_by":    "dispatcher-lin",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(localCommand, http.StatusConflict, "reject central command in local mode"); err != nil {
		return err
	}
	if code, _ := stringField(localCommand.Body, "code"); code != "gate_in_local_control" {
		return fmt.Errorf("local mode command error code: expected gate_in_local_control, got %q", code)
	}

	// Local takeover is idempotently rejected while already local.
	again, err := client.post("/gates/gate-local/local", map[string]any{
		"requested_by": "field-chen",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(again, http.StatusConflict, "reject repeated local takeover"); err != nil {
		return err
	}

	// Handback must reconfirm the real conditions measured on site.
	badHandback, err := client.post("/gates/gate-local/central", map[string]any{
		"requested_by":       "dispatcher-lin",
		"confirmed_aperture": 9.0,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(badHandback, http.StatusBadRequest, "reject impossible confirmed aperture"); err != nil {
		return err
	}

	// Field personnel moved the gate manually to 2.0; handback reports that.
	handback, err := client.post("/gates/gate-local/central", map[string]any{
		"requested_by":       "dispatcher-lin",
		"confirmed_aperture": 2.0,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(handback, http.StatusOK, "switch gate back to central"); err != nil {
		return err
	}
	centralGate, err := objectField(handback.Body, "gate")
	if err != nil {
		return err
	}
	if mode, _ := stringField(centralGate, "mode"); mode != "central" {
		return fmt.Errorf("after handback mode: expected central, got %q", mode)
	}
	if aperture, err := floatField(centralGate, "aperture"); err != nil || aperture != 2.0 {
		return fmt.Errorf("after handback aperture: expected 2.0, got %v (err=%v)", aperture, err)
	}

	// New central commands are now based on the reconfirmed real aperture.
	newCommand, err := fixtureCommand(client, "gate-local", 2.25, "切回后的新授权", "dispatcher-lin")
	if err != nil {
		return err
	}
	if starting, err := floatField(newCommand, "starting_aperture"); err != nil || starting != 2.0 {
		return fmt.Errorf("new command starting aperture: expected 2.0, got %v (err=%v)", starting, err)
	}

	// Completed history on the other gate was not rewritten by any switch.
	if err := assertCommandHistoryUntouched(client, "gate-done", completedID); err != nil {
		return err
	}

	// A faulted gate cannot be taken over or handed back until cleared on site.
	if err := assertFaultedGateRejectsModeSwitch(client); err != nil {
		return err
	}

	// Concurrent switch/execute produces exactly one coherent outcome.
	if err := assertSwitchExecuteRace(client); err != nil {
		return err
	}

	// Even without a mode switch, a pending authorization whose starting
	// snapshot drifted (another command moved the gate meanwhile) must be
	// reconfirmed against real conditions instead of acting on stale data.
	if err := assertStaleStartingApertureRejected(client); err != nil {
		return err
	}

	fmt.Println("switch-gate-control: local takeover, authorization handling and central handback verified")
	return nil
}

func completeACommand(client *apiClient, gateID string, target float64) (string, error) {
	command, err := fixtureCommand(client, gateID, target, "历史命令", "dispatcher-lin")
	if err != nil {
		return "", err
	}
	id, err := stringField(command, "id")
	if err != nil {
		return "", err
	}
	approved, err := fixtureClearReview(client, id, "safety-wu")
	if err != nil {
		return "", err
	}
	token, err := stringField(approved, "execution_token")
	if err != nil {
		return "", err
	}
	executed, err := client.post("/commands/"+id+"/execute", map[string]any{
		"operator": "operator-chen",
		"token":    token,
	})
	if err != nil {
		return "", err
	}
	if err := requireStatus(executed, http.StatusOK, "execute history command"); err != nil {
		return "", err
	}
	return id, nil
}

func assertCommandHistoryUntouched(client *apiClient, gateID, completedID string) error {
	switchResult, err := client.post("/gates/"+gateID+"/local", map[string]any{
		"requested_by": "field-chen",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(switchResult, http.StatusOK, "takeover gate with history"); err != nil {
		return err
	}
	if revoked, ok := switchResult.Body["revoked_commands"]; ok {
		if list, _ := revoked.([]any); len(list) != 0 {
			return fmt.Errorf("takeover must not touch finished commands, got %s", prettyBody(switchResult.Body))
		}
	}

	result, err := client.get("/commands/" + completedID)
	if err != nil {
		return err
	}
	command, err := objectField(result.Body, "command")
	if err != nil {
		return err
	}
	gotStatus, err := stringField(command, "status")
	if err != nil {
		return err
	}
	if gotStatus != "completed" {
		return fmt.Errorf("completed history %s was rewritten to %q by the mode switch", completedID, gotStatus)
	}
	if executedBy, _ := stringField(command, "executed_by"); executedBy != "operator-chen" {
		return fmt.Errorf("completed history %s executed_by changed: %q", completedID, executedBy)
	}
	if _, present := command["execution_token"]; present {
		return fmt.Errorf("completed history %s unexpectedly exposes an execution token", completedID)
	}
	return nil
}

func assertFaultedGateRejectsModeSwitch(client *apiClient) error {
	if _, err := fixtureGate(client, "gate-fault", "故障闸", 4, 1.0); err != nil {
		return err
	}
	command, err := fixtureCommand(client, "gate-fault", 3.0, "故障演练", "dispatcher-lin")
	if err != nil {
		return err
	}
	id, err := stringField(command, "id")
	if err != nil {
		return err
	}
	approved, err := fixtureClearReview(client, id, "safety-wu")
	if err != nil {
		return err
	}
	token, err := stringField(approved, "execution_token")
	if err != nil {
		return err
	}
	executed, err := client.post("/commands/"+id+"/execute", map[string]any{
		"operator":       "operator-chen",
		"token":          token,
		"simulate_fault": true,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(executed, http.StatusOK, "simulate fault"); err != nil {
		return err
	}

	takeover, err := client.post("/gates/gate-fault/local", map[string]any{
		"requested_by": "field-chen",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(takeover, http.StatusConflict, "reject takeover of faulted gate"); err != nil {
		return err
	}
	if code, _ := stringField(takeover.Body, "code"); code != "gate_faulted" {
		return fmt.Errorf("faulted takeover code: expected gate_faulted, got %q", code)
	}

	// Gate stays in central mode after the rejected takeover.
	readResult, err := client.get("/gates/gate-fault")
	if err != nil {
		return err
	}
	gateObj, err := objectField(readResult.Body, "gate")
	if err != nil {
		return err
	}
	if mode, _ := stringField(gateObj, "mode"); mode != "central" {
		return fmt.Errorf("faulted gate mode after failed takeover: expected central, got %q", mode)
	}
	return nil
}

func assertStaleStartingApertureRejected(client *apiClient) error {
	if _, err := fixtureGate(client, "gate-stale", "条件漂移闸", 5, 1.0); err != nil {
		return err
	}
	// An older authorization waits unreviewed while a newer one moves the gate.
	older, err := fixtureCommand(client, "gate-stale", 2.0, "较早的授权", "dispatcher-lin")
	if err != nil {
		return err
	}
	olderID, err := stringField(older, "id")
	if err != nil {
		return err
	}
	if _, err := completeACommand(client, "gate-stale", 3.5); err != nil {
		return err
	}

	// The older authorization still freezes the old aperture; its clear review
	// must now fail against the gate's real conditions.
	stale, err := client.post("/commands/"+olderID+"/interlock", map[string]any{
		"verdict":  "clear",
		"verifier": "safety-wu",
		"note":     "尝试占用已漂移的闸门",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(stale, http.StatusConflict, "reject clear review against changed conditions"); err != nil {
		return err
	}
	if code, _ := stringField(stale.Body, "code"); code != "gate_conditions_changed" {
		return fmt.Errorf("stale aperture code: expected gate_conditions_changed, got %q", code)
	}

	// Neither the command (still pending, unwritten) nor the gate changed.
	commandResult, err := client.get("/commands/" + olderID)
	if err != nil {
		return err
	}
	commandObj, err := objectField(commandResult.Body, "command")
	if err != nil {
		return err
	}
	if status, _ := stringField(commandObj, "status"); status != "pending" {
		return fmt.Errorf("rejected stale review must not rewrite the command, status=%q", status)
	}
	gateResult, err := client.get("/gates/gate-stale")
	if err != nil {
		return err
	}
	gateObj, err := objectField(gateResult.Body, "gate")
	if err != nil {
		return err
	}
	if status, _ := stringField(gateObj, "status"); status != "idle" {
		return fmt.Errorf("rejected stale review must leave the gate idle, status=%q", status)
	}
	if aperture, _ := floatField(gateObj, "aperture"); aperture != 3.5 {
		return fmt.Errorf("gate aperture after rejected review: expected 3.5, got %v", aperture)
	}
	return nil
}

func assertSwitchExecuteRace(client *apiClient) error {
	for trial := 0; trial < 5; trial++ {
		gateID := fmt.Sprintf("gate-race-%d", trial)
		if _, err := fixtureGate(client, gateID, "并发闸", 5, 1.0); err != nil {
			return err
		}
		command, err := fixtureCommand(client, gateID, 4.0, "并发授权", "dispatcher-lin")
		if err != nil {
			return err
		}
		id, err := stringField(command, "id")
		if err != nil {
			return err
		}
		approved, err := fixtureClearReview(client, id, "safety-wu")
		if err != nil {
			return err
		}
		token, err := stringField(approved, "execution_token")
		if err != nil {
			return err
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		var executeStatus, switchStatus int
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			result, err := client.post("/commands/"+id+"/execute", map[string]any{
				"operator": "operator-chen",
				"token":    token,
			})
			if err == nil {
				executeStatus = result.Status
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			result, err := client.post("/gates/"+gateID+"/local", map[string]any{
				"requested_by": "field-chen",
			})
			if err == nil {
				switchStatus = result.Status
			}
		}()
		close(start)
		wg.Wait()

		gateResult, err := client.get("/gates/" + gateID)
		if err != nil {
			return err
		}
		gateObj, err := objectField(gateResult.Body, "gate")
		if err != nil {
			return err
		}
		gateMode, _ := stringField(gateObj, "mode")
		gateStatus, _ := stringField(gateObj, "status")
		if gateStatus == "moving" {
			return fmt.Errorf("trial %d: gate must never settle in moving status", trial)
		}

		commandResult, err := client.get("/commands/" + id)
		if err != nil {
			return err
		}
		commandObj, err := objectField(commandResult.Body, "command")
		if err != nil {
			return err
		}
		finalCommandStatus, _ := stringField(commandObj, "status")

		// Invariant: exactly one coherent outcome survives the race. The mutex
		// serializes the two operations, so the final state must be internally
		// consistent whichever one wins:
		//   - execution first  -> command completed at 4.0, takeover may then win
		//   - takeover first   -> command revoked, local gate, execution rejected
		switch finalCommandStatus {
		case "completed":
			if executeStatus != http.StatusOK {
				return fmt.Errorf("trial %d: command completed without a successful execute response (%d)", trial, executeStatus)
			}
			if aperture, _ := floatField(gateObj, "aperture"); aperture != 4.0 {
				return fmt.Errorf("trial %d: completed command left aperture %v, expected 4.0", trial, aperture)
			}
		case "revoked":
			if gateMode != "local" {
				return fmt.Errorf("trial %d: revoked command requires a local gate, mode=%q", trial, gateMode)
			}
			if executeStatus == http.StatusOK {
				return fmt.Errorf("trial %d: revoked command also reports a successful execution", trial)
			}
		default:
			return fmt.Errorf(
				"trial %d: race produced an incoherent outcome (execute=%d switch=%d command=%s gate=%s/%s)",
				trial, executeStatus, switchStatus, finalCommandStatus, gateMode, gateStatus,
			)
		}
	}
	return nil
}
