package tools

import "fmt"

type PermissionDenied struct{ Tool, Message string }

func (e *PermissionDenied) Error() string { return "permission_denied: " + e.Tool + ": " + e.Message }
func (e *Executor) denied(tool, message string) string {
	e.PermissionError = &PermissionDenied{Tool: tool, Message: message}
	return marshal(map[string]any{"ok": false, "code": "permission_denied", "tool": tool, "error": message})
}
func (e *Executor) isPython(name string) bool {
	for _, d := range e.PythonTools {
		if d.Name == name {
			return true
		}
	}
	return false
}
func LoadPythonTools(paths []string) ([]PythonDefinition, error) {
	defs := make([]PythonDefinition, 0, len(paths))
	for _, path := range paths {
		d, err := pythonMetadata(path)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, fmt.Errorf("Python tool %s requires python3 and yolomancer_tool() metadata", path)
		}
		defs = append(defs, *d)
	}
	return defs, nil
}
