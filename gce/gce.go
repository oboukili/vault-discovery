package gce

import (
	"fmt"
	"github.com/sirupsen/logrus"
	"gitlab.com/maltcommunity/public/vault-discovery/helpers"
	"gitlab.com/maltcommunity/public/vault-discovery/types"
	"golang.org/x/net/context"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

func filterInstances(instances []*compute.Instance) (result []*compute.Instance) {
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

func initServiceClient(ctx context.Context) (s *compute.Service, err error) {
	s, err = compute.NewService(ctx, option.WithScopes("https://www.googleapis.com/auth/compute.readonly"))
	if err != nil {
		return nil, err
	}
	return s, err
}

func GetInstances(project string, s *compute.InstancesService, ctx context.Context) (r types.GCEZoneInstances, err error) {
	instances, err := s.AggregatedList(project).Context(ctx).Do()
	if err != nil {
		return nil, err
	}

	r = make(types.GCEZoneInstances)
	for zone, zoneInstances := range instances.Items {
		if len(zoneInstances.Instances) > 0 {
			r[strings.TrimPrefix(zone, "zones/")] = filterInstances(zoneInstances.Instances)
		}
	}
	return r, err
}

func GetVaultTunnelConn(ctx context.Context, project string, attrs types.VaultTunnelConnAttrs) (v types.VaultTunnelConn, err error) {
	var url strings.Builder

	c, err := initServiceClient(ctx)
	if err != nil {
		logrus.WithError(err).Fatalf("Could not initialize Service client!")
	}
	instanceService := compute.NewInstancesService(c)
	r, err := GetInstances(project, instanceService, ctx)
	if err != nil {
		logrus.WithError(err).Fatalf("Could not get instances!")
	}
	if len(r) == 0 {
		logrus.Fatalf("No instances were found!")
	}

	// TODO: use go channels to initialize tunnels, (function should return a channel?)
	for zone, instances := range r {
		for _, instance := range instances {
			gCloudPort, err := helpers.GetAvailableLocalTCPPort()
			if err != nil {
				logrus.WithError(err).Fatalf("Could not get a free TCP port!")
			}
			if _, err := exec.LookPath("gcloud"); err != nil {
				logrus.WithError(err).Fatalf("Could not find gcloud in PATH!")
			}
			tunnel := exec.Command("gcloud", "compute",
				"start-iap-tunnel", instance.Name, attrs.LocalPort,
				"--project", project,
				"--local-host-port", "127.0.0.1:"+gCloudPort,
				"--zone", zone)

			err = tunnel.Start()
			if err != nil {
				return v, err
			}
			// Wait for IAP tunnel socket to be open before continuing
			var conn net.Conn
			startingTimestampInSeconds := time.Now().Second()
			for {
				conn, err = net.DialTimeout("tcp4", "127.0.0.1:"+gCloudPort, 60*time.Second)
				if conn != nil && err == nil {
					break
				}
				time.Sleep(1 * time.Second)
				if time.Now().Second() > startingTimestampInSeconds+10 {
					logrus.WithError(err).Fatalf("Could not connect to the TCP tunnel within 10 seconds!")
				}
			}

			url.Reset()
			url.WriteString(attrs.VaultScheme)
			url.WriteString("://")
			url.WriteString(attrs.LocalAddr)
			url.WriteString(":")
			url.WriteString(gCloudPort)
			url.WriteString("/v1/sys/health")

			ok, err := helpers.IsVaultPrimaryInstance(url.String())
			if err != nil {
				logrus.WithError(err).WithField("url", url.String()).Fatalf("Could not call Vault instance!")
			}
			switch ok {
			case false:
				if err := conn.Close(); err != nil {
					logrus.WithError(err).Warnf("Could not close the test socket connection...")
				}
				helpers.HandlerExitCommand(tunnel)
			case true:
				logrus.WithField("PrimaryInstanceName", instance.Name).Infof("Primary Vault instance detected ;)")
				return types.VaultTunnelConn{Conn: conn, Cmd: tunnel, Attrs: attrs}, err
			}
		}
	}
	return v, fmt.Errorf("empty tunnel")
}
