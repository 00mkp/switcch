// Package account is the registry of Claude accounts switcch manages: their
// identities (persisted in a JSON state file) and a reference to each account's
// backup credential (stored separately in the OS keychain, never in the state file).
package account

import "fmt"

// Account is a registered Claude account's identity. It holds no secrets.
type Account struct {
	Slot        int    `json:"slot"`
	Email       string `json:"email"`
	AccountUUID string `json:"accountUuid,omitempty"`
	OrgUUID     string `json:"orgUuid,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	OrgName     string `json:"orgName,omitempty"`
	Alias       string `json:"alias,omitempty"`
}

// CredService is the OS-keychain service name under which this slot's backup
// credential is stored. Distinct per slot so accounts never collide.
func CredService(slot int) string {
	return fmt.Sprintf("switcch-credentials-%d", slot)
}
