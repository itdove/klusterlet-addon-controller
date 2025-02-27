// (c) Copyright IBM Corporation 2019, 2020. All Rights Reserved.
// Note to U.S. Government Users Restricted Rights:
// U.S. Government Users Restricted Rights - Use, duplication or disclosure restricted by GSA ADP Schedule
// Contract with IBM Corp.
//
// Copyright (c) Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package v1

import (
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentv1 "github.com/open-cluster-management/klusterlet-addon-controller/pkg/apis/agent/v1"
)

// constants for search collector
const (
	SearchCollector         = "klusterlet-addon-search"
	Search                  = "search"
	RequiresHubKubeConfig   = true
	managedClusterAddOnName = "search-collector"
	addonNameEnv            = "SEARCH_NAME"
)

var log = logf.Log.WithName("search")

type AddonSearch struct{}

func (addon AddonSearch) IsEnabled(instance *agentv1.KlusterletAddonConfig) bool {
	return instance.Spec.SearchCollectorConfig.Enabled
}

func (addon AddonSearch) CheckHubKubeconfigRequired() bool {
	return RequiresHubKubeConfig
}

func (addon AddonSearch) GetAddonName() string {
	return Search
}

func (addon AddonSearch) NewAddonCR(instance *agentv1.KlusterletAddonConfig, namespace string) (runtime.Object, error) {
	return newSearchCollectorCR(instance, namespace)
}

func (addon AddonSearch) GetManagedClusterAddOnName() string {
	if n := os.Getenv(addonNameEnv); len(n) != 0 {
		return n
	}
	log.Info("failed to get addon name from env var " + addonNameEnv)
	return managedClusterAddOnName
}

// newSearchCollectorCR - create CR for component search collector
func newSearchCollectorCR(instance *agentv1.KlusterletAddonConfig, namespace string) (*agentv1.SearchCollector, error) {
	labels := map[string]string{
		"app": instance.Name,
	}

	gv := agentv1.GlobalValues{
		ImagePullPolicy: instance.Spec.ImagePullPolicy,
		ImagePullSecret: instance.Spec.ImagePullSecret,
		ImageOverrides:  make(map[string]string, 1),
	}

	imageRepository, err := instance.GetImage("search_collector")
	if err != nil {
		log.Error(err, "Fail to get Image", "Component.Name", "search-collector")
		return nil, err
	}
	gv.ImageOverrides["search_collector"] = imageRepository

	if imageRepositoryLease, err := instance.GetImage("klusterlet_addon_lease_controller"); err != nil {
		log.Error(err, "Fail to get Image", "Image.Key", "klusterlet_addon_lease_controller")
	} else {
		gv.ImageOverrides["klusterlet_addon_lease_controller"] = imageRepositoryLease
	}

	return &agentv1.SearchCollector{
		TypeMeta: metav1.TypeMeta{
			APIVersion: agentv1.SchemeGroupVersion.String(),
			Kind:       "SearchCollector",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      SearchCollector,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: agentv1.SearchCollectorSpec{
			FullNameOverride:    SearchCollector,
			ClusterName:         instance.Spec.ClusterName,
			ClusterNamespace:    instance.Spec.ClusterNamespace,
			HubKubeconfigSecret: managedClusterAddOnName + "-hub-kubeconfig",
			GlobalValues:        gv,
		},
	}, err
}
