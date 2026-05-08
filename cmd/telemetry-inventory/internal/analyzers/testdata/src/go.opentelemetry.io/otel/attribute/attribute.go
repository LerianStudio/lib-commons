package attribute

type KeyValue struct{}

func String(string, string) KeyValue { return KeyValue{} }
func Int(string, int) KeyValue       { return KeyValue{} }
func Bool(string, bool) KeyValue     { return KeyValue{} }
