package gateway

import (
	"fmt"
	"net/http"

	"github.com/fiston-user/payments/internal/platform/apperr"
	"github.com/fiston-user/payments/internal/platform/httpx"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Errors the gateway answers with itself.
var (
	errRateLimited = httpx.NewError(http.StatusTooManyRequests, "rate_limited",
		"too many requests, try again after Retry-After seconds")
	errUpstreamTimeout = httpx.NewError(http.StatusGatewayTimeout, "upstream_timeout",
		"the service took too long to answer")
	errUpstreamDown = httpx.NewError(http.StatusBadGateway, "upstream_unavailable",
		"the service is unavailable, try again later")
)

// fromCode maps the gRPC codes our services send. Others are a 500.
var fromCode = map[codes.Code]struct {
	status int
	code   string
}{
	codes.InvalidArgument:    {http.StatusUnprocessableEntity, "invalid_input"},
	codes.FailedPrecondition: {http.StatusUnprocessableEntity, "failed_precondition"},
	codes.NotFound:           {http.StatusNotFound, "not_found"},
	codes.AlreadyExists:      {http.StatusConflict, "conflict"},
	codes.Aborted:            {http.StatusConflict, "request_in_progress"},
	codes.Unauthenticated:    {http.StatusUnauthorized, "unauthenticated"},
}

// fromRPC turns a gRPC error into one httpx.Error sends with the right
// status: the mirror image of grpcx.ToStatus.
func fromRPC(err error) error {
	s, ok := status.FromError(err)
	if err == nil || !ok {
		return err
	}
	switch s.Code() {
	case codes.DeadlineExceeded:
		return errUpstreamTimeout
	case codes.Unavailable:
		return fmt.Errorf("%w: %s", errUpstreamDown, s.Message())
	}
	to, known := fromCode[s.Code()]
	if !known {
		return fmt.Errorf("rpc: %s: %s", s.Code(), s.Message())
	}
	for _, d := range s.Details() {
		switch d := d.(type) {
		case *errdetails.BadRequest:
			var v apperr.ValidationError
			for _, f := range d.GetFieldViolations() {
				v.Add(f.GetField(), f.GetDescription())
			}
			if err := v.Err(); err != nil {
				return err
			}
		case *errdetails.ErrorInfo:
			to.code = d.GetReason()
		}
	}
	return httpx.NewError(to.status, to.code, s.Message())
}
