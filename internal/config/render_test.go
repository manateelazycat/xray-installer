package config

import (
	"encoding/json"
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

	text := string(data)
	for _, want := range []string{
		"##############################################",
		`server: xx.example.com`,
		`servername: www.microsoft.com`,
		`name: "测试节点 \"A\""`,
		`{name: 节点选择, type: select, proxies: [自建/家宽节点, 全部节点, CK 自用订阅请勿分享外泄]`,
		`{name: 自建/家宽节点, type: select, include-all: true, filter: "(?=.*(?i)(自建|CF|The_house|private|home|家宽|hgc|HKT|HKBN|icable|Hinet|att))"`,
		`{name: 反重力, type: select, include-all: true, filter: "(?=.*(?i)(自建|CF|The_house|private|home|家宽|hgc|HKT|HKBN|icable|Hinet|att))"`,
		`{name: 🔗 代理, type: select, hidden: true, proxies: [节点选择]}`,
		`uuid: "12345678-1234-1234-1234-1234567890ab"`,
		`public-key: "public-key"`,
		`short-id: "0123456789abcdef"`,
		"proxy-groups:",
		"rule-providers:",
		"GEOSITE,openai,节点选择",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered flclash config missing %q", want)
		}
	}

	for _, bad := range []string{"{{ .Domain }}", "{{ .UUID }}", "{{ .PublicKey }}", "{{ .ShortID }}", `server: "xx.example.com"`, `servername: "www.microsoft.com"`, `{name: "🔗 代理"`} {
		if strings.Contains(text, bad) {
			t.Fatalf("rendered flclash config still contains placeholder %q", bad)
		}
	}
}
