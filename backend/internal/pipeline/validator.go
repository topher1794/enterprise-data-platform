package pipeline

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ValidationIssue describes one problem found in a pipeline definition.
type ValidationIssue struct {
	// Field is the JSON path of the offending element, e.g. "tasks[2].task_key".
	Field string `json:"field"`
	// Code is a stable identifier for the rule that failed.
	Code string `json:"code"`
	// Message explains the problem in terms the author can act on.
	Message string `json:"message"`
	// TaskKey names the specific task, when the issue concerns one.
	TaskKey string `json:"task_key,omitempty"`
}

// Error implements error so a ValidationError can flow through the normal error
// path.
func (i ValidationIssue) Error() string {
	return fmt.Sprintf("%s: %s", i.Field, i.Message)
}

// Validation codes, referenced by tests and by clients that branch on failures.
const (
	CodeNoTasks             = "no_tasks"
	CodeDuplicateTaskKey    = "duplicate_task_key"
	CodeInvalidTaskKey      = "invalid_task_key"
	CodeUnknownDependency   = "unknown_dependency"
	CodeSelfDependency      = "self_dependency"
	CodeCycle               = "cycle_detected"
	CodeNoRoot              = "no_root_task"
	CodeDuplicateDAGID      = "duplicate_dag_id"
	CodeInvalidSchedule     = "invalid_schedule"
	CodeMissingDataset      = "missing_dataset"
	CodeUnknownOperator     = "unknown_operator"
	CodeTooManyTasks        = "too_many_tasks"
	CodeInvalidOperatorConf = "invalid_operator_config"
	CodeSLAWithoutOutput    = "sla_without_output"
)

