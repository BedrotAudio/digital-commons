package client

// Wire types for docs/api.md. Unknown fields are ignored by encoding/json. A JSON
// null decodes to the empty string where null and "" mean the same thing; a
// pointer is used where absence is meaningful.

// Locality says where processing happens. Kind is this_device, organization or
// demo.
type Locality struct {
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Application describes the running product.
type Application struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Stage   string `json:"stage"`
	Notice  string `json:"notice"`
}

// Identity is the authenticated caller.
type Identity struct {
	UserID      string   `json:"user_id"`
	DisplayName string   `json:"display_name"`
	Groups      []string `json:"groups"`
	AuthMode    string   `json:"auth_mode"`
}

// Permissions lists what the caller may do.
type Permissions struct {
	AdvancedMode         bool     `json:"advanced_mode"`
	ManualModelSelection bool     `json:"manual_model_selection"`
	ViewDiagnostics      bool     `json:"view_diagnostics"`
	SearchDocuments      bool     `json:"search_documents"`
	AllowedTiers         []string `json:"allowed_tiers"`
}

// Suggestion is a home-screen card. Action is prompt, attach, attach_two or
// documents.
type Suggestion struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Action   string `json:"action"`
	Prompt   string `json:"prompt"`
}

// Limits are the gateway's size limits. MaxAttachmentBytes applies to the UTF-8
// encoded text.
type Limits struct {
	MaxMessageChars      int      `json:"max_message_chars"`
	MaxAttachments       int      `json:"max_attachments"`
	MaxAttachmentBytes   int      `json:"max_attachment_bytes"`
	AttachmentExtensions []string `json:"attachment_extensions"`
}

// Bootstrap is the response of GET /v1/bootstrap.
type Bootstrap struct {
	Application Application  `json:"application"`
	Identity    Identity     `json:"identity"`
	Permissions Permissions  `json:"permissions"`
	Locality    Locality     `json:"locality"`
	Suggestions []Suggestion `json:"suggestions"`
	Limits      Limits       `json:"limits"`
}

// Warmup records the last warm-up attempt. State is idle, loading, ready or
// failed.
type Warmup struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

// GatewayInfo describes the gateway process.
type GatewayInfo struct {
	Version    string `json:"version"`
	InstanceID string `json:"instance_id"`
	UptimeS    int64  `json:"uptime_s"`
	Address    string `json:"address"`
}

// MissingModel names a tier whose model is not installed.
type MissingModel struct {
	Tier        string `json:"tier"`
	Model       string `json:"model"`
	InstallHint string `json:"install_hint"`
}

// LoadedModel is a model held in memory by the provider.
type LoadedModel struct {
	Name          string `json:"name"`
	SizeBytes     int64  `json:"size_bytes"`
	GPUBytes      int64  `json:"gpu_bytes"`
	ContextTokens int    `json:"context_tokens"`
	ExpiresAt     string `json:"expires_at"`
}

// Inference is the provider diagnostic block. CloudAccessDisabled is nil when the
// provider has no cloud gate or could not be asked.
type Inference struct {
	Provider            string         `json:"provider"`
	Kind                string         `json:"kind"`
	Reachable           bool           `json:"reachable"`
	Version             string         `json:"version"`
	Error               string         `json:"error"`
	CloudAccessDisabled *bool          `json:"cloud_access_disabled"`
	MissingModels       []MissingModel `json:"missing_models"`
	WarmupModel         string         `json:"warmup_model"`
	LoadedModels        []LoadedModel  `json:"loaded_models"`
}

// Status is the response of GET /v1/status. State is starting, ready, degraded,
// unavailable or demo. Reason is empty, provider_unreachable, model_missing,
// model_load_failed or cloud_access_enabled. Inference is nil unless the caller
// holds view_diagnostics.
type Status struct {
	State     string      `json:"state"`
	Reason    string      `json:"reason"`
	Summary   string      `json:"summary"`
	Locality  Locality    `json:"locality"`
	Warmup    Warmup      `json:"warmup"`
	Gateway   GatewayInfo `json:"gateway"`
	Inference *Inference  `json:"inference"`
}

// Tier describes one model tier: fast, standard, advanced or specialist.
type Tier struct {
	Tier              string  `json:"tier"`
	Label             string  `json:"label"`
	Description       string  `json:"description"`
	Enabled           bool    `json:"enabled"`
	Available         bool    `json:"available"`
	UnavailableReason string  `json:"unavailable_reason"`
	Model             string  `json:"model"`
	ContextTokens     int     `json:"context_tokens"`
	MaxOutputTokens   int     `json:"max_output_tokens"`
	Temperature       float64 `json:"temperature"`
	Thinking          bool    `json:"thinking"`
}

