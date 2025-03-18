package commons

import (
	"github.com/LerianStudio/lib-commons/commons/log"
	"go.uber.org/mock/gomock"
	"sync"
	"testing"
)

func TestWithLogger(t *testing.T) {
	WithLogger(nil)
}

func TestRunApp(t *testing.T) {
	RunApp("test app", nil)
}

func TestLauncher_Add(t *testing.T) {
	l := &Launcher{
		apps: map[string]App{
			"test": nil,
		},
	}
	l.Add("test app", nil)
}

func TestLauncherRun(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockApp := NewMockApp(ctrl)
	mockApp2 := NewMockApp(ctrl)
	mockLogger := log.NewMockLogger(ctrl)

	launcherInstance := &Launcher{
		apps: map[string]App{
			"app1": mockApp,
			"app2": mockApp2,
		},
		Logger: mockLogger,
		wg:     &sync.WaitGroup{},
	}

	mockLogger.EXPECT().Infof("Starting %d app(s)\n", 2).Times(1)
	mockLogger.EXPECT().Info("--").Times(2)
	mockLogger.EXPECT().Infof("Launcher: App \u001b[33m(%s)\u001b[0m starting\n", gomock.Any()).Times(2)
	mockLogger.EXPECT().Infof("Launcher: App (%s) finished\n", gomock.Any()).Times(2)
	mockLogger.EXPECT().Info("Launcher: Terminated").Times(1)

	mockApp.EXPECT().Run(launcherInstance).Return(nil).Times(1)
	mockApp2.EXPECT().Run(launcherInstance).Return(nil).Times(1)

	launcherInstance.Run()
}

func TestNewLauncher(t *testing.T) {
	t.Log(NewLauncher(func(l *Launcher) {}))
}
