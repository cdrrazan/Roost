// Package detect inspects an application directory and infers its
// framework, port, start command, database need, and runtime version.
// Detection is explainable: every result names the signal that
// triggered it, and failure to detect is an explicit error, never a
// silent guess.
package detect

import "errors"

// Detection is the result of inspecting one app directory.
type Detection struct {
	// Framework is one of: rails, sinatra, next, node, django, static.
	Framework string
	// Signal is the human-readable rule that triggered the detection,
	// e.g. "Gemfile + config/application.rb".
	Signal string
	// Port is the framework's default port inside the container.
	Port int
	// StartCommand is the framework default start command, binding to
	// 0.0.0.0 explicitly. Empty for static sites (Caddy serves them).
	StartCommand string
	// Database is "mysql", "postgres", or "" when no database need was
	// detected.
	Database string
	// RuntimeVersion is the language runtime version inferred from
	// .ruby-version, the Gemfile ruby line, .node-version, or
	// package.json engines. Empty when nothing declares one.
	RuntimeVersion string
}

var errNotImplemented = errors.New("not implemented")

// Detect inspects dir and returns what it found. It returns an error
// naming the directory when no rule matches, telling the user to set
// framework: explicitly.
func Detect(dir string) (Detection, error) {
	return Detection{}, errNotImplemented
}
