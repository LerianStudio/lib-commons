package commons

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/LerianStudio/lib-commons/v5/commons/assert"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/runtime"
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

// WithLogger adds a log.Logger component to launcher.
// If the launcher is nil the option is a no-op, preventing panics when
// option closures are invoked on a nil receiver.
func WithLogger(logger log.Logger) LauncherOption {
	return func(l *Launcher) {
		if l == nil {
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

			if l.Logger != nil {
				l.Logger.Log(context.Background(), log.LevelError, "launcher add app error", log.Err(err))
			}
		}
	}
}

// Launcher manages apps.
type Launcher struct {
	Logger       log.Logger
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
		if l.Logger != nil {
			l.Logger.Log(context.Background(), log.LevelError, "launcher error", log.Err(err))
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

	if l.Logger == nil {
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

	l.Logger.Log(context.Background(), log.LevelInfo, "starting apps", log.Int("count", count))

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

				l.Logger.Log(context.Background(), log.LevelInfo, "app starting", log.String("app", nameCopy))

				if err := appCopy.Run(l); err != nil {
					l.Logger.Log(context.Background(), log.LevelError, "app error", log.String("app", nameCopy), log.Err(err))
				}

				l.Logger.Log(context.Background(), log.LevelInfo, "app finished", log.String("app", nameCopy))
			},
		)
	}

	l.wg.Wait()

	l.Logger.Log(context.Background(), log.LevelInfo, "launcher terminated")

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
