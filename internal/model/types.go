package model

import (
	"encoding/json"
	"github.com/aws/aws-sdk-go-v2/aws"
)

const (
	Version            = "0.1.6"
	DefaultBaseURL     = "https://api.openai.com/v1"
	LocalBaseURL       = "http://127.0.0.1:8080/v1"
	DefaultBedrock     = "bedrock:global.anthropic.claude-opus-4-6-v1"
	BedrockMaxTokens   = int32(16000)
	BedrockThinkBudget = 4000
)

type PermissionRuleEffect string

const (
	AllowAlways PermissionRuleEffect = "allow_always"
	AutoReview  PermissionRuleEffect = "auto_review"
)

type NetworkRuleAction string

const (
	NetworkAllow NetworkRuleAction = "Allow"
	NetworkDeny  NetworkRuleAction = "Deny"
)

type CollaborationMode string

const (
	ModeDefault CollaborationMode = "Default"
	ModePlan    CollaborationMode = "Plan"
)

type CommandApprovalRule struct {
	Prefix []string              `toml:"prefix" json:"prefix"`
	Effect *PermissionRuleEffect `toml:"effect,omitempty" json:"effect,omitempty"`
}

type NetworkApprovalRule struct {
	Action   NetworkRuleAction `toml:"action" json:"action"`
	Protocol string            `toml:"protocol" json:"protocol"`
	Host     string            `toml:"host" json:"host"`
}

type ProjectTrustProfile struct {
	PermissionMode       *string               `toml:"permission_mode,omitempty" json:"permission_mode,omitempty"`
	ReadRoots            []string              `toml:"read_roots,omitempty" json:"read_roots,omitempty"`
	WritableRoots        []string              `toml:"writable_roots,omitempty" json:"writable_roots,omitempty"`
	ShellApprovalMode    *string               `toml:"shell_approval_mode,omitempty" json:"shell_approval_mode,omitempty"`
	ShellNetworkPolicy   *string               `toml:"shell_network_policy,omitempty" json:"shell_network_policy,omitempty"`
	SandboxMode          *string               `toml:"sandbox_mode,omitempty" json:"sandbox_mode,omitempty"`
	NetworkApprovalRules []NetworkApprovalRule `toml:"network_approval_rules,omitempty" json:"network_approval_rules,omitempty"`
}

type Config struct {
	Registration         *Registration                  `toml:"registration,omitempty" json:"registration,omitempty"`
	APIKey               string                         `toml:"api_key" json:"api_key"`
	BaseURL              *string                        `toml:"base_url,omitempty" json:"base_url,omitempty"`
	AWSProfile           *string                        `toml:"aws_profile,omitempty" json:"aws_profile,omitempty"`
	AWSAccessKeyID       *string                        `toml:"aws_access_key_id,omitempty" json:"aws_access_key_id,omitempty"`
	AWSSecretAccessKey   *string                        `toml:"aws_secret_access_key,omitempty" json:"aws_secret_access_key,omitempty"`
	AWSSessionToken      *string                        `toml:"aws_session_token,omitempty" json:"aws_session_token,omitempty"`
	AWSRegion            *string                        `toml:"aws_region,omitempty" json:"aws_region,omitempty"`
	BedrockModel         *string                        `toml:"bedrock_model,omitempty" json:"bedrock_model,omitempty"`
	InstallationID       *string                        `toml:"installation_id,omitempty" json:"installation_id,omitempty"`
	WritableRoots        []string                       `toml:"writable_roots,omitempty" json:"writable_roots,omitempty"`
	ShellApprovalMode    *string                        `toml:"shell_approval_mode,omitempty" json:"shell_approval_mode,omitempty"`
	ShellNetworkPolicy   *string                        `toml:"shell_network_policy,omitempty" json:"shell_network_policy,omitempty"`
	SandboxMode          *string                        `toml:"sandbox_mode,omitempty" json:"sandbox_mode,omitempty"`
	ProjectProfiles      map[string]ProjectTrustProfile `toml:"project_profiles,omitempty" json:"project_profiles,omitempty"`
	CommandApprovalRules []CommandApprovalRule          `toml:"command_approval_rules,omitempty" json:"command_approval_rules,omitempty"`
	NetworkApprovalRules []NetworkApprovalRule          `toml:"network_approval_rules,omitempty" json:"network_approval_rules,omitempty"`
	ModelProvider        *string                        `toml:"model_provider,omitempty" json:"model_provider,omitempty"`
	ApprovalsReviewer    *string                        `toml:"approvals_reviewer,omitempty" json:"approvals_reviewer,omitempty"`
	BedrockCredentials   aws.CredentialsProvider        `toml:"-" json:"-"`
}

type Registration struct {
	AssignmentID string `toml:"assignment_id" json:"assignment_id"`
	Identity     string `toml:"identity" json:"identity"`
	PublicKey    string `toml:"public_key" json:"public_key"`
	AccountUser  string `toml:"account_user" json:"account_user"`
	Endpoint     string `toml:"endpoint" json:"endpoint"`
}

type EntryKind string

const (
	EntryUser      EntryKind = "User"
	EntryAssistant EntryKind = "Assistant"
	EntryReasoning EntryKind = "Reasoning"
	EntryTool      EntryKind = "Tool"
	EntryInfo      EntryKind = "Info"
	EntryQueued    EntryKind = "Queued"
	EntryStatus    EntryKind = "Status"
	EntryDebug     EntryKind = "Debug"
	EntryError     EntryKind = "Error"
)

type TranscriptEntry struct {
	Kind      EntryKind `json:"kind"`
	Text      string    `json:"text"`
	Streaming bool      `json:"streaming"`
}

type Usage struct {
	InputTokens           uint64  `json:"input_tokens"`
	OutputTokens          uint64  `json:"output_tokens"`
	TotalTokens           uint64  `json:"total_tokens"`
	CacheReadInputTokens  uint64  `json:"cache_read_input_tokens"`
	CacheWriteInputTokens uint64  `json:"cache_write_input_tokens"`
	ReasoningTokens       *uint64 `json:"reasoning_tokens"`
}

type SessionSnapshot struct {
	Version           int               `json:"version"`
	SessionID         string            `json:"session_id"`
	UpdatedAtUnix     uint64            `json:"updated_at_unix"`
	CWD               *string           `json:"cwd"`
	CWDHistory        []string          `json:"cwd_history"`
	BedrockMessages   []json.RawMessage `json:"bedrock_messages"`
	Transcript        []TranscriptEntry `json:"transcript"`
	History           []string          `json:"history"`
	Usage             *Usage            `json:"usage"`
	ContextBudget     ContextBudget     `json:"context_budget,omitempty"`
	CollaborationMode CollaborationMode `json:"collaboration_mode"`
}

// ContextBudget anchors local growth estimates to the most recent Bedrock usage.
type ContextBudget struct {
	KnownTokens  uint64 `json:"known_tokens"`
	LastEstimate uint64 `json:"last_estimate"`
}

type ToolCall struct {
	CallID    string         `json:"call_id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type Sink interface {
	ReasoningDelta(string)
	AssistantDelta(string)
	AssistantMessage(string)
	AssistantDone()
	ToolCall(ToolCall)
	ToolResult(ToolCall, string)
	Info(string)
	Debug(string)
	Usage(Usage)
}
