package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	MaxTaskTemplateNameRunes        = 120
	MaxTaskTemplateDescriptionRunes = 20000
	MaxTaskTemplateTextRunes        = 16000
	MaxTaskTemplateSeconds          = 2147483647 // PostgreSQL INTEGER upper bound.
)

var (
	ErrTaskTemplateInvalid      = errors.New("invalid task template")
	ErrTaskTemplateNameConflict = errors.New("task template name already exists")
	ErrTaskTemplateNotFound     = errors.New("task template not found")
)

// TaskTemplate is a reusable task preset (description/goal + optional category
// and task-level intercept/allow rules).
type TaskTemplate struct {
	TaskName             *string                  `json:"task_name"`
	TimeoutSeconds       *int                     `json:"timeout_seconds"`
	PlanHeartbeatSeconds *int                     `json:"plan_heartbeat_seconds"`
	SeedFirstIntent      *bool                    `json:"seed_first_intent"`
	CoverageEnabled      *bool                    `json:"coverage_enabled"`
	ID                   int64                    `json:"id"`
	Name                 string                   `json:"name"`
	NKey                 string                   `json:"-"`
	Description          string                   `json:"description"`
	Goal                 string                   `json:"goal"`
	CategoryID           *int64                   `json:"category_id"`
	InterceptRules       []TaskInterceptRuleInput `json:"intercept_rules"`
	CreatedAt            time.Time                `json:"created_at"`
	UpdatedAt            time.Time                `json:"updated_at"`
}

// TaskTemplateInput is the create/update payload after normalization.
type TaskTemplateInput struct {
	TaskName             *string
	TimeoutSeconds       *int
	PlanHeartbeatSeconds *int
	SeedFirstIntent      *bool
	CoverageEnabled      *bool
	Name                 string
	Description          string
	Goal                 string
	CategoryID           *int64
	InterceptRules       []TaskInterceptRuleInput
}

// TaskTemplatePatch changes only fields flagged as set. Name/Description/Goal use
// non-nil pointers; CategoryID/InterceptRules use explicit Set flags (so a nil
// CategoryID can mean "clear" when SetCategoryID is true).
type TaskTemplatePatch struct {
	TaskName                *string
	SetTaskName             bool
	TimeoutSeconds          *int
	SetTimeoutSeconds       bool
	PlanHeartbeatSeconds    *int
	SetPlanHeartbeatSeconds bool
	SeedFirstIntent         *bool
	SetSeedFirstIntent      bool
	CoverageEnabled         *bool
	SetCoverageEnabled      bool
	Name                    *string
	Description             *string
	Goal                    *string
	CategoryID              *int64
	SetCategoryID           bool
	InterceptRules          []TaskInterceptRuleInput
	SetInterceptRules       bool
}

const taskTemplateCols = `id, name, nkey, description, goal, category_id, intercept_rules, created_at, updated_at, task_name, timeout_seconds, plan_heartbeat_seconds, seed_first_intent, coverage_enabled`

func scanTaskTemplate(row interface{ Scan(...any) error }) (TaskTemplate, error) {
	var t TaskTemplate
	var rulesRaw []byte
	if err := row.Scan(&t.ID, &t.Name, &t.NKey, &t.Description, &t.Goal, &t.CategoryID, &rulesRaw, &t.CreatedAt, &t.UpdatedAt, &t.TaskName, &t.TimeoutSeconds, &t.PlanHeartbeatSeconds, &t.SeedFirstIntent, &t.CoverageEnabled); err != nil {
		return t, err
	}
	t.InterceptRules = []TaskInterceptRuleInput{}
	if len(rulesRaw) > 0 {
		if err := json.Unmarshal(rulesRaw, &t.InterceptRules); err != nil {
			return t, err
		}
		if t.InterceptRules == nil {
			t.InterceptRules = []TaskInterceptRuleInput{}
		}
	}
	return t, nil
}

// marshalTemplateRules serializes a template's rule snapshot to JSONB text,
// always producing a JSON array (never null).
func marshalTemplateRules(rules []TaskInterceptRuleInput) ([]byte, error) {
	if rules == nil {
		rules = []TaskInterceptRuleInput{}
	}
	return json.Marshal(rules)
}

// taskTemplateName normalizes display whitespace while preserving the user's case.
func taskTemplateName(name string) string { return strings.Join(strings.Fields(name), " ") }

// taskTemplateNKey is the case-insensitive identity used by the unique index.
func taskTemplateNKey(name string) string { return strings.ToLower(taskTemplateName(name)) }

