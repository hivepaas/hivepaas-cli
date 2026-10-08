package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"paas.example.com":              "https://paas.example.com",
		"paas.example.com:8443/":        "https://paas.example.com:8443",
		"https://paas.example.com/api/": "https://paas.example.com",
		"http://paas.example.com":       "http://paas.example.com",
		"localhost:10000":               "http://localhost:10000",
		"LOCALHOST:10000/api":           "http://LOCALHOST:10000",
		"paas.localhost":                "http://paas.localhost",
		"127.0.0.1:10000":               "http://127.0.0.1:10000",
		"[::1]:10000":                   "http://[::1]:10000",
		"https://localhost:10443":       "https://localhost:10443",
		"localhost.example.com":         "https://localhost.example.com",
		"10.0.0.5:10000":                "https://10.0.0.5:10000",
		"  paas.example.com  ":          "https://paas.example.com",
	} {
		assert.Equal(t, want, NormalizeURL(in), in)
	}
}
