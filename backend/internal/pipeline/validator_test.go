package pipeline

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// task builds a minimally valid task for the operator under test.
func task(key, operator string, config map[string]any) Task {
	return Task{
		TaskKey:      key,
		Name:         key,
		OperatorType: operator,
		// A zero timeout is rejected by the validator: a task with no deadline
		// would hang its worker slot forever.
		OperatorConfig: config,
		TimeoutSeconds: 600,
		Enabled:        true,
	}
}

// simpleSpec is a valid two-stage graph: extract then load.
func simpleSpec() PipelineSpec {
	return PipelineSpec{
		Name:          "orders",
		DAGID:         "edp_orders",
		MaxActiveRuns: 1,
		Tasks: []Task{
			task("extract", "python", map[string]any{"python_callable": "extract_orders"}),
			task("load", "python", map[string]any{"python_callable": "load_orders"}),
		},
		Dependencies: []Dependency{
			{UpstreamKey: "extract", DownstreamKey: "load"},
		},
	}
}

func TestValidatorAcceptsValidGraph(t *testing.T) {
	t.Parallel()

	v := NewValidator(nil)
	if issues := v.Validate(simpleSpec()); len(issues) != 0 {
		t.Fatalf("expected a valid graph to pass, got %d issue(s): %v", len(issues), issues)
	}
}

func TestValidatorRejectsCycle(t *testing.T) {
	t.Parallel()

	spec := simpleSpec()
	// Close the loop: load now feeds extract, so no task can ever start.
	spec.Dependencies = append(spec.Dependencies,
		Dependency{UpstreamKey: "load", DownstreamKey: "extract"})

	issues := NewValidator(nil).Validate(spec)
	if len(issues) == 0 {
		t.Fatal("expected a cycle to be rejected")
	}

	assertIssueMentions(t, issues, "cycle")
}

func TestValidatorRejectsDependencyOnUnknownTask(t *testing.T) {
	t.Parallel()

	spec := simpleSpec()
	spec.Dependencies = append(spec.Dependencies,
		Dependency{UpstreamKey: "extract", DownstreamKey: "nowhere"})

	issues := NewValidator(nil).Validate(spec)
	if len(issues) == 0 {
		t.Fatal("expected a dependency on an unknown task to be rejected")
	}
	assertIssueMentions(t, issues, "nowhere")
}

func TestValidatorRejectsSelfDependency(t *testing.T) {
	t.Parallel()

	spec := simpleSpec()
	spec.Dependencies = append(spec.Dependencies,
		Dependency{UpstreamKey: "load", DownstreamKey: "load"})

	issues := NewValidator(nil).Validate(spec)
	if len(issues) == 0 {
		t.Fatal("expected a self-dependency to be rejected")
	}
}

func TestValidatorRejectsDuplicateTaskKey(t *testing.T) {
	t.Parallel()

	spec := simpleSpec()
	spec.Tasks = append(spec.Tasks, task("extract", "python",
		map[string]any{"python_callable": "extract_orders_again"}))

	issues := NewValidator(nil).Validate(spec)
	if len(issues) == 0 {
		t.Fatal("expected a duplicate task key to be rejected")
	}
	assertIssueMentions(t, issues, "extract")
}

func TestValidatorRejectsMalformedTaskKey(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"Extract", "extract-orders", "_extract", "extract_", "extract orders"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			spec := simpleSpec()
			spec.Tasks[0].TaskKey = key

			if issues := NewValidator(nil).ValidateGraph(spec); len(issues) == 0 {
				t.Fatalf("expected task key %q to be rejected", key)
			}
		})
	}
}

func TestValidatorRejectsUnknownOperator(t *testing.T) {
	t.Parallel()

	spec := simpleSpec()
	spec.Tasks[0].OperatorType = "teleport"

	issues := NewValidator(nil).Validate(spec)
	if len(issues) == 0 {
		t.Fatal("expected an unknown operator to be rejected")
	}
}

func TestValidatorRejectsMissingOperatorConfig(t *testing.T) {
	t.Parallel()

	spec := simpleSpec()
	// A python task without a callable cannot be rendered into a DAG.
	spec.Tasks[0].OperatorConfig = map[string]any{}

	issues := NewValidator(nil).Validate(spec)
	if len(issues) == 0 {
		t.Fatal("expected a python task with no callable to be rejected")
	}
}

func TestValidatorRejectsInvalidCron(t *testing.T) {
	t.Parallel()

	spec := simpleSpec()
	spec.Schedule = "not a cron expression"

	if issues := NewValidator(nil).Validate(simpleScheduleSpec(spec)); len(issues) == 0 {
		t.Fatal("expected an invalid schedule to be rejected")
	}
}