func normalizeTaskTemplateInput(in TaskTemplateInput) (TaskTemplateInput, string, error) {
	if err := normalizeTemplateExecution(&in.TaskName, in.TimeoutSeconds, &in.PlanHeartbeatSeconds); err != nil {
		return in, "", err
	}
	in.Name = taskTemplateName(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.Goal = strings.TrimSpace(in.Goal)
	switch {
	case in.Name == "":
		return in, "", fmt.Errorf("%w: name is required", ErrTaskTemplateInvalid)
	case utf8.RuneCountInString(in.Name) > MaxTaskTemplateNameRunes:
		return in, "", fmt.Errorf("%w: name exceeds %d characters", ErrTaskTemplateInvalid, MaxTaskTemplateNameRunes)
	case in.Description == "":
		return in, "", fmt.Errorf("%w: description is required", ErrTaskTemplateInvalid)
	case utf8.RuneCountInString(in.Description) > MaxTaskTemplateDescriptionRunes:
		return in, "", fmt.Errorf("%w: description exceeds %d characters", ErrTaskTemplateInvalid, MaxTaskTemplateDescriptionRunes)
	case in.Goal == "":
		return in, "", fmt.Errorf("%w: goal is required", ErrTaskTemplateInvalid)
	case utf8.RuneCountInString(in.Goal) > MaxTaskTemplateTextRunes:
		return in, "", fmt.Errorf("%w: goal exceeds %d characters", ErrTaskTemplateInvalid, MaxTaskTemplateTextRunes)
	}
	return in, taskTemplateNKey(in.Name), nil
}

func normalizeTaskTemplatePatch(patch TaskTemplatePatch) (TaskTemplatePatch, *string, error) {
	if patch.Name == nil && patch.Description == nil && patch.Goal == nil && !patch.SetTaskName && !patch.SetTimeoutSeconds && !patch.SetPlanHeartbeatSeconds && !patch.SetSeedFirstIntent && !patch.SetCoverageEnabled && !patch.SetCategoryID && !patch.SetInterceptRules {
		return patch, nil, fmt.Errorf("%w: no fields supplied", ErrTaskTemplateInvalid)
	}
	if err := normalizeTemplateExecution(&patch.TaskName, patch.TimeoutSeconds, &patch.PlanHeartbeatSeconds); err != nil {
		return patch, nil, err
	}
	var nkey *string
	if patch.Name != nil {
		name := taskTemplateName(*patch.Name)
		if name == "" {
			return patch, nil, fmt.Errorf("%w: name is required", ErrTaskTemplateInvalid)
		}
		if utf8.RuneCountInString(name) > MaxTaskTemplateNameRunes {
			return patch, nil, fmt.Errorf("%w: name exceeds %d characters", ErrTaskTemplateInvalid, MaxTaskTemplateNameRunes)
		}
		key := taskTemplateNKey(name)
		patch.Name = &name
		nkey = &key
	}
	if patch.Description != nil {
		description := strings.TrimSpace(*patch.Description)
		if description == "" {
			return patch, nil, fmt.Errorf("%w: description is required", ErrTaskTemplateInvalid)
		}
		if utf8.RuneCountInString(description) > MaxTaskTemplateDescriptionRunes {
			return patch, nil, fmt.Errorf("%w: description exceeds %d characters", ErrTaskTemplateInvalid, MaxTaskTemplateDescriptionRunes)
		}
		patch.Description = &description
	}
	if patch.Goal != nil {
		goal := strings.TrimSpace(*patch.Goal)
		if goal == "" {
			return patch, nil, fmt.Errorf("%w: goal is required", ErrTaskTemplateInvalid)
		}
		if utf8.RuneCountInString(goal) > MaxTaskTemplateTextRunes {
			return patch, nil, fmt.Errorf("%w: goal exceeds %d characters", ErrTaskTemplateInvalid, MaxTaskTemplateTextRunes)
		}
		patch.Goal = &goal
	}
	return patch, nkey, nil
}

func taskTemplateUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// CreateTaskTemplate inserts one globally reusable preset.
func (d *DB) CreateTaskTemplate(in TaskTemplateInput) (*TaskTemplate, error) {
	return insertTaskTemplate(d, in)
}

func insertTaskTemplate(q interface{ QueryRow(string, ...any) *sql.Row }, in TaskTemplateInput) (*TaskTemplate, error) {
	in, nkey, err := normalizeTaskTemplateInput(in)
	if err != nil {
		return nil, err
	}
	rulesJSON, err := marshalTemplateRules(in.InterceptRules)
	if err != nil {
		return nil, err
	}
	t, err := scanTaskTemplate(q.QueryRow(`
INSERT INTO task_templates(name, nkey, description, goal, category_id, intercept_rules, task_name, timeout_seconds, plan_heartbeat_seconds, seed_first_intent, coverage_enabled)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (nkey) DO NOTHING
RETURNING `+taskTemplateCols, in.Name, nkey, in.Description, in.Goal, in.CategoryID, rulesJSON, in.TaskName, in.TimeoutSeconds, in.PlanHeartbeatSeconds, in.SeedFirstIntent, in.CoverageEnabled))
	if err == sql.ErrNoRows {
		return nil, ErrTaskTemplateNameConflict
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListTaskTemplates returns the most recently maintained templates first.
func (d *DB) ListTaskTemplates() ([]*TaskTemplate, error) {
	rows, err := d.Query(`SELECT ` + taskTemplateCols + ` FROM task_templates ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TaskTemplate{}
	for rows.Next() {
		t, err := scanTaskTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// GetTaskTemplate returns nil when id does not exist.
func (d *DB) GetTaskTemplate(id int64) (*TaskTemplate, error) {
	t, err := scanTaskTemplate(d.QueryRow(`SELECT `+taskTemplateCols+` FROM task_templates WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// UpdateTaskTemplate replaces the editable fields of one preset.
func (d *DB) UpdateTaskTemplate(id int64, in TaskTemplateInput) (*TaskTemplate, error) {
	in, _, err := normalizeTaskTemplateInput(in)
	if err != nil {
		return nil, err
	}
	return d.PatchTaskTemplate(id, TaskTemplatePatch{
		Name: &in.Name, Description: &in.Description, Goal: &in.Goal,
	})
}

// PatchTaskTemplate atomically changes only the supplied fields. Keeping the
// merge in one UPDATE prevents concurrent disjoint PATCH requests from losing
// each other's changes.
func (d *DB) PatchTaskTemplate(id int64, patch TaskTemplatePatch) (*TaskTemplate, error) {
	patch, nkey, err := normalizeTaskTemplatePatch(patch)
	if err != nil {
		return nil, err
	}
	rulesJSON, err := marshalTemplateRules(patch.InterceptRules)
	if err != nil {
		return nil, err
	}
	t, err := scanTaskTemplate(d.QueryRow(`UPDATE task_templates
SET name=CASE WHEN $2 THEN $3::text ELSE name END,
    nkey=CASE WHEN $2 THEN $4::text ELSE nkey END,
    description=CASE WHEN $5 THEN $6::text ELSE description END,
    goal=CASE WHEN $7 THEN $8::text ELSE goal END,
    category_id=CASE WHEN $9 THEN $10::bigint ELSE category_id END,
    intercept_rules=CASE WHEN $11 THEN $12::jsonb ELSE intercept_rules END,
    task_name=CASE WHEN $13 THEN $14::text ELSE task_name END,
    timeout_seconds=CASE WHEN $15 THEN $16::integer ELSE timeout_seconds END,
    plan_heartbeat_seconds=CASE WHEN $17 THEN $18::integer ELSE plan_heartbeat_seconds END,
    seed_first_intent=CASE WHEN $19 THEN $20::boolean ELSE seed_first_intent END,
    coverage_enabled=CASE WHEN $21 THEN $22::boolean ELSE coverage_enabled END
WHERE id=$1
RETURNING `+taskTemplateCols,
		id,
		patch.Name != nil, patch.Name, nkey,
		patch.Description != nil, patch.Description,
		patch.Goal != nil, patch.Goal,
		patch.SetCategoryID, patch.CategoryID,
		patch.SetInterceptRules, rulesJSON,
		patch.SetTaskName, patch.TaskName,
		patch.SetTimeoutSeconds, patch.TimeoutSeconds,
		patch.SetPlanHeartbeatSeconds, patch.PlanHeartbeatSeconds,
		patch.SetSeedFirstIntent, patch.SeedFirstIntent,
		patch.SetCoverageEnabled, patch.CoverageEnabled,
	))
	if err == sql.ErrNoRows {
		return nil, ErrTaskTemplateNotFound
	}
	if taskTemplateUniqueViolation(err) {
		return nil, ErrTaskTemplateNameConflict
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// DeleteTaskTemplate deletes one preset and reports whether it existed.
func (d *DB) DeleteTaskTemplate(id int64) (bool, error) {
	result, err := d.Exec(`DELETE FROM task_templates WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// Nullable execution fields leave existing task form values alone when applied.
// Explicit zero timeout and false toggles remain real values, not omissions.
func normalizeTemplateExecution(taskName **string, timeout *int, heartbeat **int) error {
	if *taskName != nil {
		name := strings.TrimSpace(**taskName)
		if utf8.RuneCountInString(name) > MaxTaskTemplateNameRunes {
			return fmt.Errorf("%w: task_name exceeds %d characters", ErrTaskTemplateInvalid, MaxTaskTemplateNameRunes)
		}
		*taskName = &name
	}
	if timeout != nil && (*timeout < 0 || *timeout > MaxTaskTemplateSeconds) {
		return fmt.Errorf("%w: timeout_seconds must be between 0 and %d", ErrTaskTemplateInvalid, MaxTaskTemplateSeconds)
	}
	if *heartbeat != nil {
		if **heartbeat < 0 || **heartbeat > MaxTaskTemplateSeconds {
			return fmt.Errorf("%w: plan_heartbeat_seconds must be between 0 and %d", ErrTaskTemplateInvalid, MaxTaskTemplateSeconds)
		}
		seconds := normalizeHeartbeat(**heartbeat)
		*heartbeat = &seconds
	}
	return nil
}
