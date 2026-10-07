package oauth

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/process"
)

func openBrowser(ctx context.Context, options Options, authorizeURL string) error {
	program := options.BrowserExecutable
	if program == "" {
		switch runtime.GOOS {
		case "darwin":
			program = "open"
		case "linux":
			program = "xdg-open"
		default:
			return ErrBrowserOpen
		}
	}
	workingDirectory, err := os.MkdirTemp("", "kogen-oauth-browser-")
	if err != nil {
		return ErrBrowserOpen
	}
	defer os.RemoveAll(workingDirectory)
	if err := os.Chmod(workingDirectory, 0o700); err != nil {
		return ErrBrowserOpen
	}

	environment := append([]string(nil), options.BrowserEnvironment...)
	if environment == nil {
		environment = browserEnvironment()
	}
	runner := options.ProcessRunner
	if runner == nil {
		runner = process.Supervisor{}
	}
	result, err := runner.Run(ctx, contract.ProcessSpec{
		Executable:      program,
		Args:            []string{authorizeURL},
		Dir:             workingDirectory,
		Env:             environment,
		Timeout:         20 * time.Second,
		OutputLimit:     16 << 10,
		OutputTailLimit: 2 << 10,
		LogPath:         filepath.Join(workingDirectory, "browser.log"),
	})
	if err != nil || result.Unavailable || result.TimedOut || result.ExitStatus == nil || *result.ExitStatus != 0 {
		return ErrBrowserOpen
	}
	return nil
}

func browserEnvironment() []string {
	allowed := []string{"PATH", "HOME", "LANG", "LC_ALL", "DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"}
	values := make([]string, 0, len(allowed))
	for _, name := range allowed {
		if value, exists := os.LookupEnv(name); exists && !strings.ContainsRune(value, '\x00') {
			values = append(values, name+"="+value)
		}
	}
	return values
}

var _ contract.ProcessRunner = process.Supervisor{}
