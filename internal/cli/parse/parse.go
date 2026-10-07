package parse

import (
	"kogen-go/internal/cli/render"
)

// Parse recognizes the fixed command tree and options. Slug shape is left to
// the command handler, as required by spec/01-cli.md §1.3.
func Parse(args []string, cwd string) ParsedRequest {
	if message, ok := findMovedForm(args); ok {
		return ParsedRequest{Kind: ResultMoved, Message: message}
	}
	if len(args) == 0 {
		return ParsedRequest{Kind: ResultHelp, Page: render.HelpTop}
	}

	var route Route
	var tail []string
	var page render.HelpPage
	switch args[0] {
	case "help":
		if len(args) == 1 {
			return ParsedRequest{Kind: ResultHelp, Page: render.HelpTop}
		}
		return usage("kogen help: unexpected argument '"+args[1]+"'", render.HelpTop)
	case "status":
		route, tail, page = RouteStatus, args[1:], render.HelpStatus
	case "version":
		route, tail, page = RouteVersion, args[1:], render.HelpVersion
	case "intent":
		if len(args) == 1 {
			return ParsedRequest{Kind: ResultHelp, Page: render.HelpIntent}
		}
		switch args[1] {
		case "shape":
			route, tail, page = RouteIntentShape, args[2:], render.HelpIntentShape
		case "approve":
			route, tail, page = RouteIntentApprove, args[2:], render.HelpIntentApprove
		case "remove":
			route, tail, page = RouteIntentRemove, args[2:], render.HelpIntentRemove
		default:
			return usage("kogen intent: unknown command '"+args[1]+"'", render.HelpIntent)
		}
	case "queue":
		if len(args) == 1 {
			return ParsedRequest{Kind: ResultHelp, Page: render.HelpQueue}
		}
		switch args[1] {
		case "start":
			route, tail, page = RouteQueueStart, args[2:], render.HelpQueueStart
		case "stop":
			route, tail, page = RouteQueueStop, args[2:], render.HelpQueueStop
		default:
			return usage("kogen queue: unknown command '"+args[1]+"'", render.HelpQueue)
		}
	case "provider":
		if len(args) == 1 {
			return ParsedRequest{Kind: ResultHelp, Page: render.HelpProvider}
		}
		switch args[1] {
		case "list":
			route, tail, page = RouteProviderList, args[2:], render.HelpProviderList
		case "login":
			route, tail, page = RouteProviderLogin, args[2:], render.HelpProviderLogin
		case "logout":
			route, tail, page = RouteProviderLogout, args[2:], render.HelpProviderLogout
		case "use":
			route, tail, page = RouteProviderUse, args[2:], render.HelpProviderUse
		default:
			return usage("kogen provider: unknown command '"+args[1]+"'", render.HelpProvider)
		}
	default:
		return usage("kogen: unknown command '"+args[0]+"'", render.HelpTop)
	}

	return parseRoute(route, tail, page, cwd)
}

func parseRoute(route Route, tail []string, page render.HelpPage, cwd string) ParsedRequest {
	options := scanOptions(route, tail)
	if options.booleanValue != "" {
		return usage("kogen "+route.Path()+": "+options.booleanValue+" takes no value", page)
	}
	if options.unknownOption != "" {
		return usage("kogen "+route.Path()+": unknown option '"+options.unknownOption+"'", page)
	}
	if options.disallowed != "" {
		return usage("kogen "+route.Path()+": unknown option '"+options.disallowed+"'", page)
	}
	if options.missingValue != "" {
		return usage("kogen "+route.Path()+": "+options.missingValue+" needs a value", page)
	}

	positionals := collectPositionals(tail)
	if name := missingPositional(route, len(positionals)); name != "" {
		return usage("kogen "+route.Path()+": missing "+name, page)
	}
	if maximum := maxPositionals(route); len(positionals) > maximum {
		return usage("kogen "+route.Path()+": unexpected argument '"+positionals[maximum]+"'", page)
	}
	command, message := buildCommand(route, positionals, options, cwd)
	if message != "" {
		return usage(message, page)
	}
	return ParsedRequest{Kind: ResultCommand, Command: command}
}

func usage(message string, page render.HelpPage) ParsedRequest {
	return ParsedRequest{Kind: ResultUsage, Message: message, Page: page}
}
