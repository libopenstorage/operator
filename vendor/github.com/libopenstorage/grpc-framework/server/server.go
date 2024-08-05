/*
Copyright 2018 Portworx

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
	"fmt"
	"net"
	"os"

	grpcserver "github.com/libopenstorage/grpc-framework/pkg/grpc/server"
	"github.com/libopenstorage/grpc-framework/pkg/util"
	"github.com/sirupsen/logrus"
)

const (
	// Default audig log location
	defaultAuditLog = "grpc-framework-audit.log"
	// Default access log location
	defaultAccessLog = "grpc-framework-access.log"
)

// Server is an implementation of the gRPC SDK interface
type Server struct {
	config      ServerConfig
	netServer   *GrpcFrameworkServer
	udsServer   *GrpcFrameworkServer
	restGateway *RestGateway
	grpcPort    string

	accessLog *os.File
	auditLog  *os.File
}

type logger struct {
	log *logrus.Entry
}

// Interface check
var _ grpcserver.Server = &GrpcFrameworkServer{}

// New creates a new SDK server
func New(config *ServerConfig) (*Server, error) {

	if config == nil {
		return nil, fmt.Errorf("must provide configuration")
	}

	// If no security set, initialize the object as empty
	if config.Security == nil {
		config.Security = &SecurityConfig{}
	}

	// Check if the socket is provided to enable the REST gateway to communicate
	// to the unix domain socket
	if len(config.RestConfig.Port) != 0 && len(config.Socket) == 0 {
		return nil, fmt.Errorf("must provide unix domain socket for REST server to communicate with gRPC server")
	}

	// Set default log locations
	var (
		accessLog, auditLog *os.File
		err                 error
	)
	if config.AuditOutput == nil {
		auditLog, err = util.OpenLog(defaultAuditLog)
		if err != nil {
			return nil, err
		}
		config.AuditOutput = auditLog
	}
	if config.AccessOutput == nil {
		accessLog, err := util.OpenLog(defaultAccessLog)
		if err != nil {
			return nil, err
		}
		config.AccessOutput = accessLog
	}

	_, port, err := net.SplitHostPort(config.Address)
	if err != nil {
		logrus.Warnf("grpc-framework Address NOT in host:port format, failed to get port %v", err.Error())
	}

	// Create a gRPC server on the network
	netServer, err := NewGrpcFrameworkServer(config)
	if err != nil {
		return nil, err
	}

	// Create a gRPC server on a unix domain socket
	var udsServer *GrpcFrameworkServer
	if config.Net != "unix" && config.Socket != "" {
		udsConfig := *config
		udsConfig.Net = "unix"
		udsConfig.Address = config.Socket
		udsServer, err = NewGrpcFrameworkServer(&udsConfig)
		if err != nil {
			return nil, err
		}
	}

	// Create REST Gateway and connect it to the unix domain socket server
	var restGateway *RestGateway
	if config.RestConfig.Enabled {
		restGateway, err = NewRestGateway(config, udsServer)
		if err != nil {
			return nil, err
		}
	}

	return &Server{
		config:      *config,
		netServer:   netServer,
		udsServer:   udsServer,
		restGateway: restGateway,
		auditLog:    auditLog,
		accessLog:   accessLog,
		grpcPort:    port,
	}, nil
}

// Start all servers
func (s *Server) Start() error {
	if s.netServer != nil {
		if err := s.netServer.Start(); err != nil {
			return err
		}
	}
	if s.udsServer != nil {
		if err := s.udsServer.Start(); err != nil {
			s.Stop()
			return err
		}

	}
	if s.restGateway != nil {
		if err := s.restGateway.Start(); err != nil {
			s.Stop()
			return err
		}
	}

	return nil
}

func (s *Server) Stop() {
	if s.netServer != nil {
		s.netServer.Stop()
	}
	if s.udsServer != nil {
		s.udsServer.Stop()
	}
	if s.restGateway != nil {
		s.restGateway.Stop()
	}
	if s.accessLog != nil {
		s.accessLog.Close()
	}
	if s.auditLog != nil {
		s.auditLog.Close()
	}
}

func (s *Server) Address() string {
	return s.netServer.Address()
}

func (s *Server) UdsAddress() string {
	return s.udsServer.Address()
}

func (s *Server) GrpcPort() string {
	return s.grpcPort
}

func (s *Server) RestPort() string {
	return s.config.RestConfig.Port
}

func (s *Server) Transaction(f func() error) error {
	if f == nil {
		return fmt.Errorf("no stransaction provided")
	}
	if s.netServer != nil {
		s.netServer.transactionStart()
		defer s.netServer.transactionEnd()
	}
	if s.udsServer != nil {
		s.udsServer.transactionStart()
		defer s.udsServer.transactionEnd()
	}

	return f()
}
