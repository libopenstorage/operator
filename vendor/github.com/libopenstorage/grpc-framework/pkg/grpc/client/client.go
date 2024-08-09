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
	"context"
	"fmt"
	"net"
	"net/url"
	"time"

	"google.golang.org/grpc"
)

var (
	DefaultConnectionTimeout = 1 * time.Minute
)

// Connect to address by grpc
func Connect(address string, dialOptions []grpc.DialOption) (*grpc.ClientConn, error) {
	return ConnectWithTimeout(address, dialOptions, DefaultConnectionTimeout)
}

// ConnectWithTimeout to address by grpc with timeout
func ConnectWithTimeout(address string, dialOptions []grpc.DialOption, timeout time.Duration) (*grpc.ClientConn, error) {
	u, err := url.Parse(address)
	if err == nil {
		// Check if host just has an IP
		if u.Scheme == "unix" ||
			(!u.IsAbs() && net.ParseIP(address) == nil) {
			dialOptions = append(dialOptions,
				grpc.WithDialer(
					func(addr string, timeout time.Duration) (net.Conn, error) {
						return net.DialTimeout("unix", u.Path, timeout)
					}))
		}
	}

	dialOptions = append(dialOptions,
		grpc.WithBackoffMaxDelay(time.Second),
		grpc.WithBlock(),
	)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	conn, err := grpc.DialContext(ctx, address, dialOptions...)
	if err != nil {
		return nil, fmt.Errorf("Failed to connect gRPC server %s: %v", address, err)
	}

	return conn, nil
}
