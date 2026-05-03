package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"text/template"
)

const (
	DefaultDestHost = "www.microsoft.com"
	DefaultPort     = 443
)

type Parameters struct {
	Domain     string
	NodeName   string
	UUID       string
	PrivateKey string
	PublicKey  string
	ShortID    string
	PublicIP   string
	DestHost   string
	Port       int
}

//go:embed templates/config.json.tmpl
var xrayTemplate string

//go:embed templates/proxy.yaml.tmpl
var flClashTemplate string

func RenderXray(params Parameters) ([]byte, error) {
	data, err := render("xray", xrayTemplate, params)
	if err != nil {
		return nil, err
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		return nil, fmt.Errorf("indent xray config: %w", err)
	}
	pretty.WriteByte('\n')
	return pretty.Bytes(), nil
}

func RenderFlClash(params Parameters) ([]byte, error) {
	return render("flclash", flClashTemplate, params)
}

func render(name, raw string, params Parameters) ([]byte, error) {
	tpl, err := template.New(name).
		Funcs(template.FuncMap{
			"q": quoteString,
		}).
		Option("missingkey=error").
		Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s template: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, params); err != nil {
		return nil, fmt.Errorf("render %s template: %w", name, err)
	}
	return buf.Bytes(), nil
}

func quoteString(value string) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
