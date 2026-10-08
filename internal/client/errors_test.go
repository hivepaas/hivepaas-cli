package client

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

func TestCheckStatus(t *testing.T) {
	require.NoError(t, CheckStatus(http.StatusOK, nil))
	require.NoError(t, CheckStatus(http.StatusCreated, nil))

	tests := []struct {
		status int
		body   string
		code   int
		text   string
	}{
		{http.StatusUnauthorized, `{"code":"ERR_API_KEY_INVALID","detail":"API key is invalid"}`, exitcode.Auth,
			"API key is invalid (ERR_API_KEY_INVALID)"},
		{http.StatusUnauthorized, `{"code":"ERR_SESSION_JWT_EXPIRED","detail":"expired"}`, exitcode.Auth, ""},
		{http.StatusUnauthorized, `{"code":"ERR_UNAUTHORIZED","detail":"Unauthorized"}`, exitcode.Forbidden,
			"not allowed: the API key, or its user, may not do this (ERR_UNAUTHORIZED)"},
		{http.StatusUnauthorized, `{"code":"ERR_USER_NOT_HAVE_PERMISSION_ON_RESOURCE","detail":"no"}`,
			exitcode.Forbidden, ""},
		{http.StatusForbidden, `{"code":"ERR_FORBIDDEN"}`, exitcode.Forbidden, ""},
		{http.StatusNotFound, `{"code":"ERR_APP_NOT_FOUND","detail":"App 'x' is not found"}`, exitcode.NotFound, ""},
		{http.StatusConflict, `{"code":"ERR_UPDATE_VER_MISMATCHED"}`, exitcode.Invalid, ""},
		{http.StatusUnprocessableEntity, `{"code":"ERR_VALIDATION","detail":"invalid",
			"errors":[{"path":"name","message":"is required"}]}`, exitcode.Invalid,
			"invalid (ERR_VALIDATION)\n  name: is required"},
		{http.StatusUpgradeRequired, `{"code":"ERR_CLI_OUTDATED","detail":"update"}`, exitcode.CLIOutdated, ""},
		{http.StatusBadGateway, `<html>bad gateway</html>`, exitcode.Server, "<html>bad gateway</html>"},
	}
	for _, tt := range tests {
		err := CheckStatus(tt.status, []byte(tt.body))
		var apiErr *APIError
		require.ErrorAs(t, err, &apiErr, tt.body)
		assert.Equal(t, tt.code, exitcode.Of(err), tt.body)
		if tt.text != "" {
			assert.Equal(t, tt.text, err.Error())
		}
	}
}

func TestUnreachable(t *testing.T) {
	err := Check(nil, errors.New("dial tcp: connection refused"))
	assert.True(t, IsUnreachable(err))
	assert.Equal(t, exitcode.Server, exitcode.Of(err))
	assert.Equal(t, "reaching the server: dial tcp: connection refused", err.Error())
	assert.False(t, IsUnreachable(CheckStatus(http.StatusBadGateway, nil)))
}
