package model

// Account public keys. An SSH key authenticates git/SSH access for the account;
// a PGP key publishes the account's signing/encryption identity. Neither wire
// type ever carries private material — the API only stores and returns public
// keys.

// SSHKey mirrors an `account_ssh_keys` row in the public-key API.
type SSHKey struct {
	Id          string `json:"id"`
	AccountId   string `json:"account_id"`
	Label       string `json:"label"`
	Algorithm   string `json:"algorithm"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   *Time  `json:"created_at"`
	UpdatedAt   *Time  `json:"updated_at"`
	DeletedAt   *Time  `json:"deleted_at"`
}

// PGPKey mirrors an `account_pgp_keys` row in the public-key API. `KeyId` is
// the 8-byte key id in uppercase hex; `UserIds` are the key's non-revoked
// identity strings.
type PGPKey struct {
	Id          string   `json:"id"`
	AccountId   string   `json:"account_id"`
	Label       string   `json:"label"`
	Algorithm   string   `json:"algorithm"`
	PublicKey   string   `json:"public_key"`
	Fingerprint string   `json:"fingerprint"`
	KeyId       string   `json:"key_id"`
	UserIds     []string `json:"user_ids"`
	CreatedAt   *Time    `json:"created_at"`
	UpdatedAt   *Time    `json:"updated_at"`
	DeletedAt   *Time    `json:"deleted_at"`
}

// AccountKeys is the public listing of an account's keys. Both slices are
// present (never null) so a client can iterate without a nil check.
type AccountKeys struct {
	SSH []SSHKey `json:"ssh"`
	PGP []PGPKey `json:"pgp"`
}
