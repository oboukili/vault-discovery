package gce

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"strconv"

	"github.com/pkg/errors"
	"github.com/oboukili/vault-discovery/helpers"
	"github.com/oboukili/vault-discovery/types"
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
	s, err = compute.NewService(ctx, option.WithScopes(compute.ComputeReadonlyScope))
	if err != nil {
		return nil, err
	}
	return s, err
}

func GetInstances(ctx context.Context, project string, s *compute.InstancesService) (r zonalInstances, err error) {
	r = make(zonalInstances)

	req := s.AggregatedList(project).Context(ctx)
	if err := req.Pages(ctx, func(page *compute.InstanceAggregatedList) error {
		for zone, instancesScopedList := range page.Items {
			if len(instancesScopedList.Instances) > 0 {
				r[strings.TrimPrefix(zone, "zones/")] = filterInstances(instancesScopedList.Instances)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	var instancesCount int
	for _, v := range r {
		if len(v) == 0 {
			return nil, fmt.Errorf("(GCE) Could not get some instances' details, it usually happens if your filters (tags,name,labels) do not match: %+v", r)
		}
		instancesCount += len(v)
	}

	log.WithField("count", instancesCount).Info("(GCE) Found Vault instances ;)")
	return r, err
}

func GetVaultPrimaryTunnelConn(ctx context.Context, project string, attrs types.VaultTunnelConnAttrs) (v types.VaultTunnelConn, err error) {
	c, err := initServiceClient(ctx)
	if err != nil {
		log.WithError(err).Fatalf("(GCE) Could not initialize Service client!")
	}
	instanceService := compute.NewInstancesService(c)
	r, err := GetInstances(ctx, project, instanceService)
	if err != nil {
		log.WithError(err).Fatalf("(GCE) Could not get instances!")
	}
	if len(r) == 0 {
		log.Fatalf("(GCE) No instances were found!")
	}
	ch := make(chan types.VaultTunnelConn, 1)
	for _, instances := range r {
		for _, i := range instances {
			go func() {
				if err := handleInitTunnelConn(ch, i, attrs, project); err != nil {
					log.WithError(err).Fatal()
				}
			}()
		}
	}
	v = <-ch
	return v, err
}

func handleInitTunnelConn(c chan types.VaultTunnelConn, instance *compute.Instance, attrs types.VaultTunnelConnAttrs, project string) error {
	gCloudPort, err := helpers.GetAvailableLocalTCPPort()
	if err != nil {
		return errors.Wrapf(err, "(GCE) Could not get a free TCP port!")
	}
	if _, err := exec.LookPath("gcloud"); err != nil {
		return errors.Wrapf(err, "(GCE) Could not find gcloud in PATH!")
	}

	log.WithField("port", gCloudPort).Info("(GCE) Starting IAP tunnel...")
	tunnel := exec.Command("gcloud", "compute",
		"start-iap-tunnel", instance.Name, attrs.LocalPort,
		"--project", project,
		"--local-host-port", "127.0.0.1:"+gCloudPort,
		"--zone", instance.Zone)

	var debug bool
	d, ok := os.LookupEnv("GCE_DEBUG")
	if ok {
		debug, err = strconv.ParseBool(d)
		if err != nil {
			return errors.Wrapf(err, "GCE_DEBUG Environment variable should be boolean compatible")
		}
	}
	if debug {
		tunnel.Stderr = os.Stderr
		tunnel.Stdout = os.Stdout
	}
	err = tunnel.Start()
	if err != nil {
		return errors.Wrapf(err, "(GCE) Could not start the tunnel!")
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
			return errors.Wrapf(err, "(GCE) Could not connect to the TCP tunnel within 10 seconds!")
		}
	}
	log.WithField("port", gCloudPort).Info("(GCE) IAP tunnel open ;)")
	var url strings.Builder
	url.WriteString(attrs.VaultScheme)
	url.WriteString("://")
	url.WriteString(attrs.LocalAddr)
	url.WriteString(":")
	url.WriteString(gCloudPort)
	ok, err = helpers.IsVaultPrimaryInstance(url.String())
	if err != nil {
		return errors.Wrapf(err, "(GCE) Could not call Vault instance! url: %s", url.String())
	}
	switch ok {
	case false:
		helpers.HandlerCloseConn(conn, nil)
		helpers.HandlerExitCommand(tunnel)
	case true:
		log.WithField("PrimaryInstanceName", instance.Name).Infof("(GCE) Primary Vault instance detected ;)")
		c <- types.VaultTunnelConn{Conn: conn, Cmd: tunnel, Attrs: attrs}
	}
	return err
}
