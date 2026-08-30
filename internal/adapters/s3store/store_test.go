package s3store

import "testing"

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		input  string
		host   string
		secure bool
		ok     bool
	}{
		{input: "https://s3.example.com", host: "s3.example.com", secure: true, ok: true},
		{input: "http://127.0.0.1:9000", host: "127.0.0.1:9000", secure: false, ok: true},
		{input: "s3.example.com", host: "s3.example.com", secure: true, ok: true},
		{input: "ftp://s3.example.com", ok: false},
		{input: "https://s3.example.com/path", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			host, secure, err := parseEndpoint(tt.input)
			if (err == nil) != tt.ok {
				t.Fatalf("parseEndpoint() error = %v", err)
			}
			if tt.ok && (host != tt.host || secure != tt.secure) {
				t.Fatalf("parseEndpoint() = %q, %v", host, secure)
			}
		})
	}
}

func TestValidateObjectKey(t *testing.T) {
	for _, key := range []string{"tenant/kb/document/file.md", "a-b_c/1.txt"} {
		if err := validateObjectKey(key); err != nil {
			t.Fatalf("validateObjectKey(%q) error = %v", key, err)
		}
	}
	for _, key := range []string{"", "/absolute", "../escape", "a/../b", "a//b"} {
		if err := validateObjectKey(key); err == nil {
			t.Fatalf("validateObjectKey(%q) error = nil", key)
		}
	}
}

func TestNormalizeSHA256(t *testing.T) {
	valid := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got := normalizeSHA256(valid); got != valid {
		t.Fatalf("normalizeSHA256() = %q", got)
	}
	if got := normalizeSHA256("invalid"); got != "" {
		t.Fatalf("normalizeSHA256(invalid) = %q", got)
	}
}
