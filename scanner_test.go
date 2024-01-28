package main

import (
	"reflect"
	"strings"
	"testing"
)

// mockReadCloser is a helper type for mocking io.ReadCloser
type mockReadCloser struct {
	reader *strings.Reader
}

func newMockReadCloser(content string) *mockReadCloser {
	return &mockReadCloser{reader: strings.NewReader(content)}
}

func (mrc *mockReadCloser) Read(p []byte) (n int, err error) {
	return mrc.reader.Read(p)
}

func (mrc *mockReadCloser) Close() error {
	return nil
}

// TestParseProcfile tests the parseProcfile function with various procfile contents.
func TestParseProcfile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []entry
		wantErr bool
	}{
		{
			name:    "Valid procfile",
			content: "web: npm start\nworker: npm run worker",
			want: []entry{
				{name: "web", cmd: "npm start"},
				{name: "worker", cmd: "npm run worker"},
			},
			wantErr: false,
		},
		{
			name:    "Empty procfile",
			content: "",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Invalid format",
			content: "web npm start",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Duplicate names",
			content: "web: npm start\nweb: npm run something",
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock the file reading
			mockFile := newMockReadCloser(tt.content)
			got, err := parseProcfile(mockFile)

			// Check for expected error
			if (err != nil) != tt.wantErr {
				t.Errorf("parseProcfile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			// Check for expected result
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseProcfile() = %v, want %v", got, tt.want)
			}
		})
	}
}
