package commons

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/LerianStudio/lib-commons/v7/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/LerianStudio/lib-observability/v4/assert"
	"github.com/LerianStudio/lib-observability/v4/runtime"
)

// ErrLoggerNil is returned when the Logger is nil and cannot proceed.
var ErrLoggerNil = errors.New("logger is nil")

var (
	// ErrNilLauncher is returned when a launcher method is called on a nil receiver.
	ErrNilLauncher = errors.New("launcher is nil")
	// ErrEmptyApp is returned when an app name is empty or whitespace.
	ErrEmptyApp = errors.New("app name is empty")
	// ErrNilApp is returned when a nil app instance is provided.
	ErrNilApp = errors.New("app is nil")
	// ErrConfigFailed is returned when launcher option application collected errors.
	ErrConfigFailed = errors.New("launcher configuration failed")
)

// App represents an application that will run as a deployable component.
// It's an entrypoint at main.go.
// RedisRepository provides an interface for redis.
//
//go:generate mockgen --destination=app_mock.go --package=commons . App
type App interface {
	Run(launcher *Launcher) error
}

// LauncherOption defines a function option for Launcher.
type LauncherOption func(l *Launcher)

// WithLogger adds a obs.Logger component to launcher.
// If the launcher is nil the option is a no-op, preventing panics when
// option closures are invoked on a nil receiver.
// A nil logger is ignored, typed nils included: storing a Logger that holds a
// nil pointer would satisfy the "Logger == nil" guard on every use site and
// then panic on the first call, instead of returning ErrLoggerNil.
func WithLogger(logger obs.Logger) LauncherOption {
	return func(l *Launcher) {
		if l == nil || nilcheck.Interface(logger) {
			return
		}

		l.Logger = logger
	}
}

// RunApp registers an application with the launcher.
// If registration fails, the error is collected and surfaced when RunWithError is called.
// If the launcher is nil the option is a no-op, preventing panics when
// option closures are invoked on a nil receiver.
func RunApp(name string, app App) LauncherOption {
	return func(l *Launcher) {
		if l == nil {
			return
		}

		if err := l.Add(name, app); err != nil {
			l.configErrors = append(l.configErrors, fmt.Errorf("add app %q: %w", name, err))

			if !nilcheck.Interface(l.Logger) {
				l.Logger.Log(context.Background(), obs.LevelError, "launcher add app error", "error", err)
			}
		}
	}
}

// Launcher manages apps.
type Launcher struct {
	Logger       obs.Logger
	apps         map[string]App
	wg           *sync.WaitGroup
	configErrors []error
	Verbose      bool
}

// Add registers an application under the given name for later execution.
func (l *Launcher) Add(appName string, a App) error {
	if l == nil {
		asserter := assert.New(context.Background(), nil, "launcher", "Add")
		_ = asserter.Never(context.Background(), "launcher receiver is nil")

		return ErrNilLauncher
	}

	if l.apps == nil {
		l.apps = make(map[string]App)
	}

	if l.wg == nil {
		l.wg = new(sync.WaitGroup)
	}

	if strings.TrimSpace(appName) == "" {
		asserter := assert.New(context.Background(), l.Logger, "launcher", "Add")
		_ = asserter.Never(context.Background(), "app name must not be empty")

		return ErrEmptyApp
	}

	if a == nil {
		asserter := assert.New(context.Background(), l.Logger, "launcher", "Add")
		_ = asserter.Never(context.Background(), "app must not be nil", "app_name", appName)

		return ErrNilApp
	}

	l.apps[appName] = a

	return nil
}

// Run executes every application previously registered via Add.
// Maintains backward compatibility — logs errors internally when Logger is
// available. For explicit error handling, use RunWithError instead.
func (l *Launcher) Run() {
	if err := l.RunWithError(); err != nil {
		if !nilcheck.Interface(l.Logger) {
			l.Logger.Log(context.Background(), obs.LevelError, "launcher error", "error", err)
		}
	}
}

// RunWithError runs all registered applications and returns an error if the
// launcher is nil, if Logger is nil, or if configuration errors were collected
// during option application. Safe to call on a Launcher created without
// NewLauncher (fields are lazy-initialized).
func (l *Launcher) RunWithError() error {
	if l == nil {
		return ErrNilLauncher
	}

	// Logger is an exported field, so a caller can bypass WithLogger and assign
	// a typed nil straight into it. Reject that here too.
	if nilcheck.Interface(l.Logger) {
		return ErrLoggerNil
	}

	// Lazy-init guards: safe to use even if constructed without NewLauncher.
	if l.wg == nil {
		l.wg = new(sync.WaitGroup)
	}

	if l.apps == nil {
		l.apps = make(map[string]App)
	}

	// Surface any errors collected during option application.
	if len(l.configErrors) > 0 {
		return errors.Join(append([]error{ErrConfigFailed}, l.configErrors...)...)
	}

	count := len(l.apps)
	l.wg.Add(count)

	l.Logger.Log(context.Background(), obs.LevelInfo, "starting apps", "count", count)

	for name, app := range l.apps {
		nameCopy := name
		appCopy := app

		runtime.SafeGoWithContextAndComponent(
			context.Background(),
			l.Logger,
			"launcher",
			"run_app_"+nameCopy,
			runtime.KeepRunning,
			func(_ context.Context) {
				defer l.wg.Done()

				l.Logger.Log(context.Background(), obs.LevelInfo, "app starting", "app", nameCopy)

				if err := appCopy.Run(l); err != nil {
					l.Logger.Log(context.Background(), obs.LevelError, "app error", "app", nameCopy, "error", err)
				}

				l.Logger.Log(context.Background(), obs.LevelInfo, "app finished", "app", nameCopy)
			},
		)
	}

	l.wg.Wait()

	l.Logger.Log(context.Background(), obs.LevelInfo, "launcher terminated")

	return nil
}

// NewLauncher create an instance of Launch.
func NewLauncher(opts ...LauncherOption) *Launcher {
	l := &Launcher{
		apps:    make(map[string]App),
		wg:      new(sync.WaitGroup),
		Verbose: true,
	}

	for _, opt := range opts {
		opt(l)
	}

	return l
}
