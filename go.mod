module github.com/openshift/machine-api-operator

go 1.12

require (
	github.com/blang/semver v3.5.1+incompatible
	github.com/ghodss/yaml v1.0.0
	github.com/golang/glog v0.0.0-20160126235308-23def4e6c14b
	github.com/onsi/ginkgo v1.8.0
	github.com/onsi/gomega v1.5.0
	github.com/openshift/api v3.9.1-0.20190517100836-d5b34b957e91+incompatible
	github.com/openshift/client-go v0.0.0-20190617165122-8892c0adc000
	github.com/openshift/cluster-api v0.0.0-20190805113604-f8de78af80fc
	github.com/openshift/cluster-api-actuator-pkg v0.0.0-20190904193718-8250b456dec7
	github.com/openshift/cluster-autoscaler-operator v0.0.1-0.20190521201101-62768a6ba480
	github.com/openshift/cluster-version-operator v3.11.1-0.20190629164025-08cac1c02538+incompatible
	github.com/operator-framework/operator-sdk v0.5.1-0.20190301204940-c2efe6f74e7b
	github.com/prometheus/client_golang v1.0.0
	github.com/spf13/cobra v0.0.5
	github.com/stretchr/testify v1.3.0
	gonum.org/v1/gonum v0.0.0-20190915125329-975d99cd20a9 // indirect
	k8s.io/api v0.0.0-20190918155943-95b840bb6a1f
	k8s.io/apimachinery v0.0.0-20190913080033-27d36303b655
	k8s.io/client-go v11.0.1-0.20190409021438-1a26190bd76a+incompatible
	k8s.io/code-generator v0.0.0-20190912054826-cd179ad6a269
	k8s.io/klog v0.4.0
	k8s.io/utils v0.0.0-20190801114015-581e00157fb1
	sigs.k8s.io/controller-runtime v0.2.0-beta.2
	sigs.k8s.io/controller-tools v0.2.2-0.20190919191502-76a25b63325a
)

replace gopkg.in/fsnotify.v1 v1.4.7 => github.com/fsnotify/fsnotify v1.4.7

replace github.com/prometheus/client_golang => github.com/prometheus/client_golang v0.9.2

replace sigs.k8s.io/controller-tools => sigs.k8s.io/controller-tools v0.2.2-0.20190919191502-76a25b63325a

replace github.com/openshift/client-go => github.com/openshift/client-go v0.0.0-20190923180330-3b6373338c9b

// pinning to kubernetes-1.16.0

replace k8s.io/api => k8s.io/api v0.0.0-20190918155943-95b840bb6a1f

replace k8s.io/code-generator => k8s.io/code-generator v0.0.0-20190912054826-cd179ad6a269

// Pinning to origin-4.3-kubernetes-1.16.0

replace k8s.io/apiextensions-apiserver => github.com/openshift/kubernetes-apiextensions-apiserver v0.0.0-20190918161926-8f644eb6e783

replace k8s.io/apimachinery => github.com/openshift/kubernetes-apimachinery v0.0.0-20190913080033-27d36303b655

replace k8s.io/client-go => github.com/openshift/kubernetes-client-go v0.0.0-20190918160344-1fbdaa4c8d90

replace k8s.io/kube-aggregator => github.com/openshift/kubernetes-kube-aggregator v0.0.0-20190918161219-8c8f079fddc3