func TestValidatorAcceptsValidCron(t *testing.T) {
	t.Parallel()

	for _, schedule := range []string{"0 2 * * *", "*/15 * * * *", "0 0 1 * *"} {
		t.Run(schedule, func(t *testing.T) {
			t.Parallel()

			spec := simpleSpec()
			spec.Schedule = schedule
			if issues := NewValidator(nil).Validate(spec); len(issues) != 0 {
				t.Fatalf("expected schedule %q to be accepted, got %v", schedule, issues)
			}
		})
	}
}

func TestValidatorRejectsConflictingDAGID(t *testing.T) {
	t.Parallel()

	// edp_orders already belongs to a different pipeline.
	v := NewValidator(map[string]bool{"edp_orders": true})

	spec := simpleSpec()
	spec.DAGID = "edp_orders"

	issues := v.Validate(spec)
	if len(issues) == 0 {
		t.Fatal("expected a DAG id already claimed by another pipeline to be rejected")
	}
}

// simpleScheduleSpec exists so the schedule test reads as one line rather than
// an inline literal, keeping the cron cases symmetrical.
func simpleScheduleSpec(spec PipelineSpec) PipelineSpec { return spec }

func TestTopologicalOrder(t *testing.T) {
	t.Parallel()

	deps := []Dependency{
		{UpstreamKey: "a", DownstreamKey: "c"},
		{UpstreamKey: "b", DownstreamKey: "c"},
		{UpstreamKey: "c", DownstreamKey: "d"},
	}

	order, ok := TopologicalOrder([]string{"a", "b", "c", "d"}, deps)
	if !ok {
		t.Fatal("expected an acyclic graph to be orderable")
	}
	if len(order) != 4 {
		t.Fatalf("expected 4 tasks in the order, got %d: %v", len(order), order)
	}

	pos := make(map[string]int, len(order))
	for i, key := range order {
		pos[key] = i
	}
	for _, dep := range deps {
		if pos[dep.UpstreamKey] >= pos[dep.DownstreamKey] {
			t.Errorf("%s must precede %s, got %v", dep.UpstreamKey, dep.DownstreamKey, order)
		}
	}
}

func TestTopologicalOrderReportsCycle(t *testing.T) {
	t.Parallel()

	deps := []Dependency{
		{UpstreamKey: "a", DownstreamKey: "b"},
		{UpstreamKey: "b", DownstreamKey: "a"},
	}

	if _, ok := TopologicalOrder([]string{"a", "b"}, deps); ok {
		t.Fatal("expected a cycle to be reported as unorderable")
	}
}

func TestTopologicalOrderHandlesIsolatedTasks(t *testing.T) {
	t.Parallel()

	order, ok := TopologicalOrder([]string{"lonely"}, nil)
	if !ok {
		t.Fatal("expected an empty dependency graph to be orderable")
	}
	if len(order) != 1 || order[0] != "lonely" {
		t.Fatalf("expected the isolated task in the order, got %v", order)
	}
}

func TestSpecProjectsPipelineAndGraph(t *testing.T) {
	t.Parallel()

	now := uuid.New()
	p := &Pipeline{ID: now, Name: "orders", Slug: "orders", DAGID: "edp_orders", TaskCount: 2}

	spec := Spec(p, []Task{task("extract", "python", nil)}, nil)
	if spec.Name != "orders" || spec.DAGID != "edp_orders" {
		t.Fatalf("spec did not project the pipeline: %+v", spec)
	}
	if len(spec.Tasks) != 1 {
		t.Fatalf("expected the supplied tasks, got %d", len(spec.Tasks))
	}
}

func TestHasTag(t *testing.T) {
	t.Parallel()

	p := &Pipeline{Tags: []string{"finance", "gold"}}
	if !p.HasTag("gold") {
		t.Error("expected HasTag to find a present tag")
	}
	if p.HasTag("silver") {
		t.Error("expected HasTag not to find an absent tag")
	}
}

// assertIssueMentions fails unless some issue mentions substr. Issue text is the
// only thing a caller sees, so a change in wording is worth catching.
func assertIssueMentions(t *testing.T, issues []ValidationIssue, substr string) {
	t.Helper()

	for _, issue := range issues {
		if strings.Contains(strings.ToLower(issue.Error()), strings.ToLower(substr)) {
			return
		}
	}

	t.Fatalf("expected an issue mentioning %q, got %v", substr, issues)
}
