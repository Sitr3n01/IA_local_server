//go:build !windows

package adminpipe

import "context"

// The administrative pipe depends on a Windows DACL for authentication. On any
// other platform the transport is absent rather than emulated, because an
// emulation without that DACL would be an unauthenticated mutation channel.

// Listener is the non-Windows placeholder. It can never be constructed.
type Listener struct{}

// SecurityDescriptor is never available off Windows.
func (l *Listener) SecurityDescriptor() string { return "" }

// Name is never available off Windows.
func (l *Listener) Name() string { return "" }

// Accept is never reachable off Windows.
func (l *Listener) Accept() (*Conn, error) { return nil, ErrUnsupported }

// Close is a no-op off Windows.
func (l *Listener) Close() error { return nil }

// Serve is never reachable off Windows.
func (l *Listener) Serve(context.Context, Handler, func(error)) error { return ErrUnsupported }

// Listen refuses to create an administrative transport off Windows.
func Listen(string) (*Listener, error) { return nil, ErrUnsupported }

// Conn is the non-Windows placeholder connection.
type Conn struct{}

// Read is never reachable off Windows.
func (c *Conn) Read([]byte) (int, error) { return 0, ErrUnsupported }

// Write is never reachable off Windows.
func (c *Conn) Write([]byte) (int, error) { return 0, ErrUnsupported }

// Close is a no-op off Windows.
func (c *Conn) Close() error { return nil }

// Dial refuses to reach an administrative transport off Windows.
func Dial(context.Context, DialOptions) (*Conn, error) { return nil, ErrUnsupported }
