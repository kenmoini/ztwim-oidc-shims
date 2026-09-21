package spiffehelper

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var update = flag.Bool("update", false, "update golden files")

func fullConfig() Config {
	return Config{
		AgentAddress:      "/spiffe-workload-api/spire-agent.sock",
		CertDir:           "/var/run/secrets/oidcshim/gcp",
		JWTAudience:       "https://iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/x",
		JWTExtraAudiences: []string{"a", "b"},
		JWTSVIDFileName:   "token",
		JWTSVIDFileMode:   "0644",
	}
}

func minimalConfig() Config {
	return Config{
		AgentAddress:    "/spiffe-workload-api/spire-agent.sock",
		CertDir:         "/var/run/secrets/oidcshim/gcp",
		JWTAudience:     "https://iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/x",
		JWTSVIDFileName: "token",
	}
}

func TestRender_Golden(t *testing.T) {
	tests := []struct {
		name   string
		cfg    Config
		golden string
	}{
		{"full", fullConfig(), "testdata/full.golden"},
		{"minimal", minimalConfig(), "testdata/minimal.golden"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render(tt.cfg)
			if err != nil {
				t.Fatalf("Render() returned unexpected error: %v", err)
			}

			if *update {
				if err := os.WriteFile(tt.golden, []byte(got), 0o644); err != nil {
					t.Fatalf("failed to update golden file: %v", err)
				}
			}

			want, err := os.ReadFile(tt.golden)
			if err != nil {
				t.Fatalf("failed to read golden file: %v", err)
			}

			if diff := cmp.Diff(string(want), got); diff != "" {
				t.Errorf("Render() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRender_TrailingNewline(t *testing.T) {
	got, err := Render(fullConfig())
	if err != nil {
		t.Fatalf("Render() returned unexpected error: %v", err)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("Render() output does not end with a newline")
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Errorf("Render() output ends with more than one newline")
	}
}

func TestRender_QuoteEscaping(t *testing.T) {
	cfg := minimalConfig()
	cfg.JWTAudience = `has "quotes" in it`

	got, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render() returned unexpected error: %v", err)
	}

	if !strings.Contains(got, `\"quotes\"`) {
		t.Errorf("Render() output did not escape embedded quotes, got:\n%s", got)
	}
}

func TestRender_ModeNormalisation(t *testing.T) {
	// jwt_svid_file_mode is emitted unquoted, so HCL reads a leading-zero-less "644" as
	// the decimal 644 (0o1204). Render must normalise it.
	tests := []struct {
		mode string
		want string
	}{
		{"644", "jwt_svid_file_mode = 0644\n"},
		{"0644", "jwt_svid_file_mode = 0644\n"},
		{"400", "jwt_svid_file_mode = 0400\n"},
		{"0400", "jwt_svid_file_mode = 0400\n"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			cfg := minimalConfig()
			cfg.JWTSVIDFileMode = tt.mode

			got, err := Render(cfg)
			if err != nil {
				t.Fatalf("Render() returned unexpected error: %v", err)
			}
			if !strings.HasSuffix(got, tt.want) {
				t.Errorf("Render() = %q, want it to end with %q", got, tt.want)
			}
		})
	}

	// Render must not mutate the caller's Config.
	cfg := minimalConfig()
	cfg.JWTSVIDFileMode = "644"
	if _, err := Render(cfg); err != nil {
		t.Fatalf("Render() returned unexpected error: %v", err)
	}
	if cfg.JWTSVIDFileMode != "644" {
		t.Errorf("Render() mutated the caller's Config: JWTSVIDFileMode = %q", cfg.JWTSVIDFileMode)
	}
}

func TestRender_Errors(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "empty AgentAddress",
			cfg: Config{
				CertDir:         "/var/run/secrets/oidcshim/gcp",
				JWTAudience:     "aud",
				JWTSVIDFileName: "token",
			},
		},
		{
			name: "empty CertDir",
			cfg: Config{
				AgentAddress:    "/spiffe-workload-api/spire-agent.sock",
				JWTAudience:     "aud",
				JWTSVIDFileName: "token",
			},
		},
		{
			name: "empty JWTAudience",
			cfg: Config{
				AgentAddress:    "/spiffe-workload-api/spire-agent.sock",
				CertDir:         "/var/run/secrets/oidcshim/gcp",
				JWTSVIDFileName: "token",
			},
		},
		{
			name: "empty JWTSVIDFileName",
			cfg: Config{
				AgentAddress: "/spiffe-workload-api/spire-agent.sock",
				CertDir:      "/var/run/secrets/oidcshim/gcp",
				JWTAudience:  "aud",
			},
		},
		{
			name: "invalid JWTSVIDFileMode non-octal",
			cfg: func() Config {
				c := minimalConfig()
				c.JWTSVIDFileMode = "abc"
				return c
			}(),
		},
		{
			name: "invalid JWTSVIDFileMode out of range digit",
			cfg: func() Config {
				c := minimalConfig()
				c.JWTSVIDFileMode = "0999"
				return c
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Render(tt.cfg); err == nil {
				t.Errorf("Render() expected error, got nil")
			}
		})
	}
}

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}
