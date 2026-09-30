//go:build !windows

package main

import (
	"context"
	"errors"
)

// The server's scheduled tasks and the monitor exist only on Windows.
type unsupportedServerControl struct{}

func newServerControl(string, string) (serverControl, error) { return unsupportedServerControl{}, nil }

var errUnsupported = errors.New("server control is supported only on Windows")

func (unsupportedServerControl) Start(context.Context) error     { return errUnsupported }
func (unsupportedServerControl) Stop(context.Context) error      { return errUnsupported }
func (unsupportedServerControl) OpenPanel(context.Context) error { return errUnsupported }
