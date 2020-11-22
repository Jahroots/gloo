// Copyright 2018 Envoyproxy Authors
//
//   Licensed under the Apache License, Version 2.0 (the "License");
//   you may not use this file except in compliance with the License.
//   You may obtain a copy of the License at
//
//       http://www.apache.org/licenses/LICENSE-2.0
//
//   Unless required by applicable law or agreed to in writing, software
//   distributed under the License is distributed on an "AS IS" BASIS,
//   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//   See the License for the specific language governing permissions and
//   limitations under the License.

package xds

import (
	"errors"
	"fmt"

	envoy_config_cluster_v3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoy_config_endpoint_v3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoy_config_listener_v3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoy_config_route_v3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	"github.com/golang/protobuf/proto"
	"github.com/solo-io/gloo/projects/gloo/pkg/xds/internal"
	"github.com/solo-io/solo-kit/pkg/api/v1/control-plane/cache"
	"github.com/solo-io/solo-kit/pkg/api/v1/control-plane/resource"
)

// Snapshot is an internally consistent snapshot of xDS resources.
// Consistently is important for the convergence as different resource types
// from the snapshot may be delivered to the proxy in arbitrary order.
type EnvoySnapshot struct {
	// Endpoints are items in the EDS V3 response payload.
	Endpoints cache.Resources

	// hiddenDeprecatedEndpoints are items in the EDS V2 response payload.
	hiddenDeprecatedEndpoints cache.Resources

	// Clusters are items in the CDS response payload.
	Clusters cache.Resources

	// hiddenDeprecatedClusters are items in the EDS V2 response payload.
	hiddenDeprecatedClusters cache.Resources

	// Routes are items in the RDS response payload.
	Routes cache.Resources

	// hiddenDeprecatedRoutes are items in the EDS V2 response payload.
	hiddenDeprecatedRoutes cache.Resources

	// Listeners are items in the LDS response payload.
	Listeners cache.Resources
	// hiddenDeprecatedListeners are items in the EDS V2 response payload.
	hiddenDeprecatedListeners cache.Resources
}

var _ cache.Snapshot = &EnvoySnapshot{}

// NewSnapshot creates a snapshot from response types and a version.
func NewSnapshot(
	version string,
	endpoints []cache.Resource,
	clusters []cache.Resource,
	routes []cache.Resource,
	listeners []cache.Resource,
) *EnvoySnapshot {
	// TODO: Copy resources
	return &EnvoySnapshot{
		Endpoints:                 cache.NewResources(version, endpoints),
		hiddenDeprecatedEndpoints: cache.NewResources(version, nil),
		Clusters:                  cache.NewResources(version, clusters),
		hiddenDeprecatedClusters:  downgradeCacheResourceList(version, clusters),
		Routes:                    cache.NewResources(version, routes),
		hiddenDeprecatedRoutes:    cache.NewResources(version, nil),
		Listeners:                 cache.NewResources(version, listeners),
		hiddenDeprecatedListeners: downgradeCacheResourceList(version, clusters),
	}
}

func NewSnapshotFromResources(
	endpoints cache.Resources,
	clusters cache.Resources,
	routes cache.Resources,
	listeners cache.Resources,
) cache.Snapshot {
	// TODO: Copy resources and downgrade, maybe maintain hash to not do it too many times
	return &EnvoySnapshot{
		Endpoints:                 endpoints,
		Clusters:                  clusters,
		hiddenDeprecatedClusters:  downgradeCacheResources(clusters),
		Routes:                    routes,
		Listeners:                 listeners,
		hiddenDeprecatedListeners: downgradeCacheResources(listeners),
	}
}


func downgradeResource(e cache.Resource) *resource.EnvoyResource {
	var downgradedResource cache.ResourceProto
	res := e.ResourceProto()
	if res == nil {
		return nil
	}
	switch v := res.(type) {
	case *envoy_config_endpoint_v3.ClusterLoadAssignment:
		// No downgrade necessary
	case *envoy_config_cluster_v3.Cluster:
		downgradedResource = internal.DowngradeCluster(v)
	case *envoy_config_route_v3.RouteConfiguration:
		// No downgrade necessary
	case *envoy_config_listener_v3.Listener:
		downgradedResource = internal.DowngradeListener(v)
	}
	return &resource.EnvoyResource{ProtoMessage: downgradedResource}
}


func downgradeCacheResources(resources cache.Resources) cache.Resources {
	newResources := make([]cache.Resource, 0, len(resources.Items))
	for _, v := range resources.Items {
		downgradedResource := downgradeResource(v)
		if downgradedResource != nil {
			newResources = append(newResources, downgradedResource)
		}
	}
	return cache.NewResources(resources.Version, newResources)
}

