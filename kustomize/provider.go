package kustomize

import (
	"fmt"
	"io/ioutil"
	"sync"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/mitchellh/go-homedir"
)

// Config ...
type Config struct {
	Client dynamic.Interface
	Mapper *restmapper.DeferredDiscoveryRESTMapper
	Mutex  *sync.Mutex
}

// Provider ...
func Provider() *schema.Provider {
	p := &schema.Provider{
		ResourcesMap: map[string]*schema.Resource{
			"kustomization_resource": kustomizationResource(),
		},

		DataSourcesMap: map[string]*schema.Resource{
			// legacy name of the data source
			"kustomization": dataSourceKustomization(),
			// new name for the data source
			"kustomization_build": dataSourceKustomization(),

			// define overlay from TF
			"kustomization_overlay": dataSourceKustomizationOverlay(),
		},

		Schema: map[string]*schema.Schema{
			"kubeconfig_path": {
				Type:         schema.TypeString,
				Optional:     true,
				DefaultFunc:  schema.EnvDefaultFunc("KUBECONFIG_PATH", nil),
				ExactlyOneOf: []string{"kubeconfig_path", "kubeconfig_raw"},
				Description:  fmt.Sprintf("Path to a kubeconfig file. Can be set using KUBECONFIG_PATH env var. Either kubeconfig_path or kubeconfig_raw is required."),
			},
			"kubeconfig_raw": {
				Type:         schema.TypeString,
				Optional:     true,
				ExactlyOneOf: []string{"kubeconfig_path", "kubeconfig_raw"},
				Description:  "Raw kube config. If kubeconfig_raw is set, kubeconfig_path is ignored.",
			},
			"context": {
				Type:        schema.TypeString,
				Optional:    true,
				Default:     "",
				Description: "Context to use in kubeconfig with multiple contexts, if not specified the default context is to be used.",
			},
		},
	}

	p.ConfigureFunc = func(d *schema.ResourceData) (interface{}, error) {
		var config *rest.Config
		var err error

		raw := d.Get("kubeconfig_raw").(string)
		path := d.Get("kubeconfig_path").(string)
		context := d.Get("context").(string)

		if raw != "" {
			config, err = getClientConfig([]byte(raw), context)
			if err != nil {
				return nil, fmt.Errorf("provider kustomization: kubeconfig_raw: %s", err)
			}
		}

		if raw == "" && path != "" {
			data, err := readKubeconfigFile(path)
			if err != nil {
				return nil, fmt.Errorf("provider kustomization: kubeconfig_path: %s", err)
			}

			config, err = getClientConfig(data, context)
			if err != nil {
				return nil, fmt.Errorf("provider kustomization: kubeconfig_path: %s", err)
			}
		}

		// empty default config required to support
		// using a cluster resource or data source
		// that may not exist yet, to configure the provider
		if config == nil {
			config = &rest.Config{}
		}

		// Increase QPS and Burst rate limits
		config.QPS = 120
		config.Burst = 240

		client, err := dynamic.NewForConfig(config)
		if err != nil {
			return nil, fmt.Errorf("provider kustomization: %s", err)
		}

		dc, err := discovery.NewDiscoveryClientForConfig(config)
		if err != nil {
			return nil, fmt.Errorf("provider kustomization: %s", err)
		}

		mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))

		// Mutex to prevent parallel Kustomizer runs
		// temp workaround for upstream bug
		// https://github.com/kubernetes-sigs/kustomize/issues/3659
		mu := &sync.Mutex{}

		return &Config{client, mapper, mu}, nil
	}

	return p
}

func readKubeconfigFile(s string) ([]byte, error) {
	p, err := homedir.Expand(s)
	if err != nil {
		return nil, err
	}

	data, err := ioutil.ReadFile(p)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func getClientConfig(data []byte, context string) (*rest.Config, error) {
	if len(context) == 0 {
		return clientcmd.RESTConfigFromKubeConfig(data)
	}

	rawConfig, err := clientcmd.Load(data)
	if err != nil {
		return nil, err
	}

	var clientConfig clientcmd.ClientConfig = clientcmd.NewNonInteractiveClientConfig(
		*rawConfig,
		context,
		&clientcmd.ConfigOverrides{CurrentContext: context},
		nil)

	return clientConfig.ClientConfig()
}
