package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	"kogen-go/internal/auth/accounts"
	"kogen-go/internal/auth/grok"
	"kogen-go/internal/auth/oauth"
	"kogen-go/internal/auth/vault"
	"kogen-go/internal/cli/parse"
	"kogen-go/internal/contract"
	"kogen-go/internal/process"
	"kogen-go/internal/safefs"
)

const providerDefaultLabel = "default"

const unreadableChatGPTCredentialWarning = "kogen: warning: saved ChatGPT credential could not be read; login will replace it.\n"

// providerRoute contains the effects needed by provider commands. Keeping the
// process runner here lets route tests complete a real fake-OIDC browser flow
// without opening a user browser or contacting a live account.
type providerRoute struct {
	home      string
	env       process.Environment
	out       io.Writer
	errOut    io.Writer
	processes contract.ProcessRunner
}

func (cli *CLI) provider(command parse.Command) int {
	route := providerRoute{
		home: cli.Env["HOME"], env: cli.Env, out: cli.Out, errOut: cli.Err,
		processes: process.Supervisor{},
	}
	return route.run(context.Background(), command)
}

func (route providerRoute) run(ctx context.Context, command parse.Command) int {
	if route.home == "" {
		return route.writeError("environment", "home_unavailable", "HOME is not set", 3)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	accountsStore, err := accounts.Open(route.home)
	if err != nil {
		return route.failure(err)
	}
	defer accountsStore.Close()

	switch command.Route {
	case parse.RouteProviderList:
		return route.list(accountsStore)
	case parse.RouteProviderUse:
		return route.use(accountsStore, command)
	case parse.RouteProviderLogin, parse.RouteProviderLogout:
		credentials, err := vault.Open(route.home)
		if err != nil {
			return route.failure(err)
		}
		defer credentials.Close()
		if command.Route == parse.RouteProviderLogin {
			return route.login(ctx, credentials, command.Provider)
		}
		return route.logout(ctx, credentials, command.Provider)
	default:
		return route.writeError("controller", "internal_error", "provider route received an unknown command", 70)
	}
}

func (route providerRoute) list(accountsStore *accounts.Store) int {
	file, err := accountsStore.Read()
	if err != nil {
		return route.failure(err)
	}
	profilesExist, err := profilesFileExists(route.home)
	if err != nil {
		return route.failure(err)
	}
	if !profilesExist {
		_, _ = io.WriteString(route.out, accounts.FormatList(file, vault.Profiles{}))
		return 0
	}
	credentials, err := vault.Open(route.home)
	if err != nil {
		return route.failure(err)
	}
	defer credentials.Close()
	profiles, err := credentials.ReadProfiles()
	if err != nil {
		return route.failure(err)
	}
	_, _ = io.WriteString(route.out, accounts.FormatList(file, profiles))
	return 0
}

func profilesFileExists(home string) (bool, error) {
	root, err := safefs.OpenRoot(home)
	if err != nil {
		return false, err
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Join(".kogen", "profiles.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return false, vault.ErrUnsafeState
	}
	return true, nil
}

func (route providerRoute) use(accountsStore *accounts.Store, command parse.Command) int {
	credentials, err := vault.Open(route.home)
	if err != nil {
		return route.failure(err)
	}
	defer credentials.Close()
	label := ""
	if command.Label != nil {
		label = *command.Label
	}
	project := ""
	if command.Project.Project != nil {
		project = *command.Project.Project
	}
	line, err := accountsStore.Use(accounts.Provider(command.Provider), label, project, credentials)
	if err != nil {
		return route.failure(err)
	}
	_, _ = io.WriteString(route.out, line)
	return 0
}

func (route providerRoute) login(ctx context.Context, credentials *vault.Store, provider string) int {
	switch provider {
	case string(accounts.ChatGPT):
		return route.loginChatGPT(ctx, credentials)
	case string(accounts.Grok):
		return route.loginGrok(ctx, credentials)
	default:
		return route.writeError("provider", "unsupported_provider", "provider login requires chatgpt or grok", 4)
	}
}

func (route providerRoute) loginChatGPT(ctx context.Context, credentials *vault.Store) int {
	profiles, err := credentials.ReadProfiles()
	if err != nil {
		return route.failure(err)
	}
	oldProfile := profiles.ChatGPT[providerDefaultLabel]
	showPlanNotice := oldProfile.NoticeShown == nil || !*oldProfile.NoticeShown
	oldCredential, unreadable, err := credentials.GetChatGPTForLogin(providerDefaultLabel)
	if err != nil {
		return route.failure(err)
	}
	if unreadable {
		_, _ = io.WriteString(route.errOut, unreadableChatGPTCredentialWarning)
	}
	hostID, err := credentials.HostID()
	if err != nil {
		return route.failure(err)
	}
	options := oauth.Options{
		AuthBaseURL: route.env["KOGEN_AUTH_URL"], AllowHTTP: route.env["KOGEN_AUTH_URL"] != "",
		HostID: hostID, CallbackPort: oauth.CallbackPort,
		Output: route.out, ProcessRunner: route.processes,
	}
	if oldCredential != nil {
		options.PreviousClientID = oldCredential.ClientID
		options.PreviousSubject = oldCredential.Subject
	}
	result, err := oauth.Login(ctx, options)
	if err != nil {
		return route.failure(err)
	}
	if err := credentials.PutChatGPT(providerDefaultLabel, result.Credential); err != nil {
		return route.failure(err)
	}
	oldProfile.ClientID = result.Credential.ClientID
	oldProfile.Subject = result.Identity.Subject
	oldProfile.Email = result.Identity.Email
	oldProfile.ExpiresAt = result.Credential.ExpiresAt
	oldProfile.SignedIn = true
	oldProfile.RemoteRevoked = false
	if showPlanNotice {
		shown := true
		oldProfile.NoticeShown = &shown
	}
	if err := credentials.PutChatGPTProfile(providerDefaultLabel, oldProfile); err != nil {
		return route.failure(err)
	}
	if showPlanNotice {
		_, _ = io.WriteString(route.out, accounts.ChatGPTPlanNotice())
	}
	_, _ = io.WriteString(route.out, accounts.FormatChatGPTSignedIn(result.Identity.Email))
	return 0
}

func (route providerRoute) loginGrok(ctx context.Context, credentials *vault.Store) int {
	issuer := route.env["KOGEN_AUTH_URL"]
	scale := 1.0
	if raw := route.env["KOGEN_TIME_SCALE"]; raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed > 0 {
			scale = parsed
		}
	}
	client, err := grok.New(grok.Options{
		Home: route.home, Label: providerDefaultLabel, IssuerURL: issuer,
		AllowLocalHTTP: issuer != "", Store: credentials, TimeScale: scale,
	})
	if err != nil {
		return route.failure(err)
	}
	credential, err := client.Login(ctx, func(line string) { _, _ = io.WriteString(route.out, line) })
	if err != nil {
		return route.failure(err)
	}
	_, _ = io.WriteString(route.out, accounts.FormatGrokSignedIn(credential.Email))
	return 0
}

func (route providerRoute) logout(ctx context.Context, credentials *vault.Store, provider string) int {
	switch provider {
	case string(accounts.ChatGPT):
		credential, _, err := credentials.GetChatGPTForLogin(providerDefaultLabel)
		if err != nil {
			return route.failure(err)
		}
		remoteRevoked := false
		if credential != nil {
			options := oauth.Options{
				AuthBaseURL: route.env["KOGEN_AUTH_URL"], AllowHTTP: route.env["KOGEN_AUTH_URL"] != "",
				HostID: credential.HostID,
			}
			remoteRevoked, _ = oauth.Revoke(ctx, options, *credential)
		}
		if err := credentials.LogoutChatGPT(providerDefaultLabel, remoteRevoked); err != nil {
			return route.failure(err)
		}
		_, _ = io.WriteString(route.out, accounts.FormatChatGPTSignedOut(remoteRevoked))
		return 0
	case string(accounts.Grok):
		if err := credentials.LogoutGrok(providerDefaultLabel); err != nil {
			return route.failure(err)
		}
		_, _ = io.WriteString(route.out, accounts.FormatGrokSignedOut())
		return 0
	default:
		return route.writeError("provider", "unsupported_provider", "provider logout requires chatgpt or grok", 4)
	}
}

func (route providerRoute) failure(err error) int {
	class, reason, status := "environment", "provider_state_unavailable", 3
	switch {
	case errors.Is(err, accounts.ErrNoSavedLogin):
		class, reason, status = "provider", "login_required", 4
	case errors.Is(err, accounts.ErrInvalidLabel), errors.Is(err, accounts.ErrInvalidProvider), errors.Is(err, vault.ErrInvalidLabel):
		class, reason, status = "provider", "invalid_account_selection", 4
	case errors.Is(err, accounts.ErrProjectNotFound):
		class, reason, status = "environment", "project_not_found", 3
	case errors.Is(err, accounts.ErrInvalidFile):
		class, reason, status = "environment", "accounts_invalid", 3
	case errors.Is(err, vault.ErrProfilesCorrupt):
		class, reason, status = "environment", "profiles_invalid", 3
	case errors.Is(err, oauth.ErrStateMismatch), errors.Is(err, oauth.ErrAuthorizationDenied),
		errors.Is(err, oauth.ErrInvalidOptions), errors.Is(err, oauth.ErrDiscovery), errors.Is(err, oauth.ErrTokenExchange),
		errors.Is(err, oauth.ErrInvalidTokenResponse), errors.Is(err, oauth.ErrInvalidCallback),
		errors.Is(err, oauth.ErrCallbackTimeout), errors.Is(err, oauth.ErrCallbackPort),
		errors.Is(err, oauth.ErrBrowserOpen):
		class, reason, status = "provider", "login_failed", 4
	}
	var grokError *grok.Error
	if errors.As(err, &grokError) {
		class, reason, status = "provider", "login_failed", 4
	}
	return route.writeError(class, reason, publicProviderError(err), status)
}

func publicProviderError(err error) string {
	message := strings.TrimSpace(err.Error())
	for _, prefix := range []string{"accounts: ", "vault: ", "oauth: ", "grok auth: "} {
		message = strings.TrimPrefix(message, prefix)
	}
	if message == "" {
		return "provider operation failed"
	}
	return message
}

func (route providerRoute) writeError(class, reason, detail string, status int) int {
	message := class + "/" + reason
	if detail != "" {
		lines := strings.Split(strings.TrimSuffix(detail, "\n"), "\n")
		message += ": " + lines[0]
		for _, line := range lines[1:] {
			message += "\n  " + line
		}
	}
	_, _ = fmt.Fprintln(route.out, message)
	return status
}