// taskKeyPattern mirrors the `pipeline_tasks_key_format` CHECK constraint.
var taskKeyPattern = regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9]+)*$`)

// maxTasksPerPipeline bounds a single pipeline so one request cannot build a
// graph large enough to make validation expensive.
const maxTasksPerPipeline = 500

// maxGraphDepth bounds the traversal so a pathological chain cannot exhaust the
// stack. Real pipelines are far shallower than this.
const maxGraphDepth = 10000

// supportedOperators are the executor types this control plane can deploy.
// Airflow may support more; unknown operators are rejected at authoring time so
// a pipeline cannot be saved into a state that will fail on first run.
var supportedOperators = map[string]bool{
	"python":             true,
	"sql":                true,
	"bash":               true,
	"spark":              true,
	"http_poll":          true,
	"great_expectations": true,
	"dbt":                true,
	"container":          true,
	"kubernetes_pod":     true,
}

// requiredOperatorConfig lists the config keys each operator needs, so an
// incomplete task is rejected before deployment rather than at run time.
//
// Each entry is a list of alternatives: supplying one key from a group
// satisfies it. That distinction matters for operators like python, where a
// task either names a callable or points at a file, never both.
var requiredOperatorConfig = map[string][][]string{
	"python":             {{"python_callable", "file_path"}},
	"sql":                {{"sql"}},
	"bash":               {{"bash_command"}},
	"spark":              {{"application"}},
	"http_poll":          {{"endpoint"}},
	"great_expectations": {{"checkpoint_name"}, {"dataset"}},
	"dbt":                {},
	"container":          {{"image"}},
	"kubernetes_pod":     {{"image"}},
}

// Validator checks a pipeline definition for structural correctness before it is
// persisted. It is stateless and safe for concurrent use.
type Validator struct {
	// knownDAGIDs lets the validator flag a DAG identifier already claimed by
	// another pipeline. Pass nil to skip that check.
	knownDAGIDs map[string]bool
}

// NewValidator builds a pipeline validator. knownDAGIDs is the set of DAG
// identifiers in use by other pipelines; it may be nil.
func NewValidator(knownDAGIDs map[string]bool) *Validator {
	set := make(map[string]bool, len(knownDAGIDs))
	for id := range knownDAGIDs {
		set[id] = true
	}
	return &Validator{knownDAGIDs: set}
}

// ValidateGraph checks a task set and its dependency edges. It returns every
// issue it finds rather than stopping at the first, so an author can fix a
// whole definition in one pass.
//
// Issues are ordered: structural problems first, then referential ones, then
// graph-topology ones.
func (v *Validator) ValidateGraph(spec PipelineSpec) []ValidationIssue {
	var issues []ValidationIssue

	issues = append(issues, v.validateTaskShape(spec)...)
	issues = append(issues, v.validateDependencies(spec)...)
	issues = append(issues, v.validateTopology(spec)...)

	return issues
}

// validateTaskShape checks each task in isolation.
func (v *Validator) validateTaskShape(spec PipelineSpec) []ValidationIssue {
	var issues []ValidationIssue

	if len(spec.Tasks) == 0 {
		return append(issues, ValidationIssue{
			Field:   "tasks",
			Code:    CodeNoTasks,
			Message: "a pipeline must declare at least one task",
		})
	}
	if len(spec.Tasks) > maxTasksPerPipeline {
		issues = append(issues, ValidationIssue{
			Field: "tasks",
			Code:  CodeTooManyTasks,
			Message: fmt.Sprintf("a pipeline may declare at most %d tasks, got %d",
				maxTasksPerPipeline, len(spec.Tasks)),
		})
		// Validating the structure of a graph this large would be wasteful; the
		// size failure above is the actionable message.
		return issues
	}

	seen := make(map[string]int, len(spec.Tasks))

	for i, task := range spec.Tasks {
		field := fmt.Sprintf("tasks[%d]", i)

		switch {
		case strings.TrimSpace(task.TaskKey) == "":
			issues = append(issues, ValidationIssue{
				Field:   field + ".task_key",
				Code:    CodeInvalidTaskKey,
				Message: "task_key is required",
			})
		case !taskKeyPattern.MatchString(task.TaskKey):
			issues = append(issues, ValidationIssue{
				Field:   field + ".task_key",
				Code:    CodeInvalidTaskKey,
				TaskKey: task.TaskKey,
				Message: fmt.Sprintf(
					"task_key %q must be lowercase alphanumeric words separated by single underscores",
					task.TaskKey),
			})
		}

		if first, dup := seen[task.TaskKey]; dup && task.TaskKey != "" {
			issues = append(issues, ValidationIssue{
				Field:   field + ".task_key",
				Code:    CodeDuplicateTaskKey,
				TaskKey: task.TaskKey,
				Message: fmt.Sprintf("task_key %q is already used by tasks[%d]", task.TaskKey, first),
			})
		} else if task.TaskKey != "" {
			seen[task.TaskKey] = i
		}

		if !supportedOperators[task.OperatorType] {
			issues = append(issues, ValidationIssue{
				Field:   field + ".operator_type",
				Code:    CodeUnknownOperator,
				TaskKey: task.TaskKey,
				Message: fmt.Sprintf("operator_type %q is not supported; supported: %s",
					task.OperatorType, strings.Join(sortedOperators(), ", ")),
			})
		} else if missing := missingConfig(task); len(missing) > 0 {
			issues = append(issues, ValidationIssue{
				Field:   field + ".operator_config",
				Code:    CodeInvalidOperatorConf,
				TaskKey: task.TaskKey,
				Message: fmt.Sprintf("operator %q requires %s", task.OperatorType,
					strings.Join(missing, ", ")),
			})
		}

		if task.TimeoutSeconds <= 0 {
			issues = append(issues, ValidationIssue{
				Field:   field + ".timeout_seconds",
				Code:    CodeInvalidOperatorConf,
				TaskKey: task.TaskKey,
				Message: "timeout_seconds must be greater than zero",
			})
		}

		if task.SLAMinutes != nil && task.DownstreamDatasetID == nil {
			issues = append(issues, ValidationIssue{
				Field:   field + ".sla_minutes",
				Code:    CodeSLAWithoutOutput,
				TaskKey: task.TaskKey,
				Message: "sla_minutes requires the task to declare a downstream_dataset_id",
			})
		}
	}

	return issues
}

// validateDependencies checks that every edge endpoint resolves to a declared
// task.
func (v *Validator) validateDependencies(spec PipelineSpec) []ValidationIssue {
	var issues []ValidationIssue

	declared := make(map[string]struct{}, len(spec.Tasks))
	for _, task := range spec.Tasks {
		declared[task.TaskKey] = struct{}{}
	}

	seenEdges := make(map[string]struct{}, len(spec.Dependencies))

	for i, dep := range spec.Dependencies {
		field := fmt.Sprintf("dependencies[%d]", i)

		if dep.UpstreamKey == dep.DownstreamKey {
			issues = append(issues, ValidationIssue{
				Field:   field,
				Code:    CodeSelfDependency,
				TaskKey: dep.UpstreamKey,
				Message: fmt.Sprintf("task %q cannot depend on itself", dep.UpstreamKey),
			})
			continue
		}

		if _, ok := declared[dep.UpstreamKey]; !ok {
			issues = append(issues, ValidationIssue{
				Field:   field + ".upstream_key",
				Code:    CodeUnknownDependency,
				TaskKey: dep.UpstreamKey,
				Message: fmt.Sprintf("upstream_key %q does not match any declared task", dep.UpstreamKey),
			})
		}
		if _, ok := declared[dep.DownstreamKey]; !ok {
			issues = append(issues, ValidationIssue{
				Field:   field + ".downstream_key",
				Code:    CodeUnknownDependency,
				TaskKey: dep.DownstreamKey,
				Message: fmt.Sprintf("downstream_key %q does not match any declared task", dep.DownstreamKey),
			})
		}

		edge := dep.UpstreamKey + "\x00" + dep.DownstreamKey
		if _, dup := seenEdges[edge]; dup {
			issues = append(issues, ValidationIssue{
				Field:   field,
				Code:    CodeDuplicateTaskKey,
				TaskKey: dep.DownstreamKey,
				Message: fmt.Sprintf("dependency %s -> %s is declared more than once",
					dep.UpstreamKey, dep.DownstreamKey),
			})
			continue
		}
		seenEdges[edge] = struct{}{}
	}

	return issues
}

// validateTopology checks that the dependency graph is a DAG with at least one
// entry point, and returns a topological order for the valid edges.
func (v *Validator) validateTopology(spec PipelineSpec) []ValidationIssue {
	var issues []ValidationIssue

	// Build adjacency from the edges that resolve to declared tasks. Edges with
	// a missing endpoint were already reported; including them would produce
	// misleading cycle diagnostics.
	nodes := make([]string, 0, len(spec.Tasks))
	adjacency := make(map[string][]string, len(spec.Tasks))
	inDegree := make(map[string]int, len(spec.Tasks))

	declared := make(map[string]struct{}, len(spec.Tasks))
	for _, task := range spec.Tasks {
		if task.TaskKey != "" {
			nodes = append(nodes, task.TaskKey)
			declared[task.TaskKey] = struct{}{}
			inDegree[task.TaskKey] = 0
		}
	}

	for _, dep := range spec.Dependencies {
		_, upOK := declared[dep.UpstreamKey]
		_, downOK := declared[dep.DownstreamKey]
		if !upOK || !downOK || dep.UpstreamKey == dep.DownstreamKey {
			continue
		}
		adjacency[dep.UpstreamKey] = append(adjacency[dep.UpstreamKey], dep.DownstreamKey)
		inDegree[dep.DownstreamKey]++
	}

	// Deterministic ordering makes both the cycle report and the task order
	// reproducible, which matters for tests and for the graph hash.
	sort.Strings(nodes)

	// Kahn's algorithm. The queue is kept sorted so the emitted order is stable.
	queue := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if inDegree[node] == 0 {
			queue = append(queue, node)
		}
	}

	ordered := make([]string, 0, len(nodes))
	for len(queue) > 0 {
		sort.Strings(queue)
		current := queue[0]
		queue = queue[1:]
		ordered = append(ordered, current)

		if len(ordered) > maxGraphDepth {
			issues = append(issues, ValidationIssue{
				Field: "dependencies",
				Code:  CodeCycle,
				Message: fmt.Sprintf("the dependency graph exceeds the maximum depth of %d",
					maxGraphDepth),
			})
			return issues
		}

		for _, next := range adjacency[current] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	if len(ordered) < len(nodes) {
		issues = append(issues, ValidationIssue{
			Field: "dependencies",
			Code:  CodeCycle,
			Message: fmt.Sprintf(
				"the dependency graph contains a cycle: %s cannot be ordered before the rest",
				strings.Join(cycleMembers(nodes, ordered), ", ")),
		})
		return issues
	}

	if len(ordered) == 0 {
		issues = append(issues, ValidationIssue{
			Field:   "tasks",
			Code:    CodeNoRoot,
			Message: "every task depends on another task, so the pipeline has no entry point",
		})
	}

	return issues
}

// cycleMembers returns the nodes Kahn's algorithm could not reach, which are
// exactly the nodes involved in, or downstream of, a cycle.
func cycleMembers(all []string, ordered []string) []string {
	reached := make(map[string]struct{}, len(ordered))
	for _, node := range ordered {
		reached[node] = struct{}{}
	}

	var remaining []string
	for _, node := range all {
		if _, ok := reached[node]; !ok {
			remaining = append(remaining, node)
		}
	}
	return remaining
}

// ValidateMetadata checks the pipeline-level fields.
func (v *Validator) ValidateMetadata(spec PipelineSpec) []ValidationIssue {
	var issues []ValidationIssue

	if spec.DAGID != "" && v.knownDAGIDs != nil && v.knownDAGIDs[spec.DAGID] {
		issues = append(issues, ValidationIssue{
			Field:   "dag_id",
			Code:    CodeDuplicateDAGID,
			Message: fmt.Sprintf("dag_id %q is already deployed by another pipeline", spec.DAGID),
		})
	}

	if spec.Schedule != "" {
		if err := validateCron(spec.Schedule); err != nil {
			issues = append(issues, ValidationIssue{
				Field:   "schedule",
				Code:    CodeInvalidSchedule,
				Message: err.Error(),
			})
		}
	}

	if spec.MaxActiveRuns < 1 {
		issues = append(issues, ValidationIssue{
			Field:   "max_active_runs",
			Code:    CodeInvalidOperatorConf,
			Message: "max_active_runs must be at least 1",
		})
	}

	return issues
}

// Validate runs every check and returns all issues.
func (v *Validator) Validate(spec PipelineSpec) []ValidationIssue {
	issues := v.ValidateMetadata(spec)
	return append(issues, v.ValidateGraph(spec)...)
}

// TopologicalOrder returns a stable execution order for the given task keys and
// edges. It assumes the graph has already been validated as acyclic; on a
// cyclic graph the order is incomplete, which callers detect via
// order.Complete(keys).
func TopologicalOrder(taskKeys []string, deps []Dependency) ([]string, bool) {
	nodes := make([]string, 0, len(taskKeys))
	declared := make(map[string]struct{}, len(taskKeys))
	for _, key := range taskKeys {
		nodes = append(nodes, key)
		declared[key] = struct{}{}
	}
	sort.Strings(nodes)

	adjacency := make(map[string][]string, len(nodes))
	inDegree := make(map[string]int, len(nodes))
	for _, node := range nodes {
		inDegree[node] = 0
	}

	for _, dep := range deps {
		_, upOK := declared[dep.UpstreamKey]
		_, downOK := declared[dep.DownstreamKey]
		if !upOK || !downOK || dep.UpstreamKey == dep.DownstreamKey {
			continue
		}
		adjacency[dep.UpstreamKey] = append(adjacency[dep.UpstreamKey], dep.DownstreamKey)
		inDegree[dep.DownstreamKey]++
	}

	queue := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if inDegree[node] == 0 {
			queue = append(queue, node)
		}
	}

	ordered := make([]string, 0, len(nodes))
	for len(queue) > 0 {
		sort.Strings(queue)
		current := queue[0]
		queue = queue[1:]
		ordered = append(ordered, current)

		for _, next := range adjacency[current] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	return ordered, len(ordered) == len(nodes)
}

// missingConfig reports which required operator_config keys are absent.
func missingConfig(task Task) []string {
	groups, ok := requiredOperatorConfig[task.OperatorType]
	if !ok || len(groups) == 0 {
		return nil
	}

	var missing []string
	for _, alternatives := range groups {
		// One satisfied alternative is enough. When every option in a group is
		// absent, report all of them so the author can pick one deliberately
		// rather than discovering the choice by trial and error.
		satisfied := false
		for _, key := range alternatives {
			if _, present := task.OperatorConfig[key]; present {
				satisfied = true
				break
			}
		}
		if !satisfied {
			missing = append(missing, strings.Join(alternatives, " or "))
		}
	}
	return missing
}

func sortedOperators() []string {
	out := make([]string, 0, len(supportedOperators))
	for op := range supportedOperators {
		out = append(out, op)
	}
	sort.Strings(out)
	return out
}

// validateCron checks the shape of a five-field cron expression. It is a
// structural check, not a full parser: the goal is to catch typos at authoring
// time, while the orchestrator remains the authority on scheduling semantics.
func validateCron(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf(
			"schedule %q must have exactly 5 cron fields (minute hour day-of-month month day-of-week), got %d",
			expr, len(fields))
	}

	allowed := map[byte]bool{
		'0': true, '1': true, '2': true, '3': true, '4': true,
		'5': true, '6': true, '7': true, '8': true, '9': true,
		'*': true, ',': true, '-': true, '/': true,
	}

	for _, field := range fields {
		for i := 0; i < len(field); i++ {
			if !allowed[field[i]] {
				return fmt.Errorf(
					"schedule %q contains the invalid character %q; only digits, '*', ',', '-' and '/' are allowed",
					expr, string(field[i]))
			}
		}
	}

	return nil
}
