package schedule

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// DispatcherMux sends Prompt runs to the channel Worker and Script runs to
// the direct local command executor. It is deliberately typed by immutable run kind so a
// Script never enters App Server/Worker processing.
type DispatcherMux struct {
	Prompts     RunDispatcher
	Scripts     ScriptRunService
	Delivery    PromptResultDelivery
	ScriptSlots chan struct{}
}

func (d DispatcherMux) Dispatch(ctx context.Context, run ClaimedRun) error {
	switch run.Kind {
	case TaskPrompt:
		if d.Prompts == nil {
			return fmt.Errorf("scheduled prompt dispatcher is unavailable")
		}
		return d.Prompts.Dispatch(ctx, run)
	case TaskScript:
		if d.Scripts == nil || d.ScriptSlots == nil {
			return fmt.Errorf("scheduled script capacity is unavailable")
		}
		select {
		case d.ScriptSlots <- struct{}{}:
			go func() {
				defer func() { <-d.ScriptSlots }()
				defer func() {
					if recovered := recover(); recovered != nil {
						if err := d.failScript(context.Background(), run, "failed_executor_panic"); err != nil {
							slog.Error("scheduled_script_panic_finalize_failed", "event", "scheduled_script_panic_finalize_failed", "run_id", run.ID, "error", err)
						}
						slog.Error("scheduled_script_executor_panic", "event", "scheduled_script_executor_panic", "run_id", run.ID)
					}
				}()
				if err := d.executeScript(ctx, run); err != nil {
					slog.Error("scheduled_script_execution_failed", "event", "scheduled_script_execution_failed", "run_id", run.ID, "error", err)
				}
			}()
			return nil
		default:
			if err := d.Scripts.FailBeforeExecution(context.Background(), run.ID, run.ClaimToken, "failed_capacity"); err != nil {
				return fmt.Errorf("fail capacity-limited scheduled script: %w", err)
			}
			if d.Delivery != nil {
				if err := d.Delivery.Deliver(context.Background(), run.ID, ResultPresentation{Succeeded: false, ErrorCode: "failed_capacity"}); err != nil {
					slog.Error("scheduled_script_capacity_delivery_failed", "event", "scheduled_script_capacity_delivery_failed", "run_id", run.ID, "error", err)
				}
			}
			return nil
		}
	default:
		return fmt.Errorf("scheduled run kind is invalid")
	}
}

func (d DispatcherMux) executeScript(ctx context.Context, run ClaimedRun) error {
	if d.Scripts == nil {
		return fmt.Errorf("scheduled script executor is unavailable")
	}
	if err := d.Scripts.MarkRunRunning(ctx, run.ID, run.ClaimToken); err != nil {
		return err
	}
	started := time.Now()
	result, err := d.Scripts.Execute(ctx, run)
	metadata, metadataErr := d.Scripts.Metadata(result)
	if metadataErr != nil {
		if finalizeErr := d.failScript(context.Background(), run, "failed_script_metadata"); finalizeErr != nil {
			return fmt.Errorf("metadata scheduled script: %w; finalize: %v", metadataErr, finalizeErr)
		}
		return fmt.Errorf("metadata scheduled script: %w", metadataErr)
	}
	if err != nil {
		code := "failed_script_executor"
		if completeErr := d.Scripts.CompleteScriptRun(context.Background(), RunCompletion{RunID: run.ID, ClaimToken: run.ClaimToken, State: RunFailed, ErrorCode: code, StartedAt: started}, metadata); completeErr != nil {
			return fmt.Errorf("complete failed scheduled script: %w", completeErr)
		}
		if d.Delivery != nil {
			_ = d.Delivery.Deliver(context.Background(), run.ID, ResultPresentation{Succeeded: false, ErrorCode: code, ExitCode: result.ExitCode})
		}
		return fmt.Errorf("execute scheduled script: %w", err)
	}
	state, code := RunSucceeded, ""
	if result.ExitCode != 0 {
		state, code = RunFailed, "script_exit"
		if result.ExitCode == 124 {
			code = "script_timeout"
		}
	}
	if err := d.Scripts.CompleteScriptRun(ctx, RunCompletion{RunID: run.ID, ClaimToken: run.ClaimToken, State: state, ErrorCode: code, StartedAt: started}, metadata); err != nil {
		return err
	}
	if d.Delivery != nil {
		console, displayTruncated := safeScriptConsole(result.Output)
		if err := d.Delivery.Deliver(ctx, run.ID, ResultPresentation{Succeeded: state == RunSucceeded, ErrorCode: code, ScriptConsole: console, ExitCode: result.ExitCode, ScriptOutputWasTruncated: result.Truncated || displayTruncated}); err != nil {
			return fmt.Errorf("deliver scheduled script result: %w", err)
		}
	}
	return nil
}

func (d DispatcherMux) failScript(ctx context.Context, run ClaimedRun, code string) error {
	if d.Scripts == nil {
		return fmt.Errorf("scheduled script executor is unavailable")
	}
	if err := d.Scripts.FailBeforeExecution(ctx, run.ID, run.ClaimToken, code); err != nil {
		return fmt.Errorf("finalize scheduled script: %w", err)
	}
	if d.Delivery != nil {
		if err := d.Delivery.Deliver(ctx, run.ID, ResultPresentation{Succeeded: false, ErrorCode: code}); err != nil {
			return fmt.Errorf("deliver failed scheduled script: %w", err)
		}
	}
	return nil
}

const maxScriptConsoleCardBytes = 12 * 1024

// safeScriptConsole turns untrusted process output into an inert fenced code
// block. The display cap leaves enough headroom for Feishu's JSON card limit
// even when the captured script output is configured higher.
func safeScriptConsole(output []byte) (string, bool) {
	text := strings.ToValidUTF8(string(output), "�")
	text = strings.ReplaceAll(text, "```", "``\u200b`")
	truncated := len(text) > maxScriptConsoleCardBytes
	if truncated {
		text = strings.ToValidUTF8(text[:maxScriptConsoleCardBytes], "")
	}
	return "```text\n" + text + "\n```", truncated
}
