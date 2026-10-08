package output

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func printer(t *testing.T, format string) (*Printer, *bytes.Buffer) {
	t.Helper()
	var out, errw bytes.Buffer
	p, err := New(&out, &errw, format, false)
	require.NoError(t, err)
	return p, &out
}

func TestJSONKeepsTheBodyAsItCame(t *testing.T) {
	p, out := printer(t, JSON)
	require.NoError(t, p.JSON([]byte(`{"zeta":1,"alpha":{"b":"<x>","a":[1,2]}}`)))
	assert.Equal(t, "{\n  \"zeta\": 1,\n  \"alpha\": {\n    \"b\": \"<x>\",\n"+
		"    \"a\": [\n      1,\n      2\n    ]\n  }\n}\n", out.String())
}

func TestYAMLIsPlainYAML(t *testing.T) {
	p, out := printer(t, YAML)
	require.NoError(t, p.JSON([]byte(`{"name":"api","port":"80","on":"true","empty":"","list":[{"k":"v"}],"none":[]}`)))
	assert.Equal(t, "name: api\nport: \"80\"\non: \"true\"\nempty: \"\"\nlist:\n  - k: v\nnone: []\n", out.String(),
		"block style, and a string quoted only where YAML would read something else")
}

func TestDataWritesTheValue(t *testing.T) {
	p, out := printer(t, JSON)
	require.NoError(t, p.Data(struct {
		ID   string `json:"id"`
		Link string `json:"link"`
	}{"A1", "https://x/?a=1&b=2"}))
	assert.Equal(t, "{\n  \"id\": \"A1\",\n  \"link\": \"https://x/?a=1&b=2\"\n}\n", out.String())
}

func TestNotJSONIsWrittenAsItIs(t *testing.T) {
	p, out := printer(t, JSON)
	require.NoError(t, p.JSON([]byte("plain text")))
	assert.Equal(t, "plain text\n", out.String())
}

func TestNoColorsOffATerminal(t *testing.T) {
	p, _ := printer(t, Table)
	assert.Equal(t, "id", p.Dim("id"))
	assert.Equal(t, "id", p.DimErr("id"))
}

func TestNewRefusesAnUnknownFormat(t *testing.T) {
	_, err := New(nil, nil, "xml", false)
	assert.Error(t, err)
}
