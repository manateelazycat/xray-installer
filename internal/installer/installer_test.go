package installer

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xray-installer/internal/config"
)

func TestSupportsDistro(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
		like string
		want bool
	}{
		{name: "ubuntu", id: "ubuntu", want: true},
		{name: "debian", id: "debian", want: true},
		{name: "archlinux id", id: "archlinux", want: true},
		{name: "arch id", id: "arch", want: true},
		{name: "arch like", id: "manjaro", like: "archlinux", want: true},
		{name: "unsupported", id: "alpine", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := supportsDistro(tt.id, tt.like); got != tt.want {
				t.Fatalf("supportsDistro(%q, %q) = %v, want %v", tt.id, tt.like, got, tt.want)
			}
		})
	}
}

func TestPrintSummaryIncludesFlClashCommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	inst := New(&stdout, &stdout)
	inst.printSummary(&Result{
		Domain:          "xx.example.com",
		NodeName:        DefaultNodeName,
		PublicIP:        "1.2.3.4",
		Port:            443,
		UUID:            "uuid",
		PublicKey:       "pub",
		ShortID:         "short",
		XrayConfigPath:  xrayConfigPath,
		ProxyConfigPath: proxyConfigPath,
	})

	if !strings.Contains(stdout.String(), "sudo cat "+proxyConfigPath) {
		t.Fatalf("summary missing flclash fetch command: %q", stdout.String())
	}
}

func TestPrintDryRunSummaryIncludesFlClashCommandHint(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	inst := New(&stdout, &stdout)
	inst.printDryRunSummary(&Result{
		Domain:          "xx.example.com",
		NodeName:        DefaultNodeName,
		PublicIP:        "1.2.3.4",
		XrayConfigPath:  xrayConfigPath,
		ProxyConfigPath: proxyConfigPath,
	})

	if !strings.Contains(stdout.String(), "sudo cat "+proxyConfigPath) {
		t.Fatalf("dry-run summary missing flclash fetch command hint: %q", stdout.String())
	}
}

func TestWriteFileAtomicUsesRequestedMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "config.json")
	if err := writeFileAtomic(target, []byte(`{"ok":true}`), xrayConfigMode, -1, -1); err != nil {
		t.Fatalf("writeFileAtomic returned error: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if got := info.Mode().Perm(); got != xrayConfigMode {
		t.Fatalf("file mode = %o, want %o", got, xrayConfigMode)
	}
}

func TestReplaceConfigurationRollsBackAfterActivationFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	serverPath := filepath.Join(dir, "config.json")
	clientPath := filepath.Join(dir, "proxy.yaml")
	if err := os.WriteFile(serverPath, []byte("old server"), xrayConfigMode); err != nil {
		t.Fatal(err)
	}

	startErr := errors.New("simulated failure to start Xray")
	restoredService := false
	err := replaceConfiguration([]configurationFile{
		{path: serverPath, data: []byte("new server"), mode: xrayConfigMode, group: -1},
		{path: clientPath, data: []byte("new client"), mode: privateMode, group: -1},
	}, func() error {
		return startErr
	}, func() error {
		restoredService = true
		return nil
	})
	if !errors.Is(err, startErr) || !restoredService {
		t.Fatalf("replaceConfiguration returned %v, restored service = %v", err, restoredService)
	}
	data, err := os.ReadFile(serverPath)
	if err != nil || string(data) != "old server" {
		t.Fatalf("server config was not restored: %q, %v", data, err)
	}
	if _, err := os.Stat(clientPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new client config should be removed, stat returned %v", err)
	}
}

func TestHysteriaCertificateMatchesClientFingerprint(t *testing.T) {
	t.Parallel()

	const domain = "vps.example.net"
	now := time.Now().UTC()
	params, err := newHysteriaParameters(domain, now)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(params.Certificate), []byte(params.Key))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(pair.Certificate[0])
	if got := hex.EncodeToString(hash[:]); got != params.Fingerprint {
		t.Fatal("the client fingerprint does not identify the server TLS certificate")
	}
	roots := x509.NewCertPool()
	roots.AddCert(pair.Leaf)
	if _, err := pair.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: domain, CurrentTime: now}); err != nil {
		t.Fatalf("verify certificate for %s: %v", domain, err)
	}
}

func TestNormalizeDestHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{
			name:  "empty uses default",
			input: "",
			want:  config.DefaultDestHost,
		},
		{
			name:  "space uses default",
			input: "   ",
			want:  config.DefaultDestHost,
		},
		{
			name:  "custom domain normalized",
			input: "WWW.Apple.COM.",
			want:  "www.apple.com",
		},
		{
			name:    "invalid domain",
			input:   "not a domain",
			wantErr: "invalid domain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeDestHost(tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("normalizeDestHost error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeDestHost returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeDestHost = %q, want %q", got, tt.want)
			}
		})
	}
}
