/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2017 Red Hat, Inc.
 *
 */

package cli

//go:generate mockgen -source $GOFILE -imports "libvirt=github.com/libvirt/libvirt-go" -package=$GOPACKAGE -destination=generated_mock_$GOFILE

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libvirt/libvirt-go"
	utilwait "k8s.io/apimachinery/pkg/util/wait"

	"kubevirt.io/kubevirt/pkg/logging"
	"kubevirt.io/kubevirt/pkg/virt-handler/virtwrap/errors"
)

// TODO: Should we handle libvirt connection errors transparent or panic?
type Connection interface {
	LookupDomainByName(name string) (VirDomain, error)
	DomainDefineXML(xml string) (VirDomain, error)
	Close() (int, error)
	DomainEventLifecycleRegister(callback libvirt.DomainEventLifecycleCallback) error
	ListAllDomains(flags libvirt.ConnectListAllDomainsFlags) ([]VirDomain, error)
	NewStream(flags libvirt.StreamFlags) (Stream, error)
	LookupSecretByUsage(usageType libvirt.SecretUsageType, usageID string) (VirSecret, error)
	SecretDefineXML(xml string) (VirSecret, error)
	ListSecrets() ([]string, error)
	LookupSecretByUUIDString(uuid string) (VirSecret, error)
	ListAllSecrets(flags libvirt.ConnectListAllSecretsFlags) ([]VirSecret, error)
}

type Stream interface {
	io.ReadWriteCloser
	UnderlyingStream() *libvirt.Stream
}

type VirStream struct {
	*libvirt.Stream
}

type LibvirtConnection struct {
	Connect       *libvirt.Connect
	user          string
	pass          string
	uri           string
	alive         bool
	stop          chan struct{}
	reconnectLock *sync.Mutex
	callbacks     []libvirt.DomainEventLifecycleCallback
}

func (s *VirStream) Write(p []byte) (n int, err error) {
	return s.Stream.Send(p)
}

func (s *VirStream) Read(p []byte) (n int, err error) {
	return s.Stream.Recv(p)
}

/*
Close the stream and free its resources. Since closing a stream involves multiple calls with errors,
the first error occurred will be returned. The stream will always be freed.
*/
func (s *VirStream) Close() (e error) {
	e = s.Finish()
	if e != nil {
		return s.Free()
	}
	s.Free()
	return e
}

func (s *VirStream) UnderlyingStream() *libvirt.Stream {
	return s.Stream
}

func (l *LibvirtConnection) NewStream(flags libvirt.StreamFlags) (Stream, error) {
	if err := l.reconnectIfNecessary(); err != nil {
		return nil, err
	}
	defer l.checkConnectionLost()

	s, err := l.Connect.NewStream(flags)
	if err != nil {
		return nil, err
	}
	return &VirStream{Stream: s}, nil
}

func (l *LibvirtConnection) Close() (int, error) {
	close(l.stop)
	return l.Close()
}

func (l *LibvirtConnection) ListSecrets() (secrets []string, err error) {
	if err = l.reconnectIfNecessary(); err != nil {
		return
	}
	defer l.checkConnectionLost()

	secrets, err = l.Connect.ListSecrets()
	return
}

func (l *LibvirtConnection) LookupSecretByUUIDString(uuid string) (secret VirSecret, err error) {
	if err = l.reconnectIfNecessary(); err != nil {
		return
	}
	defer l.checkConnectionLost()

	secret, err = l.Connect.LookupSecretByUUIDString(uuid)
	return
}

func (l *LibvirtConnection) LookupSecretByUsage(usageType libvirt.SecretUsageType, usageID string) (secret VirSecret, err error) {
	if err = l.reconnectIfNecessary(); err != nil {
		return
	}
	defer l.checkConnectionLost()

	secret, err = l.Connect.LookupSecretByUsage(usageType, usageID)
	return
}

