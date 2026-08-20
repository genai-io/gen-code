package transport

import (
	"os"
	"slices"
	"testing"
)

func Test_expandEnv(t *testing.T) {
	// Set up test environment variables
	os.Setenv("TEST_VAR", "test_value")
	defer os.Unsetenv("TEST_VAR")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple variable",
			input:    "${TEST_VAR}",
			expected: "test_value",
		},
		{
			name:     "variable with text",
			input:    "prefix-${TEST_VAR}-suffix",
			expected: "prefix-test_value-suffix",
		},
		{
			name:     "undefined variable",
			input:    "${UNDEFINED_VAR}",
			expected: "",
		},
		{
			name:     "default value for undefined",
			input:    "${UNDEFINED_VAR:-default}",
			expected: "default",
		},
		{
			name:     "default value when defined",
			input:    "${TEST_VAR:-default}",
			expected: "test_value",
		},
		{
			name:     "no variables",
			input:    "plain text",
			expected: "plain text",
		},
		{
			name:     "multiple variables",
			input:    "${TEST_VAR} and ${TEST_VAR}",
			expected: "test_value and test_value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandEnv(tt.input)
			if got != tt.expected {
				t.Errorf("expandEnv(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func Test_expandEnvSlice(t *testing.T) {
	os.Setenv("TEST_VAR", "test_value")
	defer os.Unsetenv("TEST_VAR")

	input := []string{"${TEST_VAR}", "plain", "${TEST_VAR:-default}"}
	expected := []string{"test_value", "plain", "test_value"}

	got := expandEnvSlice(input)

	if len(got) != len(expected) {
		t.Fatalf("length mismatch: got %d, want %d", len(got), len(expected))
	}

	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("index %d: got %q, want %q", i, got[i], expected[i])
		}
	}
}

func Test_expandEnvMap(t *testing.T) {
	os.Setenv("TEST_VAR", "test_value")
	defer os.Unsetenv("TEST_VAR")

	input := map[string]string{
		"key1": "${TEST_VAR}",
		"key2": "plain",
		"key3": "${UNDEFINED:-default}",
	}
	expected := map[string]string{
		"key1": "test_value",
		"key2": "plain",
		"key3": "default",
	}

	got := expandEnvMap(input)

	for k, v := range expected {
		if got[k] != v {
			t.Errorf("key %q: got %q, want %q", k, got[k], v)
		}
	}
}

func Test_buildEnv(t *testing.T) {
	configEnv := map[string]string{
		"MY_VAR": "my_value",
	}

	env := buildEnv(configEnv)

	// Check that MY_VAR is in the result
	found := slices.Contains(env, "MY_VAR=my_value")

	if !found {
		t.Error("MY_VAR=my_value not found in result")
	}

	// Check that empty config returns current env
	envEmpty := buildEnv(nil)
	if len(envEmpty) == 0 {
		t.Error("buildEnv(nil) should return current environment")
	}
}
