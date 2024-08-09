/*
Copyright 2017 The Kubernetes Authors.

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
package client

import (
	"crypto/x509"
	"fmt"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// GetTlsDialOptions returns the appropriate gRPC dial options to connect to a gRPC server over TLS.
// If caCertData is nil then it will use the CA from the host.
func GetTlsDialOptions(caCertData []byte) ([]grpc.DialOption, error) {
	// Read the provided CA cert from the user
	capool, err := x509.SystemCertPool()
	if err != nil || capool == nil {
		logrus.Warnf("cannot load system root certificates: %v", err)
		capool = x509.NewCertPool()
	}

	// If user provided CA cert, then append it to systemCertPool.
	if len(caCertData) != 0 {
		if !capool.AppendCertsFromPEM(caCertData) {
			return nil, fmt.Errorf("cannot parse CA certificate")
		}
	}

	dialOptions := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(capool, "")),
	}
	return dialOptions, nil
}

func DialOptionsAdd(dialOptions []grpc.DialOption, o ...grpc.DialOption) []grpc.DialOption {
	return append(dialOptions, o...)
}
