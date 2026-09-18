package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fiston-user/payments/internal/platform/httpx"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFromRPC(t *testing.T) {
	invalid, _ := status.New(codes.InvalidArgument, "invalid input: amount").WithDetails(
		&errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{
			{Field: "amount", Description: "must be positive"},
		}})
	broke, _ := status.New(codes.FailedPrecondition, "insufficient funds").WithDetails(
		&errdetails.ErrorInfo{Reason: "insufficient_funds", Domain: "payments"})

	tests := []struct {
		err    error
		status int
		body   string // a part of the JSON the client gets
	}{
		{invalid.Err(), 422, `"fields":{"amount":"must be positive"}`},
		{broke.Err(), 422, `"code":"insufficient_funds"`},
		{status.Error(codes.NotFound, "wallet not found"), 404, `"code":"not_found"`},
		{status.Error(codes.DeadlineExceeded, "too slow"), 504, `"code":"upstream_timeout"`},
		{status.Error(codes.Unavailable, "connection refused"), 502, `"code":"upstream_unavailable"`},
		{status.Error(codes.Internal, "boom"), 500, `"code":"internal"`},
	}
	for _, tt := range tests {
		rr := httptest.NewRecorder()
		httpx.Error(rr, httptest.NewRequest("GET", "/", nil), fromRPC(tt.err))
		if rr.Code != tt.status || !strings.Contains(rr.Body.String(), tt.body) {
			t.Errorf("%v: got %d %s, want %d with %s", tt.err, rr.Code, rr.Body, tt.status, tt.body)
		}
	}
}