func (l *LibvirtConnection) ListAllSecrets(flags libvirt.ConnectListAllSecretsFlags) ([]VirSecret, error) {
	if err := l.reconnectIfNecessary(); err != nil {
		return nil, err
	}
	defer l.checkConnectionLost()

	virSecrets, err := l.Connect.ListAllSecrets(flags)
	if err != nil {
		return nil, err
	}
	secrets := make([]VirSecret, len(virSecrets))
	for i, d := range virSecrets {
		secrets[i] = &d
	}
	return secrets, nil
}

func (l *LibvirtConnection) SecretDefineXML(xml string) (secret VirSecret, err error) {
	if err = l.reconnectIfNecessary(); err != nil {
		return
	}
	defer l.checkConnectionLost()

	secret, err = l.Connect.SecretDefineXML(xml, 0)
	return
}

func (l *LibvirtConnection) DomainEventLifecycleRegister(callback libvirt.DomainEventLifecycleCallback) (err error) {
	if err = l.reconnectIfNecessary(); err != nil {
		return
	}
	defer l.checkConnectionLost()

	l.callbacks = append(l.callbacks, callback)
	_, err = l.Connect.DomainEventLifecycleRegister(nil, callback)
	return
}

func (l *LibvirtConnection) LookupDomainByName(name string) (dom VirDomain, err error) {
	if err = l.reconnectIfNecessary(); err != nil {
		return
	}
	defer l.checkConnectionLost()

	return l.Connect.LookupDomainByName(name)
}

func (l *LibvirtConnection) DomainDefineXML(xml string) (dom VirDomain, err error) {
	if err = l.reconnectIfNecessary(); err != nil {
		return
	}
	defer l.checkConnectionLost()

	dom, err = l.Connect.DomainDefineXML(xml)
	return
}

func (l *LibvirtConnection) ListAllDomains(flags libvirt.ConnectListAllDomainsFlags) ([]VirDomain, error) {
	if err := l.reconnectIfNecessary(); err != nil {
		return nil, err
	}
	defer l.checkConnectionLost()

	virDoms, err := l.Connect.ListAllDomains(flags)
	if err != nil {
		return nil, err
	}
	doms := make([]VirDomain, len(virDoms))
	for i, d := range virDoms {
		doms[i] = &d
	}
	return doms, nil
}

// Installs a watchdog which will check periodically if the libvirt connection is still alive.
func (l *LibvirtConnection) installWatchdog(checkInterval time.Duration) {
	go func() {
		for {
			select {

			case <-l.stop:
				return

			case <-time.After(checkInterval):
				l.reconnectIfNecessary()

				alive, err := l.Connect.IsAlive()

				// If the connection is ok, continue
				if alive {
					continue
				}

				if err == nil {
					// Connection is not alive but we have no error
					logging.DefaultLogger().Error().Msg("Connection to libvirt lost")
					l.reconnectLock.Lock()
					l.alive = false
					l.reconnectLock.Unlock()
				} else {
					// Do the usual error check to determine if the connection is lost
					l.checkConnectionLost()
				}
			}
		}
	}()
}

func (l *LibvirtConnection) reconnectIfNecessary() (err error) {
	l.reconnectLock.Lock()
	defer l.reconnectLock.Unlock()
	// TODO add a reconnect backoff, and immediately return an error in these cases
	// We need this to avoid swamping libvirt with reconnect tries
	if !l.alive {
		l.Connect, err = newConnection(l.uri, l.user, l.pass)
		if err != nil {
			return
		}
		l.alive = true
		cbs := l.callbacks
		l.callbacks = make([]libvirt.DomainEventLifecycleCallback, 0)
		for _, cb := range cbs {
			// Notify the callback about the reconnect by sending a nil event.
			// This way we give the callback a chance to emit an error to the watcher
			// ListWatcher will re-register automatically afterwards
			cb(l.Connect, nil, nil)
		}
	}
	return nil
}

