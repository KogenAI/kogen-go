package diagnostic

import (
	"errors"
	"sort"

	"kogen-go/internal/auth/accounts"
	"kogen-go/internal/auth/vault"
)

// AccountsObservation records the production accounts.yaml view, the actual
// deterministic list rendering, and one production Resolve result. Profiles
// are vault listing metadata only; credential/token bytes are not accepted or
// serialized by this adapter.
type AccountsObservation struct {
	AccountsYAML string           `json:"accounts_yaml"`
	Profiles     []AccountProfile `json:"profiles"`
	ListOutput   string           `json:"list_output"`
	Resolved     *ResolvedAccount `json:"resolved"`
	ResolveError string           `json:"resolve_error,omitempty"`
}

type AccountProfile struct {
	Provider      accounts.Provider `json:"provider"`
	Label         string            `json:"label"`
	SignedIn      bool              `json:"signed_in"`
	Email         *string           `json:"email"`
	ExpiresAt     int64             `json:"expires_at"`
	RemoteRevoked bool              `json:"remote_revoked,omitempty"`
	PlanUsage     *string           `json:"plan_usage,omitempty"`
	NoticeShown   *bool             `json:"notice_shown,omitempty"`
}

type ResolvedAccount struct {
	Provider         accounts.Provider `json:"provider"`
	Label            string            `json:"label"`
	CredentialSource string            `json:"credential_source"`
	ProviderSource   string            `json:"provider_source"`
	AccountSource    string            `json:"account_source"`
}

// ObserveAccounts reads through the production rooted Store and invokes the
// production resolver and list formatter. Callers must supply a scratch Store
// and synthetic vault metadata; this adapter never opens a live user account.
func ObserveAccounts(store *accounts.Store, profiles vault.Profiles, input accounts.ResolveInput) (AccountsObservation, error) {
	if store == nil {
		return AccountsObservation{}, errors.New("diagnostic: accounts store is required")
	}
	file, err := store.Read()
	if err != nil {
		return AccountsObservation{}, err
	}
	canonical, err := accounts.Marshal(file)
	if err != nil {
		return AccountsObservation{}, err
	}
	observation := AccountsObservation{
		AccountsYAML: string(canonical), Profiles: profileRows(profiles),
		ListOutput: accounts.FormatList(file, profiles),
	}
	resolved, err := accounts.Resolve(file, input)
	if err != nil {
		observation.ResolveError = err.Error()
		return observation, nil
	}
	observation.Resolved = &ResolvedAccount{
		Provider: resolved.Provider, Label: resolved.Label,
		CredentialSource: resolved.CredentialSource,
		ProviderSource:   resolved.ProviderSource, AccountSource: resolved.AccountSource,
	}
	return observation, nil
}

func profileRows(profiles vault.Profiles) []AccountProfile {
	rows := make([]AccountProfile, 0, len(profiles.ChatGPT)+len(profiles.Grok))
	chatLabels := make([]string, 0, len(profiles.ChatGPT))
	for label := range profiles.ChatGPT {
		chatLabels = append(chatLabels, label)
	}
	sort.Strings(chatLabels)
	for _, label := range chatLabels {
		profile := profiles.ChatGPT[label]
		rows = append(rows, AccountProfile{
			Provider: accounts.ChatGPT, Label: label, SignedIn: profile.SignedIn,
			Email: cloneString(profile.Email), ExpiresAt: profile.ExpiresAt,
			RemoteRevoked: profile.RemoteRevoked, PlanUsage: cloneString(profile.PlanUsage),
			NoticeShown: cloneBool(profile.NoticeShown),
		})
	}
	grokLabels := make([]string, 0, len(profiles.Grok))
	for label := range profiles.Grok {
		grokLabels = append(grokLabels, label)
	}
	sort.Strings(grokLabels)
	for _, label := range grokLabels {
		profile := profiles.Grok[label]
		rows = append(rows, AccountProfile{
			Provider: accounts.Grok, Label: label, SignedIn: profile.SignedIn,
			Email: cloneString(profile.Email), ExpiresAt: profile.ExpiresAt,
		})
	}
	return rows
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
