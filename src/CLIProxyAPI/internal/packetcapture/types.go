package packetcapture

import "time"

type PacketSet struct {
	ClientRequest    string `json:"client_request"`
	UpstreamRequest  string `json:"upstream_request"`
	UpstreamResponse string `json:"upstream_response"`
	ClientResponse   string `json:"client_response"`
}

type Record struct {
	ID                 string    `json:"id"`
	Timestamp          time.Time `json:"timestamp"`
	RequestID          string    `json:"request_id,omitempty"`
	Provider           string    `json:"provider"`
	ProviderGroup      string    `json:"provider_group,omitempty"`
	Source             string    `json:"source,omitempty"`
	Model              string    `json:"model"`
	UserToken          string    `json:"user_token,omitempty"`
	AuthID             string    `json:"auth_id,omitempty"`
	AuthLabel          string    `json:"auth_label,omitempty"`
	AuthType           string    `json:"auth_type,omitempty"`
	AuthIndex          string    `json:"auth_index,omitempty"`
	APIKey             string    `json:"api_key,omitempty"`
	ClientUA           string    `json:"client_ua,omitempty"`
	Endpoint           string    `json:"endpoint,omitempty"`
	UpstreamStatusCode int       `json:"upstream_status_code"`
	Failed             bool      `json:"failed"`
	TotalBytes         int64     `json:"total_bytes"`
	Packets            PacketSet `json:"packets"`
	Summary            string    `json:"summary,omitempty"`
}

type RecordSummary struct {
	ID                 string    `json:"id"`
	Timestamp          time.Time `json:"timestamp"`
	RequestID          string    `json:"request_id,omitempty"`
	Provider           string    `json:"provider"`
	Source             string    `json:"source,omitempty"`
	Model              string    `json:"model"`
	UserToken          string    `json:"user_token,omitempty"`
	AuthID             string    `json:"auth_id,omitempty"`
	AuthLabel          string    `json:"auth_label,omitempty"`
	AuthType           string    `json:"auth_type,omitempty"`
	AuthIndex          string    `json:"auth_index,omitempty"`
	APIKey             string    `json:"api_key,omitempty"`
	ClientUA           string    `json:"client_ua,omitempty"`
	Endpoint           string    `json:"endpoint,omitempty"`
	UpstreamStatusCode int       `json:"upstream_status_code"`
	Failed             bool      `json:"failed"`
	TotalBytes         int64     `json:"total_bytes"`
	Summary            string    `json:"summary,omitempty"`
}

type QueryOptions struct {
	Limit     int
	Model     string
	Source    string
	Result    string
	Provider  string
	RequestID string
}

type DeleteResult struct {
	Deleted int64    `json:"deleted"`
	Missing []string `json:"missing"`
}

type Rule struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Enabled         bool        `json:"enabled"`
	RecordHistory   bool        `json:"record_history"`
	Priority        int         `json:"priority"`
	MatchLogic      string      `json:"match_logic,omitempty"`
	Provider        string      `json:"provider,omitempty"`
	ProviderKeyword string      `json:"provider_keyword,omitempty"`
	Model           string      `json:"model,omitempty"`
	ModelKeyword    string      `json:"model_keyword,omitempty"`
	Packet          string      `json:"packet"`
	Part            string      `json:"part"`
	JSONPath        string      `json:"json_path,omitempty"`
	Header          string      `json:"header,omitempty"`
	Operator        string      `json:"operator"`
	Value           string      `json:"value,omitempty"`
	ValueNumber     float64     `json:"value_number,omitempty"`
	Action          string      `json:"action"`
	Replacement     string      `json:"replacement,omitempty"`
	ReplaceLimit    int         `json:"replace_limit,omitempty"`
	CooldownSeconds int         `json:"cooldown_seconds,omitempty"`
	Target          string      `json:"target,omitempty"`
	Notes           string      `json:"notes,omitempty"`
	Conditions      []Condition `json:"conditions,omitempty"`
	Actions         []Action    `json:"actions,omitempty"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

type Condition struct {
	Packet      string  `json:"packet,omitempty"`
	Part        string  `json:"part,omitempty"`
	JSONPath    string  `json:"json_path,omitempty"`
	Header      string  `json:"header,omitempty"`
	Operator    string  `json:"operator,omitempty"`
	Value       string  `json:"value,omitempty"`
	ValueNumber float64 `json:"value_number,omitempty"`
}

type Action struct {
	Type            string `json:"type"`
	Packet          string `json:"packet,omitempty"`
	Part            string `json:"part,omitempty"`
	JSONPath        string `json:"json_path,omitempty"`
	Header          string `json:"header,omitempty"`
	Value           string `json:"value,omitempty"`
	Replacement     string `json:"replacement,omitempty"`
	ReplaceLimit    int    `json:"replace_limit,omitempty"`
	Target          string `json:"target,omitempty"`
	CooldownSeconds int    `json:"cooldown_seconds,omitempty"`
}

type TriggerRecord struct {
	ID              string    `json:"id"`
	RuleID          string    `json:"rule_id"`
	RuleName        string    `json:"rule_name"`
	RecordID        string    `json:"record_id"`
	Timestamp       time.Time `json:"timestamp"`
	Action          string    `json:"action"`
	Target          string    `json:"target,omitempty"`
	Account         string    `json:"account,omitempty"`
	AuthID          string    `json:"auth_id,omitempty"`
	AuthLabel       string    `json:"auth_label,omitempty"`
	AuthType        string    `json:"auth_type,omitempty"`
	AuthIndex       string    `json:"auth_index,omitempty"`
	APIKey          string    `json:"api_key,omitempty"`
	Provider        string    `json:"provider,omitempty"`
	Source          string    `json:"source,omitempty"`
	Model           string    `json:"model,omitempty"`
	Packet          string    `json:"packet,omitempty"`
	PacketName      string    `json:"packet_name,omitempty"`
	Detail          string    `json:"detail,omitempty"`
	CooldownSeconds int       `json:"cooldown_seconds,omitempty"`
}

type ActionEvent struct {
	Record          Record
	Trigger         TriggerRecord
	AuthID          string
	AuthLabel       string
	AuthType        string
	AuthIndex       string
	APIKey          string
	Provider        string
	Model           string
	Action          string
	Target          string
	CooldownSeconds int
	RuleName        string
}
