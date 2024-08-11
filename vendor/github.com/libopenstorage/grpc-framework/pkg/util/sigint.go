/*
Copyright © 2022 Pure Storage

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

package util

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/sirupsen/logrus"
)

type SigIntManager struct {
	handler func()
	done    chan bool
	wg      sync.WaitGroup
	lock    sync.Mutex
	running bool
}

func NewSigIntManager(handler func()) *SigIntManager {
	return &SigIntManager{
		handler: handler,
		done:    make(chan bool, 1),
	}
}

func (s *SigIntManager) Start() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.running {
		return fmt.Errorf("already running a signal handler")
	}

	var startwg sync.WaitGroup
	signalch := make(chan os.Signal, 1)
	signal.Notify(signalch, os.Interrupt, os.Kill, syscall.SIGINT, syscall.SIGTERM)
	startwg.Add(1)
	s.wg.Add(1)
	go func() {
		startwg.Done()
		select {
		case <-signalch:
			logrus.Info("Ctrl-C captured...")
			s.handler()
		case <-s.done:
			logrus.Debug("Closing Ctrl-C capturing function")
		}
		s.wg.Done()
	}()
	startwg.Wait()

	s.running = true
	return nil
}

func (s *SigIntManager) Stop() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if !s.running {
		return nil
	}

	s.done <- true
	s.wg.Wait()
	s.running = false

	return nil
}
