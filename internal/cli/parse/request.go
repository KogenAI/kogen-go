package parse

import "kogen-go/internal/cli/render"

// ResultKind identifies which public CLI result should be rendered or handled.
type ResultKind uint8

const (
	ResultHelp ResultKind = iota + 1
	ResultMoved
	ResultUsage
	ResultCommand
)

// ParsedRequest is the result of parsing argv. Only the field named by Kind is
// meaningful; usage errors use Page and Message, while moved forms use Message.
type ParsedRequest struct {
	Kind    ResultKind
	Page    render.HelpPage
	Message string
	Command Command
}

// Route is one command leaf in the fixed public CLI tree.
type Route uint8

const (
	RouteStatus Route = iota + 1
	RouteIntentShape
	RouteIntentApprove
	RouteIntentRemove
	RouteQueueStart
	RouteQueueStop
	RouteProviderList
	RouteProviderLogin
	RouteProviderLogout
	RouteProviderUse
	RouteVersion
)

// Path returns the command words used in public usage errors.
func (r Route) Path() string {
	switch r {
	case RouteStatus:
		return "status"
	case RouteIntentShape:
		return "intent shape"
	case RouteIntentApprove:
		return "intent approve"
	case RouteIntentRemove:
		return "intent remove"
	case RouteQueueStart:
		return "queue start"
	case RouteQueueStop:
		return "queue stop"
	case RouteProviderList:
		return "provider list"
	case RouteProviderLogin:
		return "provider login"
	case RouteProviderLogout:
		return "provider logout"
	case RouteProviderUse:
		return "provider use"
	case RouteVersion:
		return "version"
	default:
		return ""
	}
}

// ProjectOptions contains paths common to project commands. Project is set to
// the working directory for project commands and remains nil for commands
// whose contract does not have a default project.
type ProjectOptions struct {
	Project *string
	Origin  *string
	Base    *string
}

// Command contains the validated, typed inputs for one command leaf. Optional
// positionals and options are nil when they were not supplied.
type Command struct {
	Route    Route
	Slug     *string
	Request  *string
	Hash     *string
	By       *string
	Provider string
	Label    *string
	Project  ProjectOptions
	Watch    bool
	JSON     bool
	Force    bool
	Detach   bool
}
