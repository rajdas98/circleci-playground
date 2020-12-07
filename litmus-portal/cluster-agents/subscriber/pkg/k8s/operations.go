package k8s

import (
	"context"
	"fmt"
	"log"
	"os"

	yaml_converter "github.com/ghodss/yaml"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"

	memory "k8s.io/client-go/discovery/cached"
)

const (
	PortalConfigName = "litmus-portal-config"
)

var (
	Ctx             = context.Background()
	decUnstructured = yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	dr              dynamic.ResourceInterface
	AgentNamespace  = os.Getenv("AGENT_NAMESPACE")
)

// IsClusterConfirmed checks if the config map with "is_cluster_confirmed" is true or not.
func IsClusterConfirmed() (bool, string, error) {
	clientset, err := GetGenericK8sClient()
	if err != nil {
		return false, "", err
	}

	getCM, err := clientset.CoreV1().ConfigMaps(AgentNamespace).Get(PortalConfigName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return false, "", nil
	} else if getCM.Data["is_cluster_confirmed"] == "true" {
		return true, getCM.Data["cluster_key"], nil
	} else if err != nil {
		return false, "", err
	}

	return false, "", nil
}

// ClusterRegister function creates litmus-portal config map in the litmus namespace
func ClusterRegister(clusterData map[string]string) (bool, error) {
	clientset, err := GetGenericK8sClient()
	if err != nil {
		return false, err
	}

	configMapData := map[string]string{
		"is_cluster_confirmed": "true",
		"cluster_key":          clusterData["KEY"],
		"cluster_id":           clusterData["CID"],
	}

	newConfigMap := corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: PortalConfigName,
		},
		Data: configMapData,
	}

	_, err = clientset.CoreV1().ConfigMaps(AgentNamespace).Create(&newConfigMap)
	if err != nil {
		return false, err
	}

	log.Println("Configmap created")
	return true, nil
}

func applyRequest(requestType string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if requestType == "create" {
		response, err := dr.Create(obj, metav1.CreateOptions{})
		if errors.IsAlreadyExists(err) {
			// This doesnt ever happen even if it does already exist
			log.Printf("Already exists")
			return nil, nil
		}

		if err != nil {
			return nil, err
		}

		log.Println("Resource successfully created")
		return response, nil
	} else if requestType == "update" {
		getObj, err := dr.Get(obj.GetName(), metav1.GetOptions{})
		if errors.IsNotFound(err) {
			// This doesnt ever happen even if it is already deleted or not found
			log.Printf("%v not found", obj.GetName())
			return nil, nil
		}

		if err != nil {
			return nil, err
		}

		obj.SetResourceVersion(getObj.GetResourceVersion())

		response, err := dr.Update(obj, metav1.UpdateOptions{})
		if err != nil {
			return nil, err
		}

		log.Println("Resource successfully updated")
		return response, nil
	} else if requestType == "delete" {
		err := dr.Delete(obj.GetName(), &metav1.DeleteOptions{})
		if errors.IsNotFound(err) {
			// This doesnt ever happen even if it is already deleted or not found
			log.Printf("%v not found", obj.GetName())
			return nil, nil
		}

		if err != nil {
			return nil, err
		}

		log.Println("Resource successfully deleted")
		return &unstructured.Unstructured{}, nil
	} else if requestType == "get" {
		response, err := dr.Get(obj.GetName(), metav1.GetOptions{})
		if errors.IsNotFound(err) {
			// This doesnt ever happen even if it is already deleted or not found
			log.Printf("%v not found", obj.GetName())
			return nil, nil
		}

		if err != nil {
			return nil, err
		}

		log.Println("Resource successfully retrieved")
		return response, nil
	}

	return nil, fmt.Errorf("err: %v\n", "Invalid Request")
}

// This function handles cluster operations
func ClusterOperations(manifest string, requestType string, namespace string) (*unstructured.Unstructured, error) {

	// Converting JSON to YAML and store it in yamlStr variable
	yamlStr, err := yaml_converter.JSONToYAML([]byte(manifest))
	if err != nil {
		return nil, err
	}

	// Getting dynamic and discovery client
	discoveryClient, dynamicClient, err := GetDynamicAndDiscoveryClient()
	if err != nil {
		return nil, err
	}

	// Create a mapper using dynamic client
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))

	// Decode YAML manifest into unstructured.Unstructured
	obj := &unstructured.Unstructured{}
	_, gvk, err := decUnstructured.Decode([]byte(yamlStr), nil, obj)
	if err != nil {
		return nil, err
	}

	// Find GVR
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}

	// Obtain REST interface for the GVR
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		// namespaced resources should specify the namespace
		dr = dynamicClient.Resource(mapping.Resource).Namespace(namespace)
	} else {
		// for cluster-wide resources
		dr = dynamicClient.Resource(mapping.Resource)
	}

	if obj.GetKind() == "CronWorkflow" || obj.GetKind() == "Workflow" {
		labels := obj.GetLabels()
		pods, err := dr.List(metav1.ListOptions{
			LabelSelector: labels["workflow_id"],
		})
		if err != nil {
			return nil, nil
		}
		obj.SetName(pods.Items[0].GetName())
	}

	return applyRequest(requestType, obj)
}
