// Package account is the registry of Claude accounts switcch manages: their
// identities (persisted in a JSON state file). Each account's backup credential
// is stored separately (see internal/credfile), never in the state file.
package account

// Account is a registered Claude account's identity. It holds no secrets.
type Account struct {
	Slot        int    `json:"slot"`
	Email       string `json:"email"`
	AccountUUID string `json:"accountUuid,omitempty"`
	OrgUUID     string `json:"orgUuid,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	OrgName     string `json:"orgName,omitempty"`
	Alias       string `json:"alias,omitempty"`
	// Fallback is the ordered list of slots this account's sessions fail over
	// to. Stored by slot so alias renames don't break it. Empty = pinned.
	Fallback []int `json:"fallback,omitempty"`
}
