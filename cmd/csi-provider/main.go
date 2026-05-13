/*
Copyright 2021.

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

package main

import (
	"flag"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/DopplerHQ/kubernetes-operator/pkg/csi"
	"github.com/DopplerHQ/kubernetes-operator/pkg/version"

	"google.golang.org/grpc"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/secrets-store-csi-driver/provider/v1alpha1"
)

const defaultSocketPath = "/var/run/secrets-store-csi-providers/doppler.sock"

func main() {
	socketPath := flag.String("socket-path", defaultSocketPath, "Path to the provider Unix domain socket")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("csi-provider")

	log.Info("Starting Doppler CSI provider", "version", version.ControllerVersion)

	if err := os.Remove(*socketPath); err == nil {
		log.Info("Removed stale socket", "path", *socketPath)
	} else if !os.IsNotExist(err) {
		log.Error(err, "Failed to remove existing socket", "path", *socketPath)
		os.Exit(1)
	}

	listener, err := net.Listen("unix", *socketPath)
	if err != nil {
		log.Error(err, "Failed to listen on socket", "path", *socketPath)
		os.Exit(1)
	}
	defer listener.Close()

	grpcServer := grpc.NewServer()
	v1alpha1.RegisterCSIDriverProviderServer(grpcServer, &csi.Provider{})

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigChan
		log.Info("Shutting down gRPC server")
		grpcServer.GracefulStop()
	}()

	log.Info("Listening", "path", *socketPath)
	if err := grpcServer.Serve(listener); err != nil {
		log.Error(err, "gRPC server error")
		os.Exit(1)
	}
}
