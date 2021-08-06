// (c) Copyright IBM Corporation 2019, 2020. All Rights Reserved.
// Note to U.S. Government Users Restricted Rights:
// U.S. Government Users Restricted Rights - Use, duplication or disclosure restricted by GSA ADP Schedule
// Contract with IBM Corp.
//
// Copyright (c) Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

// Package klusterletaddon contains the main reconcile function & related functions for klusterletAddonConfigs
package klusterletaddon

import (
	"context"
	"os"
	"strings"
	"time"

	addonv1alpha1 "github.com/open-cluster-management/api/addon/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	managedclusterv1 "github.com/open-cluster-management/api/cluster/v1"
	manifestworkv1 "github.com/open-cluster-management/api/work/v1"
	agentv1 "github.com/open-cluster-management/klusterlet-addon-controller/pkg/apis/agent/v1"
	"github.com/open-cluster-management/klusterlet-addon-controller/pkg/utils"
)

var log = logf.Log.WithName("controller_klusterletaddon")

const (
	KlusterletAddonConfigAnnotationPause = "klusterletaddonconfig-pause"
	PolicyControllerLabel                = "policycontroller.addon.open-cluster-management.io"
)

// Add creates a new KlusterletAddon Controller and adds it to the Manager.
// The Manager will set fields on the Controller and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	client := newCustomClient(mgr.GetClient(), mgr.GetAPIReader())
	return &ReconcileKlusterletAddon{client: client, scheme: mgr.GetScheme()}
}

// customClient will do get secret without cache, other operations are like normal cache client
type customClient struct {
	client.Client
	APIReader client.Reader
}

// newCustomClient creates custom client to do get secret without cache
func newCustomClient(client client.Client, apiReader client.Reader) client.Client {
	return customClient{
		Client:    client,
		APIReader: apiReader,
	}
}

