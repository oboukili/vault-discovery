package main

import (
	"crypto/rand"
	"encoding/json"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const constVaultPort  = 8200

type GCEZoneInstances map[string][]*compute.Instance

type GCEServiceClient struct {
	Context context.Context
	Service *compute.Service
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

func GCEInitServiceClient(ctx context.Context) (s *compute.Service, err error) {
	s, err = compute.NewService(ctx, option.WithScopes("https://www.googleapis.com/auth/compute.readonly"))
	if err != nil {
		return nil, err
	}
	return s, err
}

func GCEGetInstances(project string, s *compute.InstancesService, ctx context.Context) (r GCEZoneInstances, err error) {
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

func GetRandomInt() (int, error) {
	var min int64 = 32000
	var max int64 = 65000
	r, err := rand.Int(rand.Reader, big.NewInt(max-min))
	if err != nil {
		return 0, err
	}
	n := r.Int64()
	sum := n + min
	return int(sum), nil
}

func IsVaultPrimaryInstance(url string) (b bool, err error) {
	var client = &http.Client{Timeout: 10 * time.Second}
	r, err := client.Get(url)
	if err != nil {
		return false, err
	}
	defer func() {
		if err := r.Body.Close(); err != nil {
			log.WithError(err)
		}
	}()
	var target map[string]string
	err = json.NewDecoder(r.Body).Decode(target)
	if err != nil {
		return false, err
	}
	log.Print(target)
	return false, err
}

func main() {
	ctx := context.Background()
	project, ok := os.LookupEnv("GOOGLE_PROJECT")
	if !ok {
		log.Fatalf("GOOGLE_PROJECT environment variable must be set!")
	}

	// TODO: implement new discovery providers like Kubernetes
	// GCE discovery

	c, err := GCEInitServiceClient(ctx)
	if err != nil {
		log.WithError(err).Fatalf("Could not initialize Service client!")
	}
	instanceService := compute.NewInstancesService(c)
	r, err := GCEGetInstances(project, instanceService, ctx)
	if err != nil {
		log.WithError(err).Fatalf("Could not get instances!")
	}
	if len(r) == 0 {
		log.Fatalf("No instances were found!")
	}
	instancesNames := make([]string, 0)
	for _, v := range r {
		for _, k := range v {
			instancesNames = append(instancesNames, k.Name)
		}
	}
	
	var url strings.Builder

	for _, in := range instancesNames {
		port, err := GetRandomInt()
		if err != nil {
			log.WithError(err).Fatalf("Could not generate a random port number!")
		}
		tunnel := exec.Command(
			"/usr/bin/gcloud",
			"compute",
			"start-iap-tunnel",
			"--project", project,
			"--local-host-port=localhost:"+strconv.Itoa(port),
			in, strconv.Itoa(constVaultPort),
		)
		if err := tunnel.Run(); err != nil {
			log.WithFields(log.Fields{
				"tunnelProcessState": tunnel.ProcessState.String(),
				"stderr":tunnel.Stderr}).WithError(err).Fatalf("Could not start IAP tunnel!")
		}
		// Wait for socket to be open before continuing
		for {
			conn, err := net.Dial("tcp", ":"+strconv.Itoa(port))
			if err == nil {
				if err = conn.Close(); err != nil {
					log.WithError(err).Warnf("Could not close the test socket connection...")
				}
				break
			}
			if tunnel.ProcessState.Exited() {
				log.WithFields(log.Fields{
					"tunnelProcessState":tunnel.ProcessState.String(),
					"stderr":tunnel.Stderr}).Fatalf("IAP tunnel error!")
			}
		}
		
		url.Reset()
		url.WriteString("https://localhost:")
		url.WriteString(strconv.Itoa(port))
		ok, err := IsVaultPrimaryInstance(url.String())
		if err != nil {
			log.WithError(err).Fatalf("Could not call Vault instance!")
		}
		if ok {
			log.Printf("%s is primary", in)
		}
	}
}
