package domain

// RuntimeReadinessStatus represents the installation and login status of a runtime.
type RuntimeReadinessStatus string

const (
	RuntimeReadinessReady      RuntimeReadinessStatus = "ready"
	RuntimeReadinessInstalling RuntimeReadinessStatus = "installing"
	RuntimeReadinessMissing    RuntimeReadinessStatus = "missing"
	RuntimeReadinessError      RuntimeReadinessStatus = "error"
)

// TaskRunStatus represents the execution status of a task run.
type TaskRunStatus string

const (
	TaskRunStatusQueued    TaskRunStatus = "queued"
	TaskRunStatusRunning   TaskRunStatus = "running"
	TaskRunStatusCompleted TaskRunStatus = "completed"
	TaskRunStatusFailed    TaskRunStatus = "failed"
)

// TaskWaitingReason represents why a task is currently waiting.
type TaskWaitingReason string

const (
	TaskWaitingReasonBackgroundCommand TaskWaitingReason = "background_command_running"
	TaskWaitingReasonApprovalPending   TaskWaitingReason = "approval_pending"
	TaskWaitingReasonRateLimited       TaskWaitingReason = "rate_limited"
)

// ChangeSetStatus represents the lifecycle of a changeset.
type ChangeSetStatus string

const (
	ChangeSetStatusPending  ChangeSetStatus = "pending"
	ChangeSetStatusAccepted ChangeSetStatus = "accepted"
	ChangeSetStatusRejected ChangeSetStatus = "rejected"
)

// TaskFailureCode represents the reason for a task failure.
type TaskFailureCode string

const (
	TaskFailureTimeout TaskFailureCode = "timeout"
	TaskFailureCrash   TaskFailureCode = "crash"
	TaskFailureDenied  TaskFailureCode = "denied"
	TaskFailureUnknown TaskFailureCode = "unknown"
)

// TaskRun represents a single execution of a task on the daemon.
type TaskRun struct {
	ID            string            `json:"id"`
	SessionID     string            `json:"sessionId"`
	Runtime       string            `json:"runtime"`
	WorkspaceRoot string            `json:"workspaceRoot"`
	Prompt        string            `json:"prompt"`
	Status        TaskRunStatus     `json:"status"`
	FailureCode   TaskFailureCode   `json:"failureCode,omitempty"`
	WaitingReason TaskWaitingReason `json:"waitingReason,omitempty"`
	CreatedAt     string            `json:"createdAt"`
	UpdatedAt     string            `json:"updatedAt"`
}

// ChangeSet represents a collection of file changes in a workspace.
type ChangeSet struct {
	ID        string          `json:"id"`
	TaskRunID string          `json:"taskRunId"`
	Status    ChangeSetStatus `json:"status"`
	Summary   string          `json:"summary"`
	Files     []ChangeSetFile `json:"files"`
	CreatedAt string          `json:"createdAt"`
}

// ChangeSetFile represents a single file change within a changeset.
type ChangeSetFile struct {
	Path   string `json:"path"`
	Status string `json:"status"` // added, modified, deleted
	Diff   string `json:"diff,omitempty"`
}
