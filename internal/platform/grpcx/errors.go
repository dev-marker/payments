// Package grpcx is the gRPC plumbing every service shares: interceptors, and
// one consistent way to turn our errors into status codes. It's httpx, for
// gRPC.
package grpcx

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/platform/idempotency"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"
)

// Domain is the ErrorInfo domain of our errors: with the reason, like
// insufficient_funds, it names an error that clients can branch on.
const Domain = "payments"

// ToStatus maps an error to a gRPC status. It's the only place that knows how
// our errors become gRPC, like classify does for HTTP. An error that already
// is a status passes through unchanged.
func ToStatus(err error) *status.Status {
	if s, ok := status.FromError(err); ok {
		return s
	}
	var valErr *apperr.ValidationError
	var ruleErr *apperr.RuleError
	switch {
	case errors.As(err, &valErr):
		return withDetails(codes.InvalidArgument, err.Error(), fieldViolations(valErr))
	case errors.As(err, &ruleErr):
		return withDetails(codes.FailedPrecondition, err.Error(),
			&errdetails.ErrorInfo{Reason: ruleErr.Code, Domain: Domain})
	case errors.Is(err, apperr.ErrNotFound):
		return status.New(codes.NotFound, err.Error())
	case errors.Is(err, apperr.ErrConflict):
		return status.New(codes.AlreadyExists, err.Error())
	case errors.Is(err, apperr.ErrUnauthenticated):
		return status.New(codes.Unauthenticated, err.Error())
	case errors.Is(err, apperr.ErrForbidden):
		return status.New(codes.PermissionDenied, err.Error())
	case errors.Is(err, idempotency.ErrKeyReused):
		return status.New(codes.FailedPrecondition, err.Error())
	case errors.Is(err, idempotency.ErrInProgress):
		return status.New(codes.Aborted, err.Error())
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return status.FromContextError(err)
	default:
		return status.New(codes.Internal, "internal error")
	}
}

// withDetails returns a status with machine-readable details attached.
func withDetails(code codes.Code, msg string, details ...protoadapt.MessageV1) *status.Status {
	s, err := status.New(code, msg).WithDetails(details...)
	if err != nil {
		return status.New(code, msg) // only for code OK, which we never use here
	}
	return s
}

// fieldViolations lists a validation error's fields, the way gRPC clients
// expect them: a BadRequest, in the error details.
func fieldViolations(v *apperr.ValidationError) *errdetails.BadRequest {
	br := &errdetails.BadRequest{}
	for _, field := range slices.Sorted(maps.Keys(v.Fields)) {
		br.FieldViolations = append(br.FieldViolations, &errdetails.BadRequest_FieldViolation{
			Field:       field,
			Description: v.Fields[field],
		})
	}
	return br
}
