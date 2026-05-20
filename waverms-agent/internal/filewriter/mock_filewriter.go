package filewriter

import "os"

// WriteCall records a single call to MockFileWriter.WriteFile.
type WriteCall struct {
	Path    string
	Content []byte
	Perm    os.FileMode
}

// MockFileWriter records all WriteFile calls and can inject errors per path.
type MockFileWriter struct {
	Calls  []WriteCall
	Errors map[string]error
}

// WriteFile records the call and returns any injected error for path.
func (m *MockFileWriter) WriteFile(path string, content []byte, perm os.FileMode) error {
	m.Calls = append(m.Calls, WriteCall{Path: path, Content: content, Perm: perm})
	if m.Errors != nil {
		if err, ok := m.Errors[path]; ok {
			return err
		}
	}
	return nil
}
