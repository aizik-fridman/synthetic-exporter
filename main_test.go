package main

import (
	"testing"
)

func TestResolveSecret(t *testing.T) {
	// Set up environment variable for testing
	t.Setenv("TEST_SECRET_ENV", "my_super_secret_value")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Plain string, no template",
			input:    "hello_world",
			expected: "hello_world",
		},
		{
			name:     "Valid environment variable template",
			input:    "${TEST_SECRET_ENV}",
			expected: "my_super_secret_value",
		},
		{
			name:     "Non-existent environment variable template",
			input:    "${DOES_NOT_EXIST}",
			expected: "",
		},
		{
			name:     "Invalid template format",
			input:    "$TEST_SECRET_ENV",
			expected: "$TEST_SECRET_ENV",
		},
		{
			name:     "Invalid template format with braces but no dollar",
			input:    "{TEST_SECRET_ENV}",
			expected: "{TEST_SECRET_ENV}",
		},
		{
			name:     "Template embedded in string (not supported by exact match)",
			input:    "prefix_${TEST_SECRET_ENV}_suffix",
			expected: "prefix_${TEST_SECRET_ENV}_suffix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveSecret(tt.input)
			if result != tt.expected {
				t.Errorf("resolveSecret(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := loadConfig("non_existent_config.yaml")
	if err == nil {
		t.Error("Expected error when loading non-existent file, got nil")
	}
}
