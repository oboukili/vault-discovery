package main

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/google/tcpproxy"
	vault "github.com/hashicorp/vault/api"
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
	"os/signal"
	"strconv"
	"strings"
	"time"
)

const VaultHTTPScheme = "https"
const VaultRemotePort = 8200
const VaultTunnelLocalAddr = "127.0.0.1"

type GCEZoneInstances map[string][]*compute.Instance
type VaultTunnelConn struct {
	conn net.Conn
	cmd  *exec.Cmd
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

func GCEGetVaultTunnelConn(ctx context.Context, project string) (v VaultTunnelConn, err error) {
	var url strings.Builder

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

	// TODO: use go channels to initialize tunnels, (function should return a channel?)
	for zone, instances := range r {
		for _, instance := range instances {
			gCloudPort, err := GetAvailableLocalTCPPort()
			if err != nil {
				log.WithError(err).Fatalf("Could not get a free TCP port!")
			}
			if _, err := exec.LookPath("gcloud"); err != nil {
				log.WithError(err).Fatalf("Could not find gcloud in PATH!")
			}
			tunnel := exec.Command("gcloud", "compute",
				"start-iap-tunnel", instance.Name, strconv.Itoa(VaultRemotePort),
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
					log.WithError(err).Fatalf("Could not connect to the TCP tunnel within 10 seconds!")
				}
			}

			url.Reset()
			url.WriteString(VaultHTTPScheme)
			url.WriteString("://")
			url.WriteString(VaultTunnelLocalAddr)
			url.WriteString(":")
			url.WriteString(gCloudPort)
			url.WriteString("/v1/sys/health")

			ok, err := isVaultPrimaryInstance(url.String())
			if err != nil {
				log.WithError(err).WithField("url", url.String()).Fatalf("Could not call Vault instance!")
			}
			switch ok {
			case false:
				if err := conn.Close(); err != nil {
					log.WithError(err).Warnf("Could not close the test socket connection...")
				}
				handlerExitCommand(tunnel)
			case true:
				log.WithField("PrimaryInstanceName", instance.Name).Infof("Primary Vault instance detected ;)")
				return VaultTunnelConn{conn, tunnel}, err
			}
		}
	}
	return v, fmt.Errorf("empty tunnel")
}

func GetAvailableLocalTCPPort() (port string, err error) {
	var minPort int64 = 1024
	var maxPort int64 = 65534
	maxTries := 10
	// Check whether the TCP port is available, increment it otherwise until we find a free port
	for {
		r, err := rand.Int(rand.Reader, big.NewInt(maxPort-minPort))
		if err != nil {
			return port, err
		}
		port = strconv.Itoa(int(r.Int64() + minPort))
		conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", port))
		if conn == nil {
			// When conn is nil, it means the port is free
			break
		}
		err = conn.Close()
		if err != nil {
			log.WithError(err).Warnf("could not close test TCP port")
		}
		maxTries = maxTries - 1
		if maxTries == 0 {
			return port, fmt.Errorf("could not find an available port after 10 retries :(")
		}
	}
	return port, err
}

func handlerExitCommand(cmd *exec.Cmd) {
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		log.WithError(err).Warnf("Could not gracefully interrupt the tunnel...")
	}
	if err := cmd.Process.Release(); err != nil {
		log.WithError(err).Warnf("Could not release tunnel OS resources...")
	}
}

func isVaultPrimaryInstance(url string) (b bool, err error) {
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
			log.WithError(err).Warnf("Could not close HTTP connection to Vault...")
		}
	}()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	var health vault.HealthResponse
	err = json.Unmarshal(bodyBytes, &health)
	if err != nil {
		return false, err
	}
	return !health.Standby, err
}

func main() {
	var v VaultTunnelConn
	var err error
	var p tcpproxy.Proxy
	listenerAddr := VaultTunnelLocalAddr + ":" + strconv.Itoa(VaultRemotePort)

	provider := "GCE"
	ctx := context.Background()
	customFormatter := new(log.TextFormatter)
	customFormatter.TimestampFormat = "2006-01-02 15:04:05"
	customFormatter.FullTimestamp = true
	log.SetFormatter(customFormatter)

	project, ok := os.LookupEnv("GOOGLE_PROJECT")
	if !ok {
		log.Fatalf("GOOGLE_PROJECT environment variable must be set!")
	}

	// TODO: implement new discovery providers like Kubernetes
	if strings.EqualFold(provider, "GCE") {
		v, err = GCEGetVaultTunnelConn(ctx, project)
		defer func() {
			if err := v.conn.Close(); err != nil {
				log.WithError(err).WithField("remoteAddr", v.conn.RemoteAddr()).Warnf("Could not close local tunnel connection")
			}
			handlerExitCommand(v.cmd)
		}()
		if err != nil {
			log.Fatal(err)
		}
	}

	signals := make(chan os.Signal, 1)
	stop := make(chan bool)
	signal.Notify(signals, os.Interrupt)
	go func() {
		for range signals {
			log.Println("Received an interrupt, stopping...")
			stop <- true
		}
	}()

	p.AddRoute(listenerAddr, tcpproxy.To(v.conn.RemoteAddr().String()))
	err = p.Start()
	if err != nil {
		log.WithError(err).Fatal("Could not start listener")
	}
	log.WithField("addr", listenerAddr).Info("Started listener...")
	<-stop
}