func (l *LibvirtConnection) checkConnectionLost() {
	l.reconnectLock.Lock()
	defer l.reconnectLock.Unlock()

	err := libvirt.GetLastError()
	if errors.IsOk(err) {
		return
	}

	switch err.Code {
	case
		libvirt.ERR_INTERNAL_ERROR,
		libvirt.ERR_INVALID_CONN,
		libvirt.ERR_AUTH_CANCELLED,
		libvirt.ERR_NO_MEMORY,
		libvirt.ERR_AUTH_FAILED,
		libvirt.ERR_SYSTEM_ERROR,
		libvirt.ERR_RPC:
		l.alive = false
		logging.DefaultLogger().Error().Reason(err).With("code", err.Code).Msg("Connection to libvirt lost.")
	}
}

type VirSecret interface {
	SetValue(value []byte, flags uint32) error
	Undefine() error
	GetUsageID() (string, error)
	GetUUIDString() (string, error)
	GetXMLDesc(flags uint32) (string, error)
	Free() error
}

type VirDomain interface {
	GetState() (libvirt.DomainState, int, error)
	Create() error
	Resume() error
	Destroy() error
	GetName() (string, error)
	GetUUIDString() (string, error)
	GetXMLDesc(flags libvirt.DomainXMLFlags) (string, error)
	Undefine() error
	OpenConsole(devname string, stream *libvirt.Stream, flags libvirt.DomainConsoleFlags) error
	Free() error
}

func waitForLibvirt(uri string, user string, pass string, timeout time.Duration) error {
	interval := 10 * time.Second
	return utilwait.PollImmediate(interval, timeout, func() (done bool, err error) {
		if virConn, err := newConnection(uri, user, pass); err == nil {
			defer virConn.Close()
			return true, nil
		}
		return false, nil
	})
}

func NewConnection(uri string, user string, pass string, checkInterval time.Duration) (Connection, error) {
	timeout := 15 * time.Second
	logger := logging.DefaultLogger()
	logger.Info().V(1).Msgf("Connecting to libvirt daemon: %s", uri)
	if err := waitForLibvirt(uri, user, pass, timeout); err != nil {
		return nil, fmt.Errorf("cannot connect to libvirt daemon: %v", err)
	}
	logger.Info().V(1).Msg("Connected to libvirt daemon")
	virConn, err := newConnection(uri, user, pass)
	if err != nil {
		return nil, err
	}
	lvConn := &LibvirtConnection{
		Connect: virConn, user: user, pass: pass, uri: uri, alive: true,
		callbacks:     make([]libvirt.DomainEventLifecycleCallback, 0),
		reconnectLock: &sync.Mutex{},
	}
	lvConn.installWatchdog(checkInterval)

	return lvConn, nil
}

// TODO: needs a functional test.
func newConnection(uri string, user string, pass string) (*libvirt.Connect, error) {
	callback := func(creds []*libvirt.ConnectCredential) {
		for _, cred := range creds {
			if cred.Type == libvirt.CRED_AUTHNAME {
				cred.Result = user
				cred.ResultLen = len(cred.Result)
			} else if cred.Type == libvirt.CRED_PASSPHRASE {
				cred.Result = pass
				cred.ResultLen = len(cred.Result)
			}
		}
	}
	auth := &libvirt.ConnectAuth{
		CredType: []libvirt.ConnectCredentialType{
			libvirt.CRED_AUTHNAME, libvirt.CRED_PASSPHRASE,
		},
		Callback: callback,
	}
	virConn, err := libvirt.NewConnectWithAuth(uri, auth, 0)

	return virConn, err
}

func IsDown(domState libvirt.DomainState) bool {
	switch domState {
	case libvirt.DOMAIN_NOSTATE, libvirt.DOMAIN_SHUTDOWN, libvirt.DOMAIN_SHUTOFF, libvirt.DOMAIN_CRASHED:
		return true

	}
	return false
}

func IsPaused(domState libvirt.DomainState) bool {
	switch domState {
	case libvirt.DOMAIN_PAUSED:
		return true

	}
	return false
}
