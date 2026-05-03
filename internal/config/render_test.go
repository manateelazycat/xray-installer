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
		PublicIP:   "1.2.3.4",
		DestHost:   DefaultDestHost,
		Port:       DefaultPort,
	}
}

func TestRenderXray(t *testing.T) {
	data, err := RenderXray(sampleParameters())
	if err != nil {
		t.Fatalf("RenderXray returned error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("rendered xray config is not valid json: %v", err)
	}

	text := string(data)
	for _, want := range []string{
		`"id": "12345678-1234-1234-1234-1234567890ab"`,
		`"privateKey": "private-key"`,
		`"shortIds": [`,
		`"www.microsoft.com:443"`,
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

	text := string(data)
	if !strings.Contains(text, "server: 1.2.3.4") {
		t.Fatalf("rendered flclash config should use public IP as server:\n%s", text)
	}
	if strings.Contains(text, "server: xx.example.com") {
		t.Fatalf("rendered flclash config should not use domain as server:\n%s", text)
	}
}
