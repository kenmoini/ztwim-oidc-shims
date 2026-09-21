// Package spiffehelper renders spiffe-helper HCL configuration files.
//
// It is a pure, stdlib-only package: it has no knowledge of Kubernetes or the
// controller-runtime machinery. Callers (e.g. a mutating pod webhook) build a
// Config and call Render to obtain the file contents to mount into a
// spiffe-helper sidecar/init container.
package spiffehelper

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"
)

// Config is everything needed to render a JWT-only spiffe-helper configuration.
type Config struct {
	// AgentAddress is the full path to the SPIRE agent Workload API socket,
	// e.g. "/spiffe-workload-api/spire-agent.sock".
	AgentAddress string
	// CertDir is the directory spiffe-helper writes files into (the token mountPath).
	CertDir string
	// JWTAudience is the primary audience requested for the JWT-SVID.
	JWTAudience string
	// JWTExtraAudiences are additional audiences (may be empty).
	JWTExtraAudiences []string
	// JWTSVIDFileName is the file name written inside CertDir.
	JWTSVIDFileName string
	// JWTSVIDFileMode is the octal mode string for the token file, e.g. "0644". Empty means omit the key.
	JWTSVIDFileMode string
}

// ModePattern matches an optional leading "0" followed by exactly three octal digits.
// It is the one definition of an acceptable octal file mode; internal/controller reuses
// it so the CRD-level validation and the rendered config agree.
var ModePattern = regexp.MustCompile(`^0?[0-7]{3}$`)

const configTemplate = `agent_address = {{quote .AgentAddress}}
cert_dir = {{quote .CertDir}}
jwt_svids = [{jwt_audience = {{quote .JWTAudience}}, jwt_extra_audiences = [{{.ExtraAudiences}}], jwt_svid_file_name = {{quote .JWTSVIDFileName}}}]
{{- if .JWTSVIDFileMode}}
jwt_svid_file_mode = {{.JWTSVIDFileMode}}
{{- end}}
`

var tmpl = template.Must(template.New("spiffe-helper.conf").Funcs(template.FuncMap{
	"quote": strconv.Quote,
}).Parse(configTemplate))

// templateData is the shape fed to the template; it derives a couple of
// pre-formatted fields from Config so the template stays simple.
type templateData struct {
	Config
	ExtraAudiences string
}

// Render returns the spiffe-helper config file contents for c.
// It returns an error when AgentAddress, CertDir, JWTAudience or JWTSVIDFileName is empty,
// or when JWTSVIDFileMode is set but is not an octal mode matching ^0?[0-7]{3}$.
// A three-digit mode without a leading zero (e.g. "644") is normalised to "0644", because
// HCL would otherwise read the emitted value as a decimal number.
func Render(c Config) (string, error) {
	if c.AgentAddress == "" {
		return "", fmt.Errorf("spiffehelper: AgentAddress must not be empty")
	}
	if c.CertDir == "" {
		return "", fmt.Errorf("spiffehelper: CertDir must not be empty")
	}
	if c.JWTAudience == "" {
		return "", fmt.Errorf("spiffehelper: JWTAudience must not be empty")
	}
	if c.JWTSVIDFileName == "" {
		return "", fmt.Errorf("spiffehelper: JWTSVIDFileName must not be empty")
	}
	if c.JWTSVIDFileMode != "" && !ModePattern.MatchString(c.JWTSVIDFileMode) {
		return "", fmt.Errorf("spiffehelper: JWTSVIDFileMode %q is not a valid octal mode", c.JWTSVIDFileMode)
	}

	quotedExtras := make([]string, len(c.JWTExtraAudiences))
	for i, a := range c.JWTExtraAudiences {
		quotedExtras[i] = strconv.Quote(a)
	}

	// jwt_svid_file_mode is emitted unquoted, and HCL reads a number without a leading
	// zero as decimal: "644" would become 0o1204. Normalise to the octal literal.
	if c.JWTSVIDFileMode != "" && !strings.HasPrefix(c.JWTSVIDFileMode, "0") {
		c.JWTSVIDFileMode = "0" + c.JWTSVIDFileMode
	}

	data := templateData{
		Config:         c,
		ExtraAudiences: strings.Join(quotedExtras, ", "),
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("spiffehelper: render: %w", err)
	}

	return sb.String(), nil
}