func downgradeCacheResourceList(version string, resources []cache.Resource) cache.Resources {
	newResources := make([]cache.Resource, 0, len(resources))
	for _, v := range resources {
		downgradedResource := downgradeResource(v)
		if downgradedResource != nil {
			newResources = append(newResources, downgradedResource)
		}
	}
	return cache.NewResources(version, newResources)
}

// Consistent check verifies that the dependent resources are exactly listed in the
// snapshot:
// - all EDS resources are listed by name in CDS resources
// - all RDS resources are listed by name in LDS resources
//
// Note that clusters and listeners are requested without name references, so
// Envoy will accept the snapshot list of clusters as-is even if it does not match
// all references found in xDS.
func (s *EnvoySnapshot) Consistent() error {
	if s == nil {
		return errors.New("nil snapshot")
	}
	endpoints := resource.GetResourceReferences(s.Clusters.Items)
	if len(endpoints) != len(s.Endpoints.Items) {
		return fmt.Errorf("mismatched endpoint reference and resource lengths: length of %v does not equal length of %v", endpoints, s.Endpoints.Items)
	}
	if err := cache.Superset(endpoints, s.Endpoints.Items); err != nil {
		return err
	}

	routes := resource.GetResourceReferences(s.Listeners.Items)
	if len(routes) != len(s.Routes.Items) {
		return fmt.Errorf("mismatched route reference and resource lengths: length of %v does not equal length of %v", routes, s.Routes.Items)
	}
	return cache.Superset(routes, s.Routes.Items)
}

// GetResources selects snapshot resources by type.
func (s *EnvoySnapshot) GetResources(typ string) cache.Resources {
	if s == nil {
		return cache.Resources{}
	}
	switch typ {
	case resource.EndpointTypeV3:
		return s.Endpoints
	case resource.ClusterTypeV3:
		return s.Clusters
	case resource.RouteTypeV3:
		return s.Routes
	case resource.ListenerTypeV3:
		return s.Listeners
	case resource.EndpointTypeV2:
		return s.hiddenDeprecatedEndpoints
	case resource.ClusterTypeV2:
		return s.hiddenDeprecatedClusters
	case resource.RouteTypeV2:
		return s.hiddenDeprecatedRoutes
	case resource.ListenerTypeV2:
		return s.hiddenDeprecatedListeners
	}
	return cache.Resources{}
}

func (s *EnvoySnapshot) Clone() cache.Snapshot {
	snapshotClone := &EnvoySnapshot{}

	snapshotClone.Endpoints = cache.Resources{
		Version: s.Endpoints.Version,
		Items:   cloneItems(s.Endpoints.Items),
	}

	snapshotClone.Clusters = cache.Resources{
		Version: s.Clusters.Version,
		Items:   cloneItems(s.Clusters.Items),
	}

	snapshotClone.Routes = cache.Resources{
		Version: s.Routes.Version,
		Items:   cloneItems(s.Routes.Items),
	}

	snapshotClone.Listeners = cache.Resources{
		Version: s.Listeners.Version,
		Items:   cloneItems(s.Listeners.Items),
	}

	snapshotClone.hiddenDeprecatedEndpoints = cache.Resources{
		Version: s.hiddenDeprecatedEndpoints.Version,
		Items:   cloneItems(s.hiddenDeprecatedEndpoints.Items),
	}

	snapshotClone.hiddenDeprecatedClusters = cache.Resources{
		Version: s.hiddenDeprecatedClusters.Version,
		Items:   cloneItems(s.hiddenDeprecatedClusters.Items),
	}

	snapshotClone.hiddenDeprecatedRoutes = cache.Resources{
		Version: s.hiddenDeprecatedRoutes.Version,
		Items:   cloneItems(s.hiddenDeprecatedRoutes.Items),
	}

	snapshotClone.hiddenDeprecatedListeners = cache.Resources{
		Version: s.hiddenDeprecatedListeners.Version,
		Items:   cloneItems(s.hiddenDeprecatedListeners.Items),
	}

	return snapshotClone
}

func cloneItems(items map[string]cache.Resource) map[string]cache.Resource {
	clonedItems := make(map[string]cache.Resource, len(items))
	for k, v := range items {
		resProto := v.ResourceProto()
		// NOTE(marco): we have to use `github.com/golang/protobuf/proto.Clone()` to clone here,
		// `github.com/gogo/protobuf/proto.Clone()` will panic!
		resClone := proto.Clone(resProto)
		clonedItems[k] = resource.NewEnvoyResource(resClone)
	}
	return clonedItems
}
