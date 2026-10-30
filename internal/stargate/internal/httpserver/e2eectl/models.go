// Package e2eectl ports the DysonNetwork.Padlock E2EE/MLS delivery HTTP
// surface (E2eeController.cs) — a pure delivery service with no server-side
// crypto. All artifacts are opaque blobs; the only crypto constant is the
// default MLS ciphersuite.
package e2eectl

import (
	"src.solsynth.dev/sosys/stargate/internal/model"
)

// DefaultMlsCiphersuite mirrors E2eeController's ciphersuite default.
const DefaultMlsCiphersuite = "MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519"

// Envelope types (SnE2eeEnvelopeType).
const (
	envelopeTypePairwiseMessage       = 0
	envelopeTypeSenderKeyDistribution = 1
	envelopeTypeSenderKeyMessage      = 2
	envelopeTypeControl               = 3
	envelopeTypeMlsCommit             = 4
	envelopeTypeMlsWelcome            = 5
	envelopeTypeMlsApplication        = 6
	envelopeTypeMlsProposal           = 7
)

// Envelope delivery statuses (SnE2eeEnvelopeStatus).
const (
	envelopeStatusPending      = 0
	envelopeStatusDelivered    = 1
	envelopeStatusAcknowledged = 2
	envelopeStatusFailed       = 3
)

// --- Wire responses (snake_case, nulls omitted per the house policy) ---

// MlsKeyPackage mirrors SnMlsKeyPackage.
type MlsKeyPackage struct {
	Id                  string         `json:"id"`
	AccountId           string         `json:"account_id"`
	DeviceId            string         `json:"device_id"`
	DeviceLabel         *string        `json:"device_label,omitempty"`
	KeyPackage          []byte         `json:"key_package"`
	Ciphersuite         string         `json:"ciphersuite"`
	IsConsumed          bool           `json:"is_consumed"`
	ConsumedAt          *model.Time    `json:"consumed_at,omitempty"`
	ConsumedByAccountId *string        `json:"consumed_by_account_id,omitempty"`
	Meta                map[string]any `json:"meta,omitempty"`
	CreatedAt           *model.Time    `json:"created_at,omitempty"`
	UpdatedAt           *model.Time    `json:"updated_at,omitempty"`
	DeletedAt           *model.Time    `json:"deleted_at,omitempty"`
}

// MlsDeviceKeyPackage mirrors MlsDeviceKeyPackageResponse.
type MlsDeviceKeyPackage struct {
	AccountId   string         `json:"account_id"`
	DeviceId    string         `json:"device_id"`
	DeviceLabel *string        `json:"device_label,omitempty"`
	Ciphersuite string         `json:"ciphersuite"`
	KeyPackage  []byte         `json:"key_package"`
	Meta        map[string]any `json:"meta,omitempty"`
}

// MlsDeviceKpStatus mirrors MlsDeviceKpStatus.
type MlsDeviceKpStatus struct {
	DeviceId       string  `json:"device_id"`
	DeviceLabel    *string `json:"device_label,omitempty"`
	AvailableCount int     `json:"available_count"`
}

// MlsKeyPackageStatus mirrors MlsKeyPackageStatusResponse.
type MlsKeyPackageStatus struct {
	NeedsMoreKps      bool                `json:"needs_more_kps"`
	DevicesNeedingKps []MlsDeviceKpStatus `json:"devices_needing_kps"`
}

// CheckMlsReady mirrors CheckMlsReadyResponse.
type CheckMlsReady struct {
	IsReady              bool `json:"is_ready"`
	AvailableKeyPackages int  `json:"available_key_packages"`
}

// MlsUserAvailability mirrors MlsUserAvailability.
type MlsUserAvailability struct {
	AccountId            string `json:"account_id"`
	IsReady              bool   `json:"is_ready"`
	AvailableKeyPackages int    `json:"available_key_packages"`
}

// BatchCheckMlsReady mirrors BatchCheckMlsReadyResponse.
type BatchCheckMlsReady struct {
	Users []MlsUserAvailability `json:"users"`
}

