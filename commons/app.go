package commons

import (
	"errors"
	"sync"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
)

// ErrLoggerNil is returned when the Logger is nil and cannot proceed.
var ErrLoggerNil = errors.New("logger is nil")

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
func WithLogger(logger log.Logger) LauncherOption {
	return func(l *Launcher) {
		l.Logger = logger
	}
}

// RunApp start all process registered before to the launcher.
func RunApp(name string, app App) LauncherOption {
	return func(l *Launcher) {
		l.Add(name, app)
	}
}

// Launcher manages apps.
type Launcher struct {
	Logger  log.Logger
	apps    map[string]App
	wg      *sync.WaitGroup
	Verbose bool
}

// Add runs an application in a goroutine.
func (l *Launcher) Add(appName string, a App) *Launcher {
	l.apps[appName] = a
	return l
}

// Run every application registered before with Run method.
// Returns an error if Logger is nil.
func (l *Launcher) Run() error {
	if l.Logger == nil {
		return ErrLoggerNil
	}

	count := len(l.apps)
	l.wg.Add(count)

	l.Logger.Infof("Starting %d app(s)\n", count)

	for name, app := range l.apps {
		go func(name string, app App) {
			defer l.wg.Done()

			l.Logger.Info("--")
			l.Logger.Infof("Launcher: App \u001b[33m(%s)\u001b[0m starting\n", name)

			if err := app.Run(l); err != nil {
				l.Logger.Infof("Launcher: App (%s) error:", name)
				l.Logger.Infof("\u001b[31m%s\u001b[0m", err)
			}

			l.Logger.Infof("Launcher: App (%s) finished\n", name)
		}(name, app)
	}

	l.wg.Wait()

	l.Logger.Info("Launcher: Terminated")

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
