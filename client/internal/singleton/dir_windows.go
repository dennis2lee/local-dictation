package singleton

// Dir is accepted and ignored on Windows: the lock is a kernel object with a
// name, not a file in a directory.
func Dir(string) {}