// Models is the response of GET /v1/models.
type Models struct {
	Provider string `json:"provider"`
	Tiers    []Tier `json:"tiers"`
}

// PrivacyStatement is one computed fact. Status is ok, info or warn. Detail is
// technical and shown only in Advanced mode.
type PrivacyStatement struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Value  string `json:"value"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Privacy is the response of GET /v1/privacy.
type Privacy struct {
	Locality   Locality           `json:"locality"`
	Statements []PrivacyStatement `json:"statements"`
	Notice     string             `json:"notice"`
}

// Attachment is a decoded file sent with a turn. Text is always UTF-8.
type Attachment struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Text      string `json:"text"`
}

// AttachmentInfo describes a stored attachment. Its text is never returned.
type AttachmentInfo struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Chars     int    `json:"chars"`
}

// ChatRequest is the body of POST /v1/chat/stream. A nil ConversationID starts a
// new conversation. Empty Tier and Assistant are sent as auto and general.
type ChatRequest struct {
	ConversationID *string      `json:"conversation_id"`
	Message        string       `json:"message"`
	Attachments    []Attachment `json:"attachments"`
	Tier           string       `json:"tier"`
	Assistant      string       `json:"assistant"`
	UseDocuments   bool         `json:"use_documents"`
	Regenerate     bool         `json:"regenerate"`
}

// Route says how a turn was routed. Model, Provider and Reason are present only
// for callers holding view_diagnostics.
type Route struct {
	RequestedTier string `json:"requested_tier"`
	Tier          string `json:"tier"`
	Locality      string `json:"locality"`
	FallbackFrom  string `json:"fallback_from"`
	Model         string `json:"model"`
	Provider      string `json:"provider"`
	Reason        string `json:"reason"`
}

// ContextInfo is the context budget of a turn.
type ContextInfo struct {
	EstimatedTokens int `json:"estimated_tokens"`
	BudgetTokens    int `json:"budget_tokens"`
	OmittedMessages int `json:"omitted_messages"`
}

// Notice is shown to the user above a reply.
type Notice struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Citation is a source for a reply. When Withheld is true only Number is set.
type Citation struct {
	Number         int    `json:"number"`
	Withheld       bool   `json:"withheld"`
	DocumentID     string `json:"document_id"`
	Title          string `json:"title"`
	Department     string `json:"department"`
	Classification string `json:"classification"`
	Version        string `json:"version"`
	DocumentDate   string `json:"document_date"`
	SourceFilename string `json:"source_filename"`
	Section        string `json:"section"`
	Page           *int   `json:"page"`
	Snippet        string `json:"snippet"`
	Sample         bool   `json:"sample"`
}

// Usage is the token accounting of a finished turn. Estimated is true when the
// provider gave no counts.
type Usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	FirstTokenMS     int64   `json:"first_token_ms"`
	TotalMS          int64   `json:"total_ms"`
	LoadMS           int64   `json:"load_ms"`
	TokensPerSecond  float64 `json:"tokens_per_second"`
	Estimated        bool    `json:"estimated"`
}

// ConversationSummary is a row of GET /v1/conversations. Times are UTC ISO 8601.
type ConversationSummary struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Assistant    string `json:"assistant"`
	Locality     string `json:"locality"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	MessageCount int    `json:"message_count"`
}

// Message is a stored message. Role is user or assistant. FinishReason is stop,
// length, cancelled or error.
type Message struct {
	ID           string           `json:"id"`
	Role         string           `json:"role"`
	Content      string           `json:"content"`
	CreatedAt    string           `json:"created_at"`
	Attachments  []AttachmentInfo `json:"attachments"`
	Citations    []Citation       `json:"citations"`
	Notices      []Notice         `json:"notices"`
	Route        *Route           `json:"route"`
	Usage        *Usage           `json:"usage"`
	FinishReason string           `json:"finish_reason"`
}

// Conversation is the response of GET /v1/conversations/{id}.
type Conversation struct {
	ConversationSummary
	Messages []Message `json:"messages"`
}

type conversationList struct {
	Conversations []ConversationSummary `json:"conversations"`
}

type deletedCount struct {
	Deleted int `json:"deleted"`
}

// Health is the response of GET /healthz.
type Health struct {
	Status string `json:"status"`
}
