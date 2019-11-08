package main

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
	"io/ioutil"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const constVaultPort = 8200

type GCEZoneInstances map[string][]*compute.Instance

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

func GetRandomOpenTCPPort() (int, error) {
	var min int64 = 1024
	var max int64 = 65534
	r, err := rand.Int(rand.Reader, big.NewInt(max-min))
	if err != nil {
		return 0, err
	}
	n := r.Int64()
	port := n + min
	// Check whether the TCP port is available, increment it otherwise until we find a free port
	for {
		conn, err := net.Dial("tcp", "localhost:"+strconv.Itoa(int(port)))
		if err != nil {
			break
		}
		if err = conn.Close(); err != nil {
			log.WithError(err).Warnf("Could not close the test socket connection...")
		}
		port = port + 1
	}
	return int(port), nil
}

func IsVaultPrimaryInstance(url string) (b bool, err error) {
	var InsecureSkipVerify = false
	tsv, ok := os.LookupEnv("TLS_SKIP_VERIFY")
	if ok {
		InsecureSkipVerify, err = strconv.ParseBool(tsv)
		if err != nil {
			return false, err
		}
	}
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: InsecureSkipVerify}
	var client = http.Client{}
	resp, err := client.Get(url)
	if err != nil {
		return false, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.WithError(err).Warnf("Could not close http connection to Vault...")
		}
	}()

	if resp.StatusCode == http.StatusOK {
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Fatal(err)
		}
		bodyString := string(bodyBytes)
		log.Info(bodyString)
	} else {
	
	}

	var target map[string]
	err = json.NewDecoder(resp.Body).Decode(target)
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
	var url strings.Builder

	for zone, instances := range r {
		for _, instance := range instances {

			port, err := GetRandomOpenTCPPort()
			if err != nil {
				log.WithError(err).Fatalf("Could not generate a random port number!")
			}
			if _, err := exec.LookPath("gcloud"); err != nil {
				log.WithError(err).Fatalf("Could not find gcloud in PATH!")
			}

			tunnel := exec.Command("gcloud", "compute",
				"start-iap-tunnel", instance.Name, strconv.Itoa(constVaultPort),
				"--project", project,
				"--local-host-port", "localhost:"+strconv.Itoa(port),
				"--zone", zone)
			//tunnel.Stdout = os.Stdout
			tunnel.Stderr = os.Stderr
			if err := tunnel.Start(); err != nil {
				log.WithError(err).WithField("args", tunnel.Args).Fatalf("Could not start IAP tunnel!")
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
			}

			url.Reset()
			url.WriteString("https://localhost:")
			url.WriteString(strconv.Itoa(port))
			url.WriteString("/v1/sys/health")
			_, err = IsVaultPrimaryInstance(url.String())
			if err != nil {
				log.WithError(err).Fatalf("Could not call Vault instance!")
			}
		}
	}
}