func (cc customClient) Get(ctx context.Context, key client.ObjectKey, obj runtime.Object) error {
	if _, ok := obj.(*corev1.Secret); ok {
		return cc.APIReader.Get(ctx, key, obj)
	}
	return cc.Client.Get(ctx, key, obj)
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("klusterletaddon-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource Klusterlet
	err = c.Watch(&source.Kind{Type: &agentv1.KlusterletAddonConfig{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}
	// Watch for changes to secondary resource Pods and requeue the owner ClusterDeployment
	err = c.Watch(
		&source.Kind{Type: &managedclusterv1.ManagedCluster{}},
		&handler.EnqueueRequestsFromMapFunc{ToRequests: handler.ToRequestsFunc(
			func(obj handler.MapObject) []reconcile.Request {
				return []reconcile.Request{
					{
						NamespacedName: types.NamespacedName{
							Name:      obj.Meta.GetName(), // only handle klusterlet with name/namespaxe same as managedCluster's name
							Namespace: obj.Meta.GetName(),
						},
					},
				}
			},
		)},
	)
	if err != nil {
		return err
	}

	// watch for deletion of managedclusteraddons owned by a klusterletaddonconfig
	err = c.Watch(
		&source.Kind{Type: &addonv1alpha1.ManagedClusterAddOn{}},
		&handler.EnqueueRequestForOwner{
			OwnerType:    &agentv1.KlusterletAddonConfig{},
			IsController: true,
		},
		newManagedClusterAddonDeletionPredicate(),
	)
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileKlusterletAddon implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileKlusterletAddon{}

// ReconcileKlusterletAddon reconciles a Klusterlet object
type ReconcileKlusterletAddon struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a KlusterletAddonConfig object
// and makes changes based on the state read and what is in the KlusterletAddonConfig.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileKlusterletAddon) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling KlusterletAddonConfig")

	// Fetch the ManagedCluster instance
	managedCluster := &managedclusterv1.ManagedCluster{}
	managedClusterIsNotFound := false
	if err := r.client.Get(context.TODO(), types.NamespacedName{
		Name: request.Namespace,
	}, managedCluster); err != nil && errors.IsNotFound(err) {
		managedClusterIsNotFound = true
	} else if err != nil {
		return reconcile.Result{}, err
	}

	// Fetch the Klusterletaddonconfig instance
	klusterletAddonConfig := &agentv1.KlusterletAddonConfig{}
	if err := r.client.Get(context.TODO(), request.NamespacedName, klusterletAddonConfig); err != nil {
		if errors.IsNotFound(err) {
			// remove finalizer on ManagedCluster if klusterlet not found
			if !managedClusterIsNotFound && utils.HasFinalizer(managedCluster, KlusterletAddonFinalizer) {
				utils.RemoveFinalizer(managedCluster, KlusterletAddonFinalizer)
				if err := r.client.Update(context.TODO(), managedCluster); err != nil {
					return reconcile.Result{}, err
				}
			}
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	if klusterletAddonConfig.DeletionTimestamp != nil {
		// if ManagedCluster not online, force delete all manifestwork
		removeFinalizers := managedClusterIsNotFound || !IsManagedClusterOnline(managedCluster)

		// delete & wait all CRs
		if isCompleted, err := deleteManifestWorkCRs(klusterletAddonConfig, r.client, removeFinalizers); err != nil {
			reqLogger.Error(err, "Fail to delete all ManifestWorks for Addon CRs")
			return reconcile.Result{}, err
		} else if !isCompleted {
			return reconcile.Result{Requeue: true, RequeueAfter: 5 * time.Second}, err
		}

		// delete & wait component Operator
		if isCompleted, err := deleteManifestWorkHelper(
			klusterletAddonConfig.Name+KlusterletAddonOperatorPostfix,
			klusterletAddonConfig.Namespace,
			r.client,
			removeFinalizers,
		); err != nil {
			reqLogger.Error(err, "Fail to delete ManifestWork of Klusterlet Addon Operator")
			return reconcile.Result{}, err
		} else if !isCompleted {
			return reconcile.Result{Requeue: true, RequeueAfter: 5 * time.Second}, err
		}

		// delete & wait CRDs
		if isCompleted, err := deleteManifestWorkHelper(
			klusterletAddonConfig.Name+KlusterletAddonCRDsPostfix,
			klusterletAddonConfig.Namespace,
			r.client,
			removeFinalizers,
		); err != nil {
			reqLogger.Error(err, "Fail to delete ManifestWork of CRDs")
			return reconcile.Result{}, err
		} else if !isCompleted {
			return reconcile.Result{Requeue: true, RequeueAfter: 5 * time.Second}, err
		}

		//WORK-AROUND until issue #74 is implemented
		//update managedcluster label for PolicyController
		if err := setManagedClusterPolicyControllerLabel(managedCluster, klusterletAddonConfig, r.client); err != nil {
			reqLogger.Error(err, "Fail to update label for PolicyController in managedcluster")
			return reconcile.Result{}, err
		}

		utils.RemoveFinalizer(klusterletAddonConfig, KlusterletAddonFinalizer)
		if err := r.client.Update(context.TODO(), klusterletAddonConfig); err != nil {
			return reconcile.Result{}, err
		}
		// remove finalizer on managedCluster when all things are removed
		if !managedClusterIsNotFound {
			utils.RemoveFinalizer(managedCluster, KlusterletAddonFinalizer)
			if err := r.client.Update(context.TODO(), managedCluster); err != nil {
				return reconcile.Result{}, err
			}
		}
		return reconcile.Result{}, nil
	}
	// don't do anything when there is no managedcluster
	if managedClusterIsNotFound {
		return reconcile.Result{}, nil
	}

	if !utils.HasFinalizer(managedCluster, KlusterletAddonFinalizer) {
		utils.AddFinalizer(managedCluster, KlusterletAddonFinalizer)
		if err := r.client.Update(context.TODO(), managedCluster); err != nil && errors.IsConflict(err) {
			return reconcile.Result{Requeue: true, RequeueAfter: 5 * time.Second}, nil
		} else if err != nil {
			reqLogger.Error(err, "Fail to UPDATE managedCluster")
			return reconcile.Result{}, err
		}
	}
	if !utils.HasFinalizer(klusterletAddonConfig, KlusterletAddonFinalizer) {
		utils.AddFinalizer(klusterletAddonConfig, KlusterletAddonFinalizer)
		if err := r.client.Update(context.TODO(), klusterletAddonConfig); err != nil && errors.IsConflict(err) {
			return reconcile.Result{Requeue: true, RequeueAfter: 5 * time.Second}, nil
		} else if err != nil {
			reqLogger.Error(err, "Fail to UPDATE KlusterletAddonConfig")
			return reconcile.Result{}, err
		}
	}

	// Delete klusterletAddonConfig if ManagedCluster is in deletion
	if managedCluster.DeletionTimestamp != nil && klusterletAddonConfig.DeletionTimestamp == nil {
		if err := r.client.Delete(context.TODO(), klusterletAddonConfig); err != nil {
			reqLogger.Error(err, "Fail to trigger deletion of KlusterletAddonConfig")
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, nil
	}

	if isPaused(klusterletAddonConfig) {
		reqLogger.Info("KlusterletAddonConfig reconciliation is paused. Nothing more to do.")
		return reconcile.Result{}, nil
	}

	// Fill with default imagePullSecret if empty
	if klusterletAddonConfig.Spec.ImagePullSecret == "" {
		klusterletAddonConfig.Spec.ImagePullSecret = os.Getenv("DEFAULT_IMAGE_PULL_SECRET")
	}

	// Fill with default imageRegistry if empty
	if klusterletAddonConfig.Spec.ImageRegistry == "" {
		klusterletAddonConfig.Spec.ImageRegistry = os.Getenv("DEFAULT_IMAGE_REGISTRY")
	}

	// Create manifest work for crds
	if err := createManifestWorkCRD(klusterletAddonConfig, managedCluster.Status.Version.Kubernetes, r); err != nil {
		reqLogger.Error(err, "Fail to create manifest work for CRD")
		return reconcile.Result{}, err
	}

	// Create manifest work for Klusterlet Addon operator
	if err := createManifestWorkComponentOperator(klusterletAddonConfig, r); err != nil {
		reqLogger.Error(err, "Fail to create manifest work for klusterlet addon opearator")
		return reconcile.Result{}, err
	}

	// Sync ManagedClusterAddon for component crs according to klusterletAddonConfig enable/disable settings
	if err := syncManagedClusterAddonCRs(klusterletAddonConfig, r); err != nil {
		reqLogger.Error(err, "Fail to create ManagedClusterAddon for CRs")
		return reconcile.Result{}, err
	}

	//WORK-AROUND until issue #74 is implemented
	//update managedcluster label for PolicyController
	if err := setManagedClusterPolicyControllerLabel(managedCluster, klusterletAddonConfig, r.client); err != nil {
		reqLogger.Error(err, "Fail to update label for PolicyController in managedcluster")
		return reconcile.Result{}, err
	}

	manifestWork, err := utils.GetManifestWork(request.Namespace+KlusterletAddonCRDsPostfix, request.Namespace, r.client)
	if err != nil {
		return reconcile.Result{}, err
	}

	if manifestWork != nil && len(manifestWork.Status.Conditions) > 0 {
		if IsCRDManfestWorkAvailable(manifestWork) {
			// sync manifestWork for component crs according to klusterletAddonConfig enable/disable settings
			if err := syncManifestWorkCRs(klusterletAddonConfig, r); err != nil {
				reqLogger.Error(err, "Fail to create manifest work for CRs")
				return reconcile.Result{}, err
			}
		} else {
			return reconcile.Result{Requeue: true, RequeueAfter: 30 * time.Second}, nil
		}
	} else if IsManagedClusterOnline(managedCluster) {
		return reconcile.Result{Requeue: true, RequeueAfter: 30 * time.Second}, nil
	}

	return reconcile.Result{Requeue: true, RequeueAfter: 5 * time.Minute}, nil
}

// IsManagedClusterOnline - if cluster is online returns true otherwise returns false
func IsManagedClusterOnline(managedCluster *managedclusterv1.ManagedCluster) bool {
	if managedCluster == nil {
		return false
	}
	for _, condition := range managedCluster.Status.Conditions {
		if condition.Type == managedclusterv1.ManagedClusterConditionAvailable { //not sure which condition is valid
			if condition.Status == "True" {
				return true
			}
		}
	}

	return false
}

// deleteManifestWorkHelper returns true if object is not found
func deleteManifestWorkHelper(name, namespace string, client client.Client, removeFinalizers bool) (bool, error) {
	err := utils.DeleteManifestWork(name, namespace, client, removeFinalizers)
	if err != nil && errors.IsNotFound(err) {
		return true, nil
	} else if err != nil {
		return false, err
	}

	return false, nil
}

// isPaused returns true if the KlusterletAddonConfig instance is labeled as paused, and false otherwise
func isPaused(instance *agentv1.KlusterletAddonConfig) bool {
	a := instance.GetAnnotations()
	if a == nil {
		return false
	}

	if a[KlusterletAddonConfigAnnotationPause] != "" &&
		strings.EqualFold(a[KlusterletAddonConfigAnnotationPause], "true") {
		return true
	}

	return false
}

func newManagedClusterAddonDeletionPredicate() predicate.Predicate {
	return predicate.Predicate(predicate.Funcs{
		GenericFunc: func(e event.GenericEvent) bool { return false },
		CreateFunc:  func(e event.CreateEvent) bool { return false },
		DeleteFunc: func(e event.DeleteEvent) bool {
			if e.Object == nil {
				log.Error(nil, "Delete event has no runtime object to delete", "event", e)
				return false
			}
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool { return false },
	})
}

// IsCRDManfestWorkAvailable - if manifestwork for crd is applied and resource is available on managed cluster it will return true
func IsCRDManfestWorkAvailable(manifestWork *manifestworkv1.ManifestWork) bool {
	for _, condition := range manifestWork.Status.Conditions {
		if condition.Type == manifestworkv1.WorkAvailable { //not sure which condition is valid
			if condition.Status == "True" {
				return true
			}
		}
	}

	return false
}

//WORK-AROUND until issue #74 is implemented
//Add/Remove a label depending on policy controller installation

func setManagedClusterPolicyControllerLabel(managedCluster *managedclusterv1.ManagedCluster, klusterletAddonConfig *agentv1.KlusterletAddonConfig, client client.Client) error {
	if klusterletAddonConfig.DeletionTimestamp != nil ||
		!klusterletAddonConfig.Spec.PolicyController.Enabled {
		if _, ok := managedCluster.GetLabels()[PolicyControllerLabel]; ok {
			delete(managedCluster.GetLabels(), PolicyControllerLabel)
			return client.Update(context.TODO(), managedCluster)
		}
	} else {
		if managedCluster.GetLabels() == nil {
			managedCluster.SetLabels(map[string]string{})
		}
		if strings.ToLower(managedCluster.GetLabels()[PolicyControllerLabel]) != "true" {
			managedCluster.GetLabels()[PolicyControllerLabel] = "true"
			return client.Update(context.TODO(), managedCluster)
		}
	}
	return nil
}

// func setManagedClusterPolicyControllerLabel(
// 	managedCluster *managedclusterv1.ManagedCluster,
// 	klusterletAddonConfig *agentv1.KlusterletAddonConfig,
// 	client client.Client) error {
// 	managedClusterAddon := &addonv1alpha1.ManagedClusterAddOn{}
// 	// check if it exists
// 	if err := client.Get(
// 		context.TODO(),
// 		types.NamespacedName{
// 			Name:      addons.PolicyCtrl.GetManagedClusterAddOnName(),
// 			Namespace: klusterletAddonConfig.Namespace,
// 		},
// 		managedClusterAddon,
// 	); err != nil {
// 		if errors.IsNotFound(err) {
// 			//ManagedClusterAddOn doesn't exist, check if managedcluster needs to be updated
// 			if _, ok := managedCluster.GetLabels()[PolicyControllerLabel]; ok {
// 				delete(managedCluster.GetLabels(), PolicyControllerLabel)
// 				return client.Update(context.TODO(), managedCluster)
// 			}
// 			return nil
// 		}
// 		return err
// 	}
// 	//ManagedClusterAddOn exists
// 	if managedCluster.GetLabels() == nil {
// 		managedCluster.SetLabels(map[string]string{})
// 	}
// 	//check if managedcluster needs to be updated
// 	if strings.ToLower(managedCluster.GetLabels()[PolicyControllerLabel]) != "true" {
// 		managedCluster.GetLabels()[PolicyControllerLabel] = "true"
// 		return client.Update(context.TODO(), managedCluster)
// 	}
// 	return nil
// }
