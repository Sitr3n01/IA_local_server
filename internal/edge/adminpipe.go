package edge

import (
	"context"
	"encoding/json"

	"github.com/sitr3n/local-ai-provider/internal/adminpipe"
)

// adminHandler adapts the transport-neutral administrative operations to the
// named-pipe transport. It adds no policy of its own: every refusal below comes
// from the same code path the deprecated HTTP control plane uses, so the two
// transports cannot drift into different admission rules.
type adminHandler struct {
	server *Server
}

// AdminHandler exposes the administrative operations to a DACL-protected
// transport.
func (s *Server) AdminHandler() adminpipe.Handler {
	return adminHandler{server: s}
}

func (h adminHandler) Execute(ctx context.Context, request adminpipe.Request) (adminpipe.Result, *adminpipe.Error) {
	h.server.metrics.pipeAdminMutations.Add(1)

	switch request.Operation {
	case adminpipe.OperationDrain, adminpipe.OperationResume:
		operation := "drain"
		if request.Operation == adminpipe.OperationResume {
			operation = "resume"
		}
		payload := h.server.performMaintenance(operation)
		encoded, err := json.Marshal(payload["maintenance"])
		if err != nil {
			return adminpipe.Result{}, &adminpipe.Error{
				Code:       "internal_error",
				Message:    "maintenance state could not be encoded",
				HTTPStatus: 500,
			}
		}
		return adminpipe.Result{
			Operation:   request.Operation,
			Status:      "completed",
			Maintenance: encoded,
		}, nil
	case adminpipe.OperationLoad, adminpipe.OperationUnload, adminpipe.OperationSwitch:
		result, failure := h.server.performModelControl(ctx, request.ModelID, request.Operation)
		if failure != nil {
			return adminpipe.Result{}, &adminpipe.Error{
				Code:       failure.Code,
				Message:    failure.Message,
				HTTPStatus: failure.Status,
			}
		}
		return adminpipe.Result{
			Operation:   result.Operation,
			Model:       result.Model,
			Status:      result.Status,
			ActiveModel: result.ActiveModel,
		}, nil
	default:
		// Unreachable through ServeConn, which validates first. Kept because a
		// handler that guesses at an unknown verb is worse than one that refuses.
		return adminpipe.Result{}, &adminpipe.Error{
			Code:       "unknown_operation",
			Message:    "administrative operation is not supported",
			HTTPStatus: 404,
		}
	}
}
