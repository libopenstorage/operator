package server

import (
	"context"
	"reflect"

	"github.com/libopenstorage/grpc-framework/pkg/auth"
	"github.com/libopenstorage/grpc-framework/pkg/correlation"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ExternalAuthZChecker is a caller-supplied function that is invoked by this framework to perform an authZ check.
// Returns true if the request is allowed. Otherwise, returns false.
type ExternalAuthZChecker func(ctx context.Context, authZReq ExternalAuthZRequest) (bool, error)

// ExternalAuthZRequest contains data required to perform authorization via an external authorizer e.g. OPA.
// The concrete type of this request is specific to the external authorizer. gRPC API handlers return this value
// to indicate which objects/operations authZ check is to be performed against. The value is then passed
// to the specified AuthZChecker function which understands the concrete type of this value.
type ExternalAuthZRequest interface{}

// InsecureNoAuthNAuthZ is returned by the API handlers that wish to skip authN and authZ checks.
const InsecureNoAuthNAuthZ = "insecureNoAuthNAuthZ"

// InsecureNoAuthZ is returned by the API handlers that want to skip authZ but require that
// the user be an authenticated user. No guests.
const InsecureNoAuthZ = "insecureNoAuthZ"

// Type of the key that stores handler data in gRPC context.
type externalAuthorizerContextKey string

// HandlerData is optionally returned by the API handlers that wish to stash data in the context for later retrieval.
// This is useful to avoid duplicating (in the handler) the work previously done when performing an authZ check.
type HandlerData interface{}

const (
	// Key to store api handler's data in gRPC context
	handlerData externalAuthorizerContextKey = "handlerdata"
)

// contextSaveHandlerData saves handler-specific data in the context. This allows handler to stash result of
// work done during GetAuthZRequest() and retrieve it later when the handler is invoked after authZ check passes.
// This avoids duplicate work.
func contextSaveHandlerData(ctx context.Context, data interface{}) context.Context {
	return context.WithValue(ctx, handlerData, data)
}

// ContextGetHandlerData returns handler data that was stashed in the context by
// authZ interceptor. Returns nil if no data was found.
func ContextGetHandlerData(ctx context.Context) interface{} {
	return ctx.Value(handlerData)
}

// ExternalAuthZRequestGetter must be implemented by all gRPC services that use the external authorizer.
type ExternalAuthZRequestGetter interface {
	// GetAuthZRequest is invoked by the authZ interceptor before performing an authZ check.
	// Returns an auth request that will be passed to ExternalAuthZChecker function to authorize
	// the specified input API request. Optionally, returns handler-data to be stashed in the context
	// for the later retrieval by the handler. The first return param can have following special values:
	// - Return InsecureNoAuthNAuthZ to skip both authN and authZ completely.
	// - Return InsecureNoAuthZ to perform just an authN check for a specific request and skip authZ check.
	// Such insecure requests must also be whilelisted in insecureNoAuthNAuthZReqs or insecureNoAuthZReqs params.
	// ExternalAuthZChecker function is not invoked for the insecure requests.
	GetAuthZRequest(ctx context.Context, fullPath string, request interface{}) (ExternalAuthZRequest, HandlerData, error)
}

// externalAuthorizerUnaryInterceptor returns authZ interceptor for unary gRPCs. It calls the specified authZChecker
// for the services that need authZ checks to be performed. Services must implement ExternalAuthZRequestGetter interface.
func (s *GrpcFrameworkServer) externalAuthorizerUnaryInterceptor(
	authZChecker ExternalAuthZChecker,
	insecureNoAuthNAuthZReqs []interface{},
	insecureNoAuthZReqs []interface{},
) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, apiRequest interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (interface{}, error) {
		// Audit log
		log := correlation.NewFunctionLogger(ctx)
		log.Out = s.auditLogOutput
		auditLogErrorf := func(c codes.Code, format string, a ...interface{}) error {
			log.WithContext(ctx).WithFields(logrus.Fields{
				"method": "externalAuthorizerUnaryInterceptor",
				"code":   c.String(),
			}).Warningf(format, a...)
			return status.Errorf(c, "external authorization failed")
		}
		// do we have an authenticated user?
		userAuthenticated := false
		if userInfo, found := auth.NewUserInfoFromContext(ctx); found && !userInfo.Guest {
			userAuthenticated = true
		}
		authZReqGetter, ok := info.Server.(ExternalAuthZRequestGetter)
		if !ok {
			return nil, auditLogErrorf(codes.Internal, "%T does not implement authZ request getter", info.Server)
		}
		authZReq, handlerData, err := authZReqGetter.GetAuthZRequest(ctx, info.FullMethod, apiRequest)
		if err != nil {
			st, ok := status.FromError(err)
			if ok {
				log.WithContext(ctx).WithFields(logrus.Fields{
					"method": "externalAuthorizerUnaryInterceptor",
					"code":   st.Code().String(),
				}).Warningf("failed to get authZ request, status: %v", err.Error())
				return nil, err
			}
			return nil, auditLogErrorf(codes.Internal, "failed to get authZ request: %v", err)
		}
		switch authZReq {
		case InsecureNoAuthNAuthZ:
			// skip authN and authZ as long as the request type exists in our list
			if !s.listContainsReqType(insecureNoAuthNAuthZReqs, apiRequest) {
				return nil, auditLogErrorf(codes.Internal, "req %T absent in skip authN and authZ list", apiRequest)
			}
		case InsecureNoAuthZ:
			if !s.listContainsReqType(insecureNoAuthZReqs, apiRequest) {
				return nil, auditLogErrorf(codes.Internal, "req %T absent in skip authZ list", apiRequest)
			}
			// any authenticated user is allowed
			if !userAuthenticated {
				return nil, auditLogErrorf(codes.Unauthenticated, "authentication creds not found")
			}
		default:
			if !userAuthenticated {
				return nil, auditLogErrorf(codes.Unauthenticated, "authentication creds not found")
			}
			// perform authZ check
			allow, err := authZChecker(ctx, authZReq)
			if err != nil {
				return nil, auditLogErrorf(codes.Internal, "failed to check authZ: %v", err)
			}
			if !allow {
				return nil, auditLogErrorf(codes.PermissionDenied, "access denied")
			}
		}
		newCtx := contextSaveHandlerData(ctx, handlerData)
		return handler(newCtx, apiRequest)
	}
}

func (s *GrpcFrameworkServer) externalAuthorizerStreamInterceptor(
	authZChecker ExternalAuthZChecker,
	insecureNoAuthNAuthZReqs []interface{},
	insecureNoAuthZReqs []interface{},
) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		// TODO: perform auth Z check; then call the handler commented out below
		return status.Errorf(
			codes.PermissionDenied,
			"External authorizer not implemented for stream APIs")

		// return handler(srv, ss)
	}
}

func (s *GrpcFrameworkServer) listContainsReqType(list []interface{}, req interface{}) bool {
	for _, r := range list {
		if reflect.TypeOf(r) == reflect.TypeOf(req) {
			return true
		}
	}
	return false
}
