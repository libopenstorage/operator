/*
Copyright 2022 Pure Storage

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package server

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/libopenstorage/grpc-framework/pkg/auth/role"
	"github.com/libopenstorage/grpc-framework/pkg/correlation"
	grpcserver "github.com/libopenstorage/grpc-framework/pkg/grpc/server"

	"github.com/sirupsen/logrus"

	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpc_auth "github.com/grpc-ecosystem/go-grpc-middleware/auth"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/tap"
)

type GrpcFrameworkServer struct {
	*grpcserver.GrpcServer

	lock   sync.RWMutex
	name   string
	config ServerConfig

	// Loggers
	log             *logrus.Entry
	auditLogOutput  io.Writer
	accessLogOutput io.Writer

	roleServer role.RoleManager
}

// New creates a new gRPC server for the gRPC framework
func NewGrpcFrameworkServer(config *ServerConfig) (*GrpcFrameworkServer, error) {
	if nil == config {
		return nil, fmt.Errorf("configuration must be provided")
	}

	// Default to tcp
	if len(config.Net) == 0 {
		config.Net = "tcp"
	}

	// Create a log object for this server
	name := "grpc-framework-" + config.Net
	log := logrus.WithFields(logrus.Fields{
		"name": name,
	})

	// Setup authentication

	// Check the necessary security config options are set. Authentication is enabled. Therefore,
	// either RoleManager must be provided (this implies use of the default authZ)
	// or explicit authZ interceptors must be provided
	// or external authZ checker must be provided (this implies use of external_authorizer.go)
	if config.Security.Authenticators != nil && config.Security.Role == nil &&
		(config.AuthZUnaryInterceptor == nil || config.AuthZStreamInterceptor == nil) &&
		config.ExternalAuthZChecker == nil {
		return nil, fmt.Errorf("must supply role manager when authentication is enabled and default authZ is used")
	}
	for issuer := range config.Security.Authenticators {
		log.Infof("Authentication enabled for issuer: %s", issuer)
	}

	// Create gRPC server
	gServer, err := grpcserver.New(&grpcserver.GrpcServerConfig{
		Name:    name,
		Net:     config.Net,
		Address: config.Address,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to setup %s server: %v", name, err)
	}

	s := &GrpcFrameworkServer{
		GrpcServer:      gServer,
		accessLogOutput: config.AccessOutput,
		auditLogOutput:  config.AuditOutput,
		roleServer:      config.Security.Role,
		config:          *config,
		name:            name,
		log:             log,
	}
	return s, nil
}

// Start is used to start the server.
// It will return an error if the server is already running.
func (s *GrpcFrameworkServer) Start() error {

	// Setup https if certs have been provided
	opts := s.config.ServerOptions
	if s.config.Net != "unix" && s.config.Security.Tls != nil {
		creds, err := credentials.NewServerTLSFromFile(
			s.config.Security.Tls.CertFile,
			s.config.Security.Tls.KeyFile)
		if err != nil {
			return fmt.Errorf("failed to create credentials from cert files: %v", err)
		}
		opts = append(opts, grpc.Creds(creds))
		s.log.Info("TLS enabled")
	} else {
		s.log.Info("TLS disabled")
	}

	// Add correlation interceptor
	correlationInterceptor := correlation.ContextInterceptor{
		Origin: correlation.ComponentGrpcFw,
	}

	// Set up unary interceptors
	unaryInterceptors := []grpc.UnaryServerInterceptor{
		s.rwlockUnaryIntercepter,
		correlationInterceptor.ContextUnaryServerInterceptor,
	}

	// use caller's authN interceptor if provided
	if s.config.AuthNUnaryInterceptor != nil {
		unaryInterceptors = append(unaryInterceptors, s.config.AuthNUnaryInterceptor)
	} else if s.config.Security.Authenticators != nil {
		// use the default authN interceptor
		unaryInterceptors = append(unaryInterceptors, grpc_auth.UnaryServerInterceptor(s.auth))
	}

	// use caller's authZ interceptor if provided
	if s.config.AuthZUnaryInterceptor != nil {
		// use caller's authZ interceptor as-is
		unaryInterceptors = append(unaryInterceptors, s.config.AuthZUnaryInterceptor)
	} else if s.config.ExternalAuthZChecker != nil {
		// plug the caller-supplied authChecker into our external authorizer framework
		unaryInterceptors = append(unaryInterceptors, s.externalAuthorizerUnaryInterceptor(
			s.config.ExternalAuthZChecker, s.config.InsecureNoAuthNAuthZReqs, s.config.InsecureNoAuthZReqs))
	} else if s.config.Security.Authenticators != nil {
		// use our default authZ interceptor
		unaryInterceptors = append(unaryInterceptors, s.authorizationServerUnaryInterceptor)
	}

	// append remaining default unary interceptors
	unaryInterceptors = append(unaryInterceptors, []grpc.UnaryServerInterceptor{
		s.loggerServerUnaryInterceptor,
		grpc_prometheus.UnaryServerInterceptor,
	}...)

	// Set up stream interceptors
	streamInterceptors := []grpc.StreamServerInterceptor{
		s.rwlockStreamIntercepter,
	}

	// use caller's authN interceptor if provided
	if s.config.AuthNStreamInterceptor != nil {
		streamInterceptors = append(streamInterceptors, s.config.AuthNStreamInterceptor)
	} else if s.config.Security.Authenticators != nil {
		// use the default authN interceptor
		streamInterceptors = append(streamInterceptors, grpc_auth.StreamServerInterceptor(s.auth))
	}

	// use caller's authZ interceptor if provided
	if s.config.AuthZStreamInterceptor != nil {
		// use caller's authZ interceptor as-is
		streamInterceptors = append(streamInterceptors, s.config.AuthZStreamInterceptor)
	} else if s.config.ExternalAuthZChecker != nil {
		// plug the caller-supplied authChecker into our external authorizer interceptor
		streamInterceptors = append(streamInterceptors, s.externalAuthorizerStreamInterceptor(
			s.config.ExternalAuthZChecker, s.config.InsecureNoAuthNAuthZReqs, s.config.InsecureNoAuthZReqs))
	} else if s.config.Security.Authenticators != nil {
		// use our default authZ interceptor
		streamInterceptors = append(streamInterceptors, s.authorizationServerStreamInterceptor)
	}

	// append remaining default stream interceptors
	streamInterceptors = append(streamInterceptors, []grpc.StreamServerInterceptor{
		s.loggerServerStreamInterceptor,
		grpc_prometheus.StreamServerInterceptor,
	}...)

	// Append other custom interceptors to the end of the chain
	unaryInterceptors = append(unaryInterceptors, s.config.UnaryServerInterceptors...)
	streamInterceptors = append(streamInterceptors, s.config.StreamServerInterceptors...)

	opts = append(opts, grpc.UnaryInterceptor(
		grpc_middleware.ChainUnaryServer(unaryInterceptors...),
	))
	opts = append(opts, grpc.StreamInterceptor(
		grpc_middleware.ChainStreamServer(streamInterceptors...),
	))

	// Determine if we should add rate limiters
	if s.config.RateLimiters.RateLimiter != nil || s.config.RateLimiters.RateLimiterPerUser != nil {
		opts = append(opts, grpc.InTapHandle(s.rateLimiter))
	}

	// Start the gRPC Server
	err := s.GrpcServer.StartWithServer(func() *grpc.Server {
		grpcServer := grpc.NewServer(opts...)

		// Register gRPC Handlers
		for _, ext := range s.config.GrpcServerExtensions {
			ext(grpcServer)
		}

		// Register stats for all the services
		s.registerPrometheusMetrics(grpcServer)

		return grpcServer
	})
	if err != nil {
		return err
	}

	return nil
}

func (s *GrpcFrameworkServer) registerPrometheusMetrics(grpcServer *grpc.Server) {
	// Register the gRPCs and enable latency historgram
	grpc_prometheus.Register(grpcServer)
	grpc_prometheus.EnableHandlingTimeHistogram()

	// Initialize the metrics
	grpcMetrics := grpc_prometheus.NewServerMetrics()
	grpcMetrics.InitializeMetrics(grpcServer)
}

func (s *GrpcFrameworkServer) transactionStart() {
	s.lock.Lock()
}

func (s *GrpcFrameworkServer) transactionEnd() {
	s.lock.Unlock()
}

func (s *GrpcFrameworkServer) globalLimiterAllow() bool {
	// Check if there is no limiter. If none, allow all
	if s.config.RateLimiters.RateLimiter == nil {
		return true
	}
	return s.config.RateLimiters.RateLimiter.Allow()
}

func (s *GrpcFrameworkServer) rateLimiter(
	ctx context.Context,
	info *tap.Info,
) (context.Context, error) {

	// Check global limiter
	if !s.globalLimiterAllow() {
		return nil, status.Error(codes.ResourceExhausted, "resources for clients exhausted")
	}

	// Check per user limiter
	// XXX TODO

	return ctx, nil
}