// MlsGroupState mirrors SnMlsGroupState.
type MlsGroupState struct {
	Id           string         `json:"id"`
	MlsGroupId   string         `json:"mls_group_id"`
	Epoch        int64          `json:"epoch"`
	StateVersion int64          `json:"state_version"`
	LastCommitAt *model.Time    `json:"last_commit_at,omitempty"`
	GroupInfo    []byte         `json:"group_info"`
	RatchetTree  []byte         `json:"ratchet_tree"`
	Meta         map[string]any `json:"meta,omitempty"`
	CreatedAt    *model.Time    `json:"created_at,omitempty"`
	UpdatedAt    *model.Time    `json:"updated_at,omitempty"`
	DeletedAt    *model.Time    `json:"deleted_at,omitempty"`
}

// MlsDeviceMembership mirrors SnMlsDeviceMembership.
type MlsDeviceMembership struct {
	Id                     string      `json:"id"`
	MlsGroupId             string      `json:"mls_group_id"`
	AccountId              string      `json:"account_id"`
	DeviceId               string      `json:"device_id"`
	JoinedEpoch            int64       `json:"joined_epoch"`
	LastSeenEpoch          *int64      `json:"last_seen_epoch,omitempty"`
	LastReshareRequiredAt  *model.Time `json:"last_reshare_required_at,omitempty"`
	LastReshareCompletedAt *model.Time `json:"last_reshare_completed_at,omitempty"`
	CreatedAt              *model.Time `json:"created_at,omitempty"`
	UpdatedAt              *model.Time `json:"updated_at,omitempty"`
	DeletedAt              *model.Time `json:"deleted_at,omitempty"`
}

// E2eeEnvelope mirrors SnE2eeEnvelope.
type E2eeEnvelope struct {
	Id                  string         `json:"id"`
	SenderId            string         `json:"sender_id"`
	SenderDeviceId      *string        `json:"sender_device_id,omitempty"`
	RecipientId         string         `json:"recipient_id"`
	RecipientAccountId  string         `json:"recipient_account_id"`
	RecipientDeviceId   *string        `json:"recipient_device_id,omitempty"`
	SessionId           *string        `json:"session_id,omitempty"`
	Type                int            `json:"type"`
	GroupId             *string        `json:"group_id,omitempty"`
	ClientMessageId     *string        `json:"client_message_id,omitempty"`
	Sequence            int64          `json:"sequence"`
	Ciphertext          []byte         `json:"ciphertext"`
	Header              []byte         `json:"header,omitempty"`
	Signature           []byte         `json:"signature,omitempty"`
	DeliveryStatus      int            `json:"delivery_status"`
	DeliveredAt         *model.Time    `json:"delivered_at,omitempty"`
	AckedAt             *model.Time    `json:"acked_at,omitempty"`
	ExpiresAt           *model.Time    `json:"expires_at,omitempty"`
	LegacyAccountScoped bool           `json:"legacy_account_scoped"`
	Meta                map[string]any `json:"meta,omitempty"`
	CreatedAt           *model.Time    `json:"created_at,omitempty"`
	UpdatedAt           *model.Time    `json:"updated_at,omitempty"`
	DeletedAt           *model.Time    `json:"deleted_at,omitempty"`
}

// UploadGroupInfoResult is the PUT groupinfo response body.
type UploadGroupInfoResult struct {
	Success bool   `json:"success"`
	GroupId string `json:"group_id"`
	Epoch   int64  `json:"epoch"`
}

// GroupInfoView is the GET groupinfo response body.
type GroupInfoView struct {
	GroupId     string `json:"group_id"`
	Epoch       int64  `json:"epoch"`
	GroupInfo   []byte `json:"group_info"`
	RatchetTree []byte `json:"ratchet_tree"`
}

// --- Request bodies (verbatim DTOs from E2eeController.cs) ---

