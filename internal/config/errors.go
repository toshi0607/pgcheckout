package config

import (
	"errors"
	"regexp"
)

const (
	Usage                 = "usage"
	ConfigMissing         = "config_missing"
	ConfigInvalid         = "config_invalid"
	ConnectFailed         = "connect_failed"
	BaseNotFound          = "base_not_found"
	BaseNotMarked         = "base_not_marked"
	BaseOpen              = "base_open"
	DatabaseInUse         = "database_in_use"
	DatabaseExists        = "database_exists"
	ServerResources       = "server_resources"
	Canceled              = "canceled"
	RefillFailed          = "refill_failed"
	UnmanagedDatabase     = "unmanaged_database"
	UnmanagedFile         = "unmanaged_file"
	BranchNotFound        = "branch_not_found"
	NotGitRepo            = "not_git_repo"
	DetachedHead          = "detached_head"
	InsufficientPrivilege = "insufficient_privilege"
	Internal              = "internal"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Cause   error  `json:"-"`
}

var connectionURL = regexp.MustCompile(`(?i)\x60postgres(?:ql)?://[^\x60\r\n]*\x60|'postgres(?:ql)?://[^'\r\n]*'|"postgres(?:ql)?://[^"\r\n]*"|postgres(?:ql)?://[^[:space:]'"<>\x60]*`)
var connectionPassword = regexp.MustCompile(`(?i)\bpassword[ \t]*=[ \t]*(?:'(?:\\.|[^'\\])*'|"(?:\\.|[^"\\])*"|(?:\\.|[^[:space:]'"])+)?`)

func RedactConnectionDetails(message string) string {
	message = connectionURL.ReplaceAllString(message, "<redacted-url>")
	return connectionPassword.ReplaceAllString(message, "password=<redacted>")
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func NewError(code, message, hint string) *Error {
	return &Error{Code: code, Message: message, Hint: hint}
}

func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code
	}
	return Internal
}
