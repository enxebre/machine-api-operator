/*
Copyright 2018 The Kubernetes Authors.

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
	"log"
	"time"

	"github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	"github.com/openshift/machine-api-operator/pkg/controller"
	"github.com/openshift/machine-api-operator/pkg/controller/machineset"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const (
	defaultWebhookPort    = 8443
	defaultWebhookCertdir = "/etc/machine-api-operator/tls"
)

func main() {
	flag.Set("logtostderr", "true")
	klog.InitFlags(nil)
	watchNamespace := flag.String("namespace", "",
		"Namespace that the controller watches to reconcile cluster-api objects. If unspecified, the controller watches for cluster-api objects across all namespaces.")

	flag.Parse()
	if *watchNamespace != "" {
		log.Printf("Watching cluster-api objects only in namespace %q for reconciliation.", *watchNamespace)
	}
	log.Printf("Registering Components.")
	// Get a config to talk to the apiserver
	cfg, err := config.GetConfig()
	if err != nil {
		log.Fatal(err)
	}

	// Create a new Cmd to provide shared dependencies and start components
	syncPeriod := 10 * time.Minute
	opts := manager.Options{
		// Disable metrics serving
		MetricsBindAddress: "0",
		SyncPeriod:         &syncPeriod,
		Namespace:          *watchNamespace,
	}

	mgr, err := manager.New(cfg, opts)
	if err != nil {
		log.Fatal(err)
	}

	// Enable defaulting and validating webhooks
	defaulter, err := v1beta1.NewDefaulter()
	if err != nil {
		log.Fatal(err)
	}

	validator, err := v1beta1.NewValidator()
	if err != nil {
		log.Fatal(err)
	}

	mgr.GetWebhookServer().Port = defaultWebhookPort
	mgr.GetWebhookServer().CertDir = defaultWebhookCertdir
	mgr.GetWebhookServer().Register("/mutate-machine-openshift-io-v1beta1-machine", &webhook.Admission{Handler: defaulter})
	mgr.GetWebhookServer().Register("/validate-machine-openshift-io-v1beta1-machine", &webhook.Admission{Handler: validator})

	log.Printf("Registering Components.")

	// Setup Scheme for all resources
	if err := v1beta1.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatal(err)
	}

	// Setup all Controllers
	if err := controller.AddToManager(mgr, opts, machineset.Add); err != nil {
		log.Fatal(err)
	}

	log.Printf("Starting the Cmd.")

	// Start the Cmd
	log.Fatal(mgr.Start(signals.SetupSignalHandler()))
}
