// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/kushalpandya/wargo/internal/host"
)

// PAMServiceProblem returns "" when dir/service is a regular file, otherwise
// a message telling the operator how to create it.
func PAMServiceProblem(dir, service string) string {
	path := filepath.Join(dir, service)
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
		return ""
	}
	return "Password sign-in needs " + path + ". Run deploy/launchd/install.sh, or copy deploy/pam/wargo-macos there with sudo."
}

// pamProblem reports a missing PAM service file. Only macOS ships no wargo
// service by default, so other platforms never report one.
func (s *Service) pamProblem() string {
	if runtime.GOOS != "darwin" || host.Container() {
		return ""
	}
	return PAMServiceProblem("/etc/pam.d", s.cfg.Auth.PAMService)
}
