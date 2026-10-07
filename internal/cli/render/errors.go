package render

// UsageError is an argument-level error with the help page for the command
// the caller meant to invoke.
type UsageError struct {
	Message string
	Page    HelpPage
}

// Render returns the usage line followed by a blank line and the exact help
// page, including its trailing newline.
func (e UsageError) Render() string {
	return e.Message + "\n\n" + e.Page.Contents()
}

// Moved returns the single-line response for a command form that was moved.
func Moved(message string) string {
	return "kogen: moved: use " + message + "\n"
}
