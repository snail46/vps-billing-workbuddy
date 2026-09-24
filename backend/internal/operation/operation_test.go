package operation

import (
	"strings"
	"testing"
	"time"
)

func TestOperationMachineMatchesTheVocabularyTheDocumentFixes(t *testing.T) {
	// docs/05 fixes the words: "Operation: queued/running/waiting_provider/
	// waiting_resource/verifying/retrying/succeeded/failed/cancelled". The test
	// reads the line and holds the machine to it in both directions — an edge
	// into a state the document never named, or a named state the machine never
	// reaches, is found here rather than adopted.
	want := []string{
		StatusQueued, StatusRunning, StatusWaitingProvider, StatusWaitingResource,
		StatusVerifying, StatusRetrying, StatusSucceeded, StatusFailed, StatusCancelled,
	}

	seen := map[string]bool{}
	for _, status := range OperationStatuses() {
		seen[status] = true
	}
	for _, status := range want {
		if !seen[status] {
			t.Errorf("the machine does not know %q, which docs/05 names", status)
		}
	}
	if len(OperationStatuses()) != len(want) {
		t.Errorf("the machine knows %d statuses, docs/05 names %d", len(OperationStatuses()), len(want))
	}

	// Every edge lands on a named state.
	for from, edges := range operationTransitions {
		if !seen[from] {
			t.Errorf("the machine transitions out of %q, which is not a state", from)
		}
		for _, to := range edges {
			if !seen[to] {
				t.Errorf("the edge %s -> %q lands outside the vocabulary", from, to)
			}
		}
	}
}

func TestOperationMachineHoldsTheEdgesTheArchitectureStates(t *testing.T) {
	// The edges ADR-008 fixes, stated here as the table they live in.
	edges := map[string][]string{
		StatusQueued:          {StatusRunning, StatusCancelled},
		StatusRunning:         {StatusWaitingProvider, StatusWaitingResource, StatusVerifying, StatusRetrying, StatusSucceeded, StatusFailed, StatusCancelled},
		StatusWaitingProvider: {StatusRunning, StatusRetrying, StatusFailed, StatusCancelled},
		StatusWaitingResource: {StatusRunning, StatusRetrying, StatusFailed, StatusCancelled},
		StatusVerifying:       {StatusSucceeded, StatusRetrying, StatusFailed, StatusCancelled},
		StatusRetrying:        {StatusRunning, StatusFailed, StatusCancelled},
	}
	for from, tos := range edges {
		for _, to := range tos {
			if !OperationCanTransition(from, to) {
				t.Errorf("%s -> %s is refused, ADR-008 allows it", from, to)
			}
		}
	}

	// And the refusals that make the machine mean something.
	refused := [][2]string{
		{StatusQueued, StatusSucceeded},          // work was never done
		{StatusQueued, StatusRetrying},           // there is no attempt to retry
		{StatusSucceeded, StatusRunning},         // the record is not rewritable
		{StatusFailed, StatusRunning},            // failure is final; retry is a new operation
		{StatusCancelled, StatusRunning},         // cancelled means stopped
		{StatusWaitingProvider, StatusSucceeded}, // the provider must answer first
	}
	for _, pair := range refused {
		if OperationCanTransition(pair[0], pair[1]) {
			t.Errorf("%s -> %s is allowed, ADR-008 refuses it", pair[0], pair[1])
		}
	}
}

func TestTerminalStatesCannotBeLeft(t *testing.T) {
	for _, status := range []string{StatusSucceeded, StatusFailed, StatusCancelled} {
		if !OperationIsTerminal(status) {
			t.Errorf("%s is not terminal", status)
		}
		for _, to := range OperationStatuses() {
			if OperationCanTransition(status, to) {
				t.Errorf("the terminal state %s offers an edge to %s", status, to)
			}
		}
	}
}

func TestBackoffIsExponentialAndCapped(t *testing.T) {
	if got := BackoffFor(1); got != 2*time.Second {
		t.Errorf("the first attempt waits %s, expected 2s", got)
	}
	if got := BackoffFor(2); got != 4*time.Second {
		t.Errorf("the second attempt waits %s, expected 4s", got)
	}
	if got := BackoffFor(5); got != 32*time.Second {
		t.Errorf("the fifth attempt waits %s, expected 32s", got)
	}
	if got := BackoffFor(10); got != BackoffCap {
		t.Errorf("the tenth attempt waits %s, expected the %s cap", got, BackoffCap)
	}
	if got := BackoffFor(1000); got != BackoffCap {
		t.Errorf("the thousandth attempt waits %s, expected the cap", got)
	}
	if got := BackoffFor(0); got != BackoffBase {
		t.Errorf("a nonsensical attempt number waits %s, expected the base", got)
	}
}

func TestProgressIsDerivedFromStepsAndNeverFromACaller(t *testing.T) {
	cases := []struct {
		name  string
		steps []Step
		want  int
	}{
		{"no steps is no progress", nil, 0},
		{"all pending", []Step{{StepKey: "a", Status: StepPending}, {StepKey: "b", Status: StepPending}}, 0},
		{"half done", []Step{{StepKey: "a", Status: StepSucceeded}, {StepKey: "b", Status: StepPending}}, 50},
		{"skipped counts", []Step{{StepKey: "a", Status: StepSkipped}, {StepKey: "b", Status: StepPending}}, 50},
		{"a running step is not done", []Step{{StepKey: "a", Status: StepSucceeded}, {StepKey: "b", Status: StepRunning}}, 50},
		{"a failed step is not done", []Step{{StepKey: "a", Status: StepFailed}, {StepKey: "b", Status: StepPending}}, 0},
		{"all done", []Step{{StepKey: "a", Status: StepSucceeded}, {StepKey: "b", Status: StepSucceeded}}, 100},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ProgressOf(testCase.steps); got != testCase.want {
				t.Errorf("progress = %d, expected %d", got, testCase.want)
			}
		})
	}
}

func TestStepRendersForTheOperator(t *testing.T) {
	step := Step{StepKey: "create_instance", Status: StepFailed}
	if !strings.Contains(step.String(), "create_instance") || !strings.Contains(step.String(), StepFailed) {
		t.Errorf("the step renders as %q, which does not name what an operator needs", step.String())
	}
}
