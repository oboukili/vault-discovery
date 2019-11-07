package main

import (
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
	"os"
	"strings"
)

type GCEZoneInstances map[string][]*compute.Instance

type GCEInstanceServiceClient struct {
	Context         context.Context
	InstanceService *compute.InstancesService
}

func GCEInstanceFilter(instances []*compute.Instance) (result []*compute.Instance) {
	filter, ok := os.LookupEnv("TAG_INSTANCE_FILTER")
	switch ok {
	case true:
		for _, i := range instances {
			for _, t := range i.Tags.Items {
				// Strict equality on tags
				if strings.EqualFold(t, filter) {
					result = append(result, i)
				}
			}
		}
	case false:
		result = instances
	}
	filter, ok = os.LookupEnv("LABEL_INSTANCE_FILTER")
	if ok {
		for _, i := range result {
			for _, l := range i.Labels {
				// Strict equality on labels
				if strings.EqualFold(l, filter) {
					result = append(result, i)
				}
			}
		}
	}
	filter, ok = os.LookupEnv("NAME_INSTANCE_FILTER")
	if ok {
		for _, i := range result {
			if strings.Contains(i.Name, filter) {
				result = append(result, i)
			}
		}
	}
	return result
}

func GCEInitInstanceServiceClient() (c GCEInstanceServiceClient, err error) {
	ctx := context.Background()
	service, err := compute.NewService(ctx, option.WithScopes("https://www.googleapis.com/auth/compute.readonly"))
	if err != nil {
		return c, err
	}
	s := compute.NewInstancesService(service)
	return GCEInstanceServiceClient{
		Context:         ctx,
		InstanceService: s,
	}, err

}

func GCEGetInstances(project string, client GCEInstanceServiceClient) (r GCEZoneInstances, err error) {
	s := client.InstanceService
	ctx := client.Context

	instances, err := s.AggregatedList(project).Context(ctx).Do()
	if err != nil {
		return nil, err
	}

	r = make(GCEZoneInstances)
	for zone, zoneInstances := range instances.Items {
		if len(zoneInstances.Instances) > 0 {
			r[strings.TrimPrefix(zone, "zones/")] = GCEInstanceFilter(zoneInstances.Instances)
		}
	}
	return r, err
}

func main() {
	project, ok := os.LookupEnv("GOOGLE_PROJECT")
	if !ok {
		log.Fatalf("GOOGLE_PROJECT environment variable must be set!")
	}

	// TODO: implement new discovery providers like Kubernetes
	// GCE discovery
	c, err := GCEInitInstanceServiceClient()
	if err != nil {
		log.WithError(err).Fatalf("Could not initialize client!")
	}
	r, err := GCEGetInstances(project, c)
	if err != nil {
		log.WithError(err).Fatalf("Could not get instances!")
	}
	if len(r) == 0 {
		log.Fatalf("No instances were found!")
	}
	log.Print(r)
}
