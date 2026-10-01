package main

import (
	"fmt"
	"net/http"
	"sync"
)

// runLocalModeConcurrency hammers the single-writer boundary: a mode switch
// racing against an execution (or an authorization revocation) must end with
// exactly one consistent outcome. The gate must never settle a movement that
// the switch won, nor stay reserved after a lost switch.
func runLocalModeConcurrency(client *apiClient) error {
	const iterations = 30
	for i := 0; i < iterations; i++ {
		gateID := fmt.Sprintf("gate-race-%02d", i)
		if _, err := fixtureGate(client, gateID, fmt.Sprintf("并发闸%02d", i), 5, 0.5); err != nil {
			return err
		}
		command, err := fixtureCommand(client, gateID, 2.0, "并发切换与执行", "dispatcher-zhou")
		if err != nil {
			return err
		}
		commandID, err := stringField(command, "id")
		if err != nil {
			return err
		}
		approved, err := fixtureClearReview(client, commandID, "safety-wu")
		if err != nil {
			return err
		}
		token, err := stringField(approved, "execution_token")
		if err != nil {
			return err
		}

		var wg sync.WaitGroup
		results := make(chan apiResult, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			// The switch needs the gate released first; revoke competes with
			// execution for the approved authorization.
			result, _ := client.post("/commands/"+commandID+"/revoke", map[string]any{
				"operator": "dispatcher-zhou",
				"reason":   "现场接管，撤销授权",
			})
			results <- result
		}()
		go func() {
			defer wg.Done()
			result, _ := client.post("/commands/"+commandID+"/execute", map[string]any{
				"operator": "operator-chen",
				"token":    token,
			})
			results <- result
		}()
		wg.Wait()
		close(results)

		var statuses []int
		for result := range results {
			statuses = append(statuses, result.Status)
		}
		successes := 0
		for _, status := range statuses {
			if status == http.StatusOK {
				successes++
			} else if status != http.StatusConflict {
				return fmt.Errorf("iteration %d: unexpected status %d in revoke/execute race", i, status)
			}
		}
		if successes != 1 {
			return fmt.Errorf("iteration %d: expected exactly one winning result, got %d (%v)", i, successes, statuses)
		}

		finalCommandResult, err := client.get("/commands/" + commandID)
		if err != nil {
			return err
		}
		finalCommand, err := objectField(finalCommandResult.Body, "command")
		if err != nil {
			return err
		}
		finalStatus, err := stringField(finalCommand, "status")
		if err != nil {
			return err
		}
		finalGateResult, err := client.get("/gates/" + gateID)
		if err != nil {
			return err
		}
		finalGate, err := objectField(finalGateResult.Body, "gate")
		if err != nil {
			return err
		}
		finalGateStatus, err := stringField(finalGate, "status")
		if err != nil {
			return err
		}

		switch finalStatus {
		case "revoked":
			if finalGateStatus != "idle" {
				return fmt.Errorf("iteration %d: revoked command left gate in %q", i, finalGateStatus)
			}
			if _, exists := finalGate["active_command_id"]; exists {
				return fmt.Errorf("iteration %d: revoked command still occupies gate", i)
			}
		case "completed":
			if finalGateStatus != "idle" {
				return fmt.Errorf("iteration %d: completed command left gate in %q", i, finalGateStatus)
			}
			if aperture, err := floatField(finalGate, "aperture"); err != nil || aperture != 2.0 {
				return fmt.Errorf("iteration %d: completed command aperture mismatch: %v %v", i, aperture, err)
			}
		default:
			return fmt.Errorf("iteration %d: race produced non-terminal command status %q", i, finalStatus)
		}

		// Whatever lost the race, the winner's result cannot be undone by a
		// late retry: no second movement may start.
		retry, err := client.post("/commands/"+commandID+"/execute", map[string]any{
			"operator": "operator-chen",
			"token":    token,
		})
		if err != nil {
			return err
		}
		if retry.Status != http.StatusConflict {
			return fmt.Errorf("iteration %d: post-race execute retry unexpectedly succeeded (%d)", i, retry.Status)
		}

		// Only after the revoke (not the execution) can local control take over.
		if finalStatus == "revoked" {
			switchResult, err := client.post("/gates/"+gateID+"/mode", map[string]any{
				"mode":     "local",
				"operator": "operator-chen",
				"reason":   "现场接管",
			})
			if err != nil {
				return err
			}
			if err := requireStatus(switchResult, http.StatusOK, "switch after winning revoke"); err != nil {
				return err
			}
			// And while local, the completed/future central work is blocked.
			denied, err := client.post("/gates/"+gateID+"/mode", map[string]any{
				"mode":     "local",
				"operator": "operator-chen",
				"reason":   "重复切换",
			})
			if err != nil {
				return err
			}
			if err := requireStatus(denied, http.StatusConflict, "duplicate local switch rejected"); err != nil {
				return err
			}
		}

		// Direct race: a switch-to-local call competing with the execution of
		// the live authorization. The switch can never move the gate while a
		// movement starts, and a completed execution cannot be redone: exactly
		// one ordering wins, and the final state must stay consistent.
		switchGateID := fmt.Sprintf("gate-switch-race-%02d", i)
		if _, err := fixtureGate(client, switchGateID, fmt.Sprintf("切换并发闸%02d", i), 5, 0.5); err != nil {
			return err
		}
		switchCommand, err := fixtureCommand(client, switchGateID, 3.0, "直接与切换竞争的执行", "dispatcher-zhou")
		if err != nil {
			return err
		}
		switchCommandID, err := stringField(switchCommand, "id")
		if err != nil {
			return err
		}
		switchApproved, err := fixtureClearReview(client, switchCommandID, "safety-wu")
		if err != nil {
			return err
		}
		switchToken, err := stringField(switchApproved, "execution_token")
		if err != nil {
			return err
		}

		var raceWG sync.WaitGroup
		raceResults := make(chan apiResult, 2)
		raceWG.Add(2)
		go func() {
			defer raceWG.Done()
			result, _ := client.post("/gates/"+switchGateID+"/mode", map[string]any{
				"mode":     "local",
				"operator": "operator-chen",
				"reason":   "现场接管",
			})
			raceResults <- result
		}()
		go func() {
			defer raceWG.Done()
			result, _ := client.post("/commands/"+switchCommandID+"/execute", map[string]any{
				"operator": "operator-chen",
				"token":    switchToken,
			})
			raceResults <- result
		}()
		raceWG.Wait()
		close(raceResults)

		var switchOK bool
		execOK := 0
		for result := range raceResults {
			switch result.Status {
			case http.StatusOK:
				// The mode switch response carries only "gate"; the execution
				// response also carries "command".
				if _, hasCommand := result.Body["command"]; hasCommand {
					execOK++
				} else {
					switchOK = true
				}
			case http.StatusConflict:
			default:
				return fmt.Errorf("iteration %d: unexpected status %d in switch/execute race", i, result.Status)
			}
		}
		if execOK != 1 {
			return fmt.Errorf("iteration %d: execution must win exactly once in switch race, got %d", i, execOK)
		}

		raceCommandResult, err := client.get("/commands/" + switchCommandID)
		if err != nil {
			return err
		}
		raceCommand, err := objectField(raceCommandResult.Body, "command")
		if err != nil {
			return err
		}
		raceCommandStatus, err := stringField(raceCommand, "status")
		if err != nil {
			return err
		}
		if raceCommandStatus != "completed" {
			return fmt.Errorf("iteration %d: switch/execute race command status: expected completed, got %q", i, raceCommandStatus)
		}
		raceGateResult, err := client.get("/gates/" + switchGateID)
		if err != nil {
			return err
		}
		raceGate, err := objectField(raceGateResult.Body, "gate")
		if err != nil {
			return err
		}
		if status, err := stringField(raceGate, "status"); err != nil || status != "idle" {
			return fmt.Errorf("iteration %d: race gate status expected idle, got %q", i, status)
		}
		if aperture, err := floatField(raceGate, "aperture"); err != nil || aperture != 3.0 {
			return fmt.Errorf("iteration %d: race gate aperture expected 3.0, got %v", i, aperture)
		}
		raceMode, err := stringField(raceGate, "mode")
		if err != nil {
			return err
		}
		if switchOK && raceMode != "local" {
			return fmt.Errorf("iteration %d: switch reported success but gate mode is %q", i, raceMode)
		}
		if !switchOK && raceMode != "central" {
			return fmt.Errorf("iteration %d: switch lost the race but gate mode is %q", i, raceMode)
		}

		// A second execution attempt must never move the gate regardless of mode.
		secondExec, err := client.post("/commands/"+switchCommandID+"/execute", map[string]any{
			"operator": "operator-chen",
			"token":    switchToken,
		})
		if err != nil {
			return err
		}
		if secondExec.Status != http.StatusConflict {
			return fmt.Errorf("iteration %d: second execution after switch race must be conflict, got %d", i, secondExec.Status)
		}
	}

	fmt.Printf("local-mode-concurrency: %d revoke/execute and switch/execute races each produced a single consistent winner\n", iterations)
	return nil
}
