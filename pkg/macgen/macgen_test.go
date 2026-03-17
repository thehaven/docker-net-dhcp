package macgen

import (
	"testing"
)

func TestGenerate(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		want    string
		wantErr bool
	}{
		{
			name: "default colon format with simple seed",
			// MD5("test") = 098f6bcd4621d373cade4e832627b4f6
			// First 5 bytes: 09 8f 6b cd 46
			// Prefix: 02
			// Expected: 02:09:8f:6b:cd:46
			opts:    Options{Seed: "test"},
			want:    "02:09:8f:6b:cd:46",
			wantErr: false,
		},
		{
			name: "hyphen format",
			opts:    Options{Seed: "test", Format: FormatHyphen},
			want:    "02-09-8f-6b-cd-46",
			wantErr: false,
		},
		{
			name: "dot format",
			opts:    Options{Seed: "test", Format: FormatDot},
			want:    "0209.8f6b.cd46",
			wantErr: false,
		},
		{
			name: "empty seed error",
			opts:    Options{Seed: ""},
			want:    "",
			wantErr: true,
		},
		{
			name: "another seed",
			// MD5("container-1") = b588c219865f6fe336908e5991216b13
			// First 5 bytes: b5 88 c2 19 86
			// Prefix: 02
			// Expected: 02:b5:88:c2:19:86
			opts:    Options{Seed: "container-1", Format: FormatColon},
			want:    "02:b5:88:c2:19:86",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Generate(tt.opts)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Generate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Generate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGenerateDUID(t *testing.T) {
	tests := []struct {
		name string
		mac  string
		want string // Expecting DUID-LL (type 3), hardware type ethernet (1), plus MAC
	}{
		{
			name: "standard mac",
			mac:  "02:09:8f:6b:cd:46",
			want: "00:03:00:01:02:09:8f:6b:cd:46",
		},
		{
			name: "hyphen mac",
			mac:  "02-c0-c3-6c-b1-2b",
			want: "00:03:00:01:02:c0:c3:6c:b1:2b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GenerateDUID(tt.mac)
			if err != nil {
				t.Fatalf("GenerateDUID() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("GenerateDUID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGenerateDUID_InvalidMAC(t *testing.T) {
	_, err := GenerateDUID("invalid-mac")
	if err == nil {
		t.Error("GenerateDUID() expected error for invalid MAC, got nil")
	}
}
