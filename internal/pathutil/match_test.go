package pathutil

import "testing"

func TestIsUnderPath(t *testing.T) {
	tests := []struct {
		name     string
		child    string
		parent   string
		expected bool
	}{
		{"exact match", "/home/user/project", "/home/user/project", true},
		{"subdirectory", "/home/user/project/src", "/home/user/project", true},
		{"deep subdirectory", "/home/user/project/src/pkg/main.go", "/home/user/project", true},
		{"not under - different path", "/home/user/other", "/home/user/project", false},
		{"not under - prefix but not boundary", "/home/user/projectExtra", "/home/user/project", false},
		{"trailing slash parent", "/home/user/project/src", "/home/user/project/", true},
		{"trailing slash child", "/home/user/project/src/", "/home/user/project", true},
		{"both trailing slashes", "/home/user/project/src/", "/home/user/project/", true},
		{"empty child", "", "/home/user/project", false},
		{"empty parent", "/home/user/project", "", false},
		{"both empty", "", "", false},
		{"root parent", "/foo", "/", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsUnderPath(tt.child, tt.parent)
			if got != tt.expected {
				t.Errorf("IsUnderPath(%q, %q) = %v, want %v", tt.child, tt.parent, got, tt.expected)
			}
		})
	}
}
