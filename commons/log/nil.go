package log

// NoneLogger is a wrapper for log nothing.
type NoneLogger struct{}

// Info implements Info Logger interface function.
func (l *NoneLogger) Info(_ ...any) {}

// Infof implements Infof Logger interface function.
func (l *NoneLogger) Infof(_ string, _ ...any) {}

// Infoln implements Infoln Logger interface function.
func (l *NoneLogger) Infoln(_ ...any) {}

// Error implements Error Logger interface function.
func (l *NoneLogger) Error(_ ...any) {}

// Errorf implements Errorf Logger interface function.
func (l *NoneLogger) Errorf(_ string, _ ...any) {}

// Errorln implements Errorln Logger interface function.
func (l *NoneLogger) Errorln(_ ...any) {}

// Warn implements Warn Logger interface function.
func (l *NoneLogger) Warn(_ ...any) {}

// Warnf implements Warnf Logger interface function.
func (l *NoneLogger) Warnf(_ string, _ ...any) {}

// Warnln implements Warnln Logger interface function.
func (l *NoneLogger) Warnln(_ ...any) {}

// Debug implements Debug Logger interface function.
func (l *NoneLogger) Debug(_ ...any) {}

// Debugf implements Debugf Logger interface function.
func (l *NoneLogger) Debugf(_ string, _ ...any) {}

// Debugln implements Debugln Logger interface function.
func (l *NoneLogger) Debugln(_ ...any) {}

// Fatal implements Fatal Logger interface function.
func (l *NoneLogger) Fatal(_ ...any) {}

// Fatalf implements Fatalf Logger interface function.
func (l *NoneLogger) Fatalf(_ string, _ ...any) {}

// Fatalln implements Fatalln Logger interface function.
func (l *NoneLogger) Fatalln(_ ...any) {}

// WithFields implements WithFields Logger interface function
//
//nolint:ireturn
func (l *NoneLogger) WithFields(_ ...any) Logger {
	return l
}

// WithDefaultMessageTemplate sets the default message template for the logger.
//
//nolint:ireturn
func (l *NoneLogger) WithDefaultMessageTemplate(_ string) Logger {
	return l
}

// Sync implements Sync Logger interface function.
//
//nolint:ireturn
func (l *NoneLogger) Sync() error { return nil }
