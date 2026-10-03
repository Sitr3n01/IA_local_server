//go:build !windows

package monitor

// The administrative pipe exists on Windows only, and so does the native
// confirmation; elsewhere the controls report themselves unavailable and this
// approver is never reached. It declines, so nothing could ever run unconfirmed.
type decliningApprover struct{}

func newApprover() approver { return decliningApprover{} }

func (decliningApprover) approve(approvalRequest) approval { return approvalDeclined }
