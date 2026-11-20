package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func sampleParameters() Parameters {
	return Parameters{
		Domain:     "xx.example.com",
		NodeName:   `测试节点 "A"`,
		UUID:       "12345678-1234-1234-1234-1234567890ab",
		PrivateKey: "private-key",
		PublicKey:  "public-key",
		ShortID:    "0123456789abcdef",
		DestHost:   "www.apple.com",
		Port:       DefaultPort,
		Hysteria: HysteriaParameters{
			NodeName:    "测试节点 Hysteria2",
			Password:    "hysteria-password",
			Certificate: "certificate-pem\n",
			Key:         "tls-key-pem\n",
			Fingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
	}
}

func TestRenderXray(t *testing.T) {
	data, err := RenderXray(sampleParameters())
	if err != nil {
		t.Fatalf("RenderXray returned error: %v", err)
	}

	var parsed struct {
		Inbounds []struct {
			Port     int
			Protocol string
			Settings struct {
				Version int
				Users   []struct{ Auth string }
			}
			StreamSettings struct {
				Network     string
				Security    string
				TLSSettings struct {
					ALPN         []string
					Certificates []struct {
						Certificate []string
						Key         []string
					}
				}
			}
		}
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("rendered xray config is not valid json: %v", err)
	}

	if len(parsed.Inbounds) != 2 {
		t.Fatalf("expected two inbounds, got %d", len(parsed.Inbounds))
	}
	reality, hysteria := parsed.Inbounds[0], parsed.Inbounds[1]
	if reality.Protocol != "vless" || reality.StreamSettings.Network != "tcp" || reality.Port != DefaultPort {
		t.Fatal("expected a VLESS TCP inbound on the default port")
	}
	if hysteria.Protocol != "hysteria" || hysteria.StreamSettings.Network != "hysteria" || hysteria.Port != DefaultPort ||
		hysteria.StreamSettings.Security != "tls" || hysteria.Settings.Version != 2 {
		t.Fatal("expected a TLS Hysteria 2 inbound on the same port")
	}
	params := sampleParameters().Hysteria
	if len(hysteria.Settings.Users) != 1 || hysteria.Settings.Users[0].Auth != params.Password {
		t.Fatal("Hysteria 2 password did not reach the inbound")
	}
	tls := hysteria.StreamSettings.TLSSettings
	if strings.Join(tls.ALPN, ",") != "h3" || len(tls.Certificates) != 1 ||
		strings.Join(tls.Certificates[0].Certificate, "") != params.Certificate ||
		strings.Join(tls.Certificates[0].Key, "") != params.Key {
		t.Fatal("Hysteria 2 TLS settings are incomplete")
	}

	text := string(data)
	for _, want := range []string{
		`"id": "12345678-1234-1234-1234-1234567890ab"`,
		`"privateKey": "private-key"`,
		`"shortIds": [`,
		`"www.apple.com:443"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered xray config missing %q", want)
		}
	}
}

func TestRenderFlClash(t *testing.T) {
	data, err := RenderFlClash(sampleParameters())
	if err != nil {
		t.Fatalf("RenderFlClash returned error: %v", err)
	}

	want, err := os.ReadFile("testdata/proxy.yaml.golden")
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}

	if string(data) != string(want) {
		t.Fatalf("rendered flclash config mismatch.\n--- got ---\n%s\n--- want ---\n%s", string(data), string(want))
	}
}