// publishMlsKeyPackageBody mirrors PublishMlsKeyPackageBody.
type publishMlsKeyPackageBody struct {
	KeyPackage  []byte         `json:"key_package"`
	Ciphersuite string         `json:"ciphersuite"`
	DeviceId    string         `json:"device_id"`
	DeviceLabel *string        `json:"device_label,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

// bootstrapMlsGroupBody mirrors BootstrapMlsGroupBody.
type bootstrapMlsGroupBody struct {
	Epoch        int64          `json:"epoch"`
	StateVersion *int64         `json:"state_version,omitempty"`
	Meta         map[string]any `json:"meta,omitempty"`
}

// commitMlsGroupBody mirrors CommitMlsGroupBody.
type commitMlsGroupBody struct {
	Epoch  int64          `json:"epoch"`
	Reason string         `json:"reason"`
	Meta   map[string]any `json:"meta,omitempty"`
}

// fanoutEnvelopeItemBody mirrors FanoutEnvelopeItemBody.
type fanoutEnvelopeItemBody struct {
	RecipientDeviceId *string        `json:"recipient_device_id,omitempty"`
	ClientMessageId   *string        `json:"client_message_id,omitempty"`
	Ciphertext        []byte         `json:"ciphertext"`
	Header            []byte         `json:"header,omitempty"`
	Signature         []byte         `json:"signature,omitempty"`
	Meta              map[string]any `json:"meta,omitempty"`
}

// fanoutMlsWelcomeBody mirrors FanoutMlsWelcomeBody.
type fanoutMlsWelcomeBody struct {
	RecipientAccountId *string                  `json:"recipient_account_id,omitempty"`
	ExpiresAt          *model.Time              `json:"expires_at,omitempty"`
	Payloads           []fanoutEnvelopeItemBody `json:"payloads"`
}

// markMlsReshareRequiredBody mirrors MarkMlsReshareRequiredBody.
type markMlsReshareRequiredBody struct {
	TargetAccountId string `json:"target_account_id"`
	TargetDeviceId  string `json:"target_device_id"`
	Epoch           int64  `json:"epoch"`
	Reason          string `json:"reason"`
}

// uploadGroupInfoBody mirrors UploadGroupInfoBody.
type uploadGroupInfoBody struct {
	Epoch       int64  `json:"epoch"`
	GroupInfo   []byte `json:"group_info"`
	RatchetTree []byte `json:"ratchet_tree"`
}

// fanoutEnvelopeBody mirrors FanoutEnvelopeBody.
type fanoutEnvelopeBody struct {
	RecipientAccountId string                   `json:"recipient_account_id"`
	SessionId          *string                  `json:"session_id,omitempty"`
	Type               int                      `json:"type"`
	GroupId            *string                  `json:"group_id,omitempty"`
	ExpiresAt          *model.Time              `json:"expires_at,omitempty"`
	IncludeSenderCopy  bool                     `json:"include_sender_copy"`
	Payloads           []fanoutEnvelopeItemBody `json:"payloads"`
}

// fanoutMlsCommitBody mirrors FanoutMlsCommitBody.
type fanoutMlsCommitBody struct {
	Epoch           int64          `json:"epoch"`
	Ciphertext      []byte         `json:"ciphertext"`
	Header          []byte         `json:"header,omitempty"`
	Signature       []byte         `json:"signature,omitempty"`
	ClientMessageId *string        `json:"client_message_id,omitempty"`
	Meta            map[string]any `json:"meta,omitempty"`
}

// fanoutMlsGroupMessageBody mirrors FanoutMlsGroupMessageBody.
type fanoutMlsGroupMessageBody struct {
	Ciphertext      []byte         `json:"ciphertext"`
	Header          []byte         `json:"header,omitempty"`
	Signature       []byte         `json:"signature,omitempty"`
	ClientMessageId *string        `json:"client_message_id,omitempty"`
	Meta            map[string]any `json:"meta,omitempty"`
}

// batchCheckMlsReadyRequest mirrors BatchCheckMlsReadyRequest.
type batchCheckMlsReadyRequest struct {
	AccountIds []string `json:"account_ids"`
}

// addMlsDeviceMembershipBody mirrors AddMlsDeviceMembershipBody.
type addMlsDeviceMembershipBody struct {
	GroupId string `json:"group_id"`
	Epoch   int64  `json:"epoch"`
}

// resetMlsGroupBody mirrors ResetMlsGroupBody.
type resetMlsGroupBody struct {
	NewEpoch     int64   `json:"new_epoch"`
	StateVersion int64   `json:"state_version"`
	Reason       *string `json:"reason,omitempty"`
}
