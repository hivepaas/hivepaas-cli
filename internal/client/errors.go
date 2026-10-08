package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// APIError is what the API answers a request it does not serve with: its
// status and its ErrorInfo.
type APIError struct {
	Status int
	Info   api.HperrorsErrorInfo
}

func (e *APIError) Error() string {
	detail := e.Info.Detail
	if e.Info.Code == codeUnauthorized {
		// The API's detail for it is "Unauthorized", which reads as a key that
		// was refused; the key was accepted, and the action was not.
		detail = "not allowed: the API key, or its user, may not do this"
	}
	if detail == "" {
		detail = e.Info.Title
	}
	if detail == "" {
		detail = http.StatusText(e.Status)
	}
	var b strings.Builder
	b.WriteString(detail)
	if e.Info.Code != "" {
		fmt.Fprintf(&b, " (%s)", e.Info.Code)
	}
	if e.Info.Errors != nil {
		for _, inner := range *e.Info.Errors {
			fmt.Fprintf(&b, "\n  %s: %s", inner.Path, inner.Message)
		}
	}
	return b.String()
}

// codeUnauthorized is the API's 401 for an action the caller may not take,
// as against one for a caller it does not know.
const codeUnauthorized = "ERR_UNAUTHORIZED"

// authCodes are the 401s that refuse the caller itself: no session, a key that
// is not valid, a user who cannot log in. The API answers 401 to an action the
// caller may not take as well, which is exit 4.
var authCodes = []string{"ERR_NO_SESSION", "ERR_API_KEY_INVALID", "ERR_USER_UNAVAILABLE", "ERR_SSO_REQUIRED"}

func (e *APIError) isAuth() bool {
	return e.Info.Code == "" || strings.HasPrefix(e.Info.Code, "ERR_SESSION_") || slices.Contains(authCodes, e.Info.Code)
}

// ExitCode is how the CLI exits on this error: section 7 of the design.
func (e *APIError) ExitCode() int {
	switch {
	case e.Status == http.StatusUnauthorized && e.isAuth():
		return exitcode.Auth
	case e.Status == http.StatusUnauthorized:
		return exitcode.Forbidden
	case e.Status == http.StatusForbidden:
		return exitcode.Forbidden
	case e.Status == http.StatusNotFound:
		return exitcode.NotFound
	case e.Status == http.StatusUpgradeRequired:
		return exitcode.CLIOutdated
	case e.Status >= http.StatusInternalServerError:
		return exitcode.Server
	case e.Status >= http.StatusBadRequest:
		return exitcode.Invalid
	}
	return exitcode.Failure
}

// Check turns the outcome of a generated call into an error: the transport's,
// or the API's for a status that is not a success. resp is the generated
// response, whose HTTPResponse and Body it reads.
func Check(resp any, err error) error {
	if err != nil {
		return unreachable(err)
	}
	value := reflect.ValueOf(resp)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return exitcode.New(exitcode.Server, "the server answered nothing")
	}
	httpResp, _ := value.Elem().FieldByName("HTTPResponse").Interface().(*http.Response)
	body, _ := value.Elem().FieldByName("Body").Interface().([]byte)
	if httpResp == nil {
		return exitcode.New(exitcode.Server, "the server answered nothing")
	}
	return CheckStatus(httpResp.StatusCode, body)
}

// CheckStatus is the API's error for a status and body, or nil for a success.
func CheckStatus(status int, body []byte) error {
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return nil
	}
	apiErr := &APIError{Status: status}
	if json.Unmarshal(body, &apiErr.Info) != nil {
		apiErr.Info = leadingErrorInfo(body)
	}
	if apiErr.Info.Code == "" {
		apiErr.Info.Detail = strings.TrimSpace(string(body))
		if len(apiErr.Info.Detail) > maxRawDetail {
			apiErr.Info.Detail = apiErr.Info.Detail[:maxRawDetail] + "..."
		}
	}
	return apiErr
}

const maxRawDetail = 300

// leadingErrorInfo is what can be read of an ErrorInfo that was cut short - a
// websocket handshake keeps the first kilobyte of a refusal, and a server's
// stack trace runs past it: the fields before the cut, of which code and detail
// come first.
func leadingErrorInfo(body []byte) api.HperrorsErrorInfo {
	var info api.HperrorsErrorInfo
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return info
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return info
		}
		var value any
		if err = dec.Decode(&value); err != nil {
			return info
		}
		text, _ := value.(string)
		switch key {
		case "code":
			info.Code = text
		case "detail":
			info.Detail = text
		case "title":
			info.Title = text
		case "status":
			if n, ok := value.(float64); ok {
				info.Status = int(n)
			}
		}
	}
	return info
}

func unreachable(err error) error {
	var exitErr *exitcode.Error
	if errors.As(err, &exitErr) {
		return err
	}
	return exitcode.Wrap(exitcode.Server, &unreachableError{err: err})
}

// unreachableError is a request the server never answered.
type unreachableError struct{ err error }

func (e *unreachableError) Error() string {
	if errors.Is(e.err, http.ErrSchemeMismatch) {
		// Not tried again over http on its own: the key's secret would cross the
		// network unencrypted, which is for the person to choose.
		host := "the server"
		var urlErr *url.Error
		if errors.As(e.err, &urlErr) {
			if u, err := url.Parse(urlErr.URL); err == nil && u.Host != "" {
				host = u.Host
			}
		}
		return fmt.Sprintf("%s answers plain HTTP, not HTTPS: use http://%s if that is the server you mean - "+
			"the key's secret then crosses the network unencrypted", host, host)
	}
	return "reaching the server: " + e.err.Error()
}
func (e *unreachableError) Unwrap() error { return e.err }

// IsUnreachable says err is a request the server never answered: it was not
// reached, or the connection dropped.
func IsUnreachable(err error) bool {
	var u *unreachableError
	return errors.As(err, &u)
}
