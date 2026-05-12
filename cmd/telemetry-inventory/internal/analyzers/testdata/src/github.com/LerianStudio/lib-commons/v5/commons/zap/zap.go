package zap

type Field struct{}

func String(string, string) Field { return Field{} }
func Int(string, int) Field       { return Field{} }
func Bool(string, bool) Field     { return Field{} }
func Any(string, any) Field       { return Field{} }
func ErrorField(error) Field      { return Field{} }
