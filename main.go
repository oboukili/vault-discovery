package main

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	vault "github.com/hashicorp/vault/api"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
	"io"
	"io/ioutil"
	"math/big"
	"net"
	"net/http"
	//"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const VaultPort = 8200
const VaultLocalTunnelAddr = "127.0.0.1:8200"

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

func GCEGetVaultTunnelConn(ctx context.Context, project string) (VaultTunnelConn, error) {
	var url strings.Builder
	var conn net.Conn

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

			port, err := GetRandomOpenTCPPort()
			if err != nil {
				log.WithError(err).Fatalf("Could not generate a random port number!")
			}
			if _, err := exec.LookPath("gcloud"); err != nil {
				log.WithError(err).Fatalf("Could not find gcloud in PATH!")
			}

			tunnel := exec.Command("gcloud", "compute",
				"start-iap-tunnel", instance.Name, strconv.Itoa(VaultPort),
				"--project", project,
				"--local-host-port", "localhost:"+strconv.Itoa(port),
				"--zone", zone)
			// TODO: for easy debugging, cleanup after dev phase
			//tunnel.Stdout = os.Stdout
			//tunnel.Stderr = os.Stderr
			if err := tunnel.Start(); err != nil {
				log.WithError(err).WithField("args", tunnel.Args).Fatalf("Could not start IAP tunnel!")
			}
			// Wait for IAP tunnel socket to be open before continuing
			startingTimestampInSeconds := time.Now().Second()
			for {
				conn, err = net.Dial("tcp", "localhost:"+strconv.Itoa(port))
				if conn != nil {
					if err = conn.Close(); err != nil {
						log.WithError(err).Warnf("Could not close the test socket connection...")
					}
					break
				}
				time.Sleep(1 * time.Second)
				if time.Now().Second() > startingTimestampInSeconds+10 {
					log.Fatalf("Could not connect to the TCP tunnel within 10 seconds!")
					break
				}
			}

			url.Reset()
			url.WriteString("https://localhost:")
			url.WriteString(strconv.Itoa(port))
			url.WriteString("/v1/sys/health")
			ok, err := IsVaultPrimaryInstance(url.String())
			if err != nil {
				log.WithError(err).WithField("url", url.String()).Fatalf("Could not call Vault instance!")
			}
			switch ok {
			case false:
				if err := tunnel.Process.Signal(os.Interrupt); err != nil {
					log.WithError(err).Warnf("Could not gracefully interrupt the tunnel...")
				}
				if err := tunnel.Process.Release(); err != nil {
					log.WithError(err).Warnf("Could not release tunnel process resources...")
				}
			case true:
				log.Printf("%s is primary", instance.Name)
				conn, err = net.Dial("tcp", "localhost:"+strconv.Itoa(port))
				if err != nil || conn == nil {
					return VaultTunnelConn{nil, nil}, err
				}
				return VaultTunnelConn{conn, tunnel}, err
			}
		}
	}
	return VaultTunnelConn{nil, nil}, fmt.Errorf("empty tunnel")
}

func GetRandomOpenTCPPort() (port int, err error) {
	var minPort int64 = 1024
	var maxPort int64 = 65534
	r, err := rand.Int(rand.Reader, big.NewInt(maxPort-minPort))
	if err != nil {
		return 0, err
	}
	n := r.Int64()
	port = int(n + minPort)

	// Check whether the TCP port is available, increment it otherwise until we find a free port
	for {
		conn, _ := net.DialTimeout("tcp", net.JoinHostPort("localhost", strconv.Itoa(port)), 1*time.Second)
		switch conn {
		case nil:
			return port, err
		default:
			if err := conn.Close(); err != nil {
				log.WithError(err).Warnf("Could not clean up the TCP test connection...")
			}
		}
		port = port + 1
		if port > int(maxPort) {
			break
		}
	}
	return port, err
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
	provider := "GCE"
	ctx := context.Background()

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
			if err := v.cmd.Process.Signal(os.Interrupt); err != nil {
				log.WithError(err).Warnf("Could not gracefully interrupt the tunnel...")
			}
			if err := v.cmd.Process.Release(); err != nil {
				log.WithError(err).Warnf("Could not release tunnel process resources...")
			}
		}()
		if err != nil {
			log.Fatal(err)
		}
	}

	l, err := net.Listen("tcp4", VaultLocalTunnelAddr)
	if err != nil {
		log.WithError(err).Fatal("Could not listen on " + VaultLocalTunnelAddr)
	}
	defer func() {
		if err := l.Close(); err != nil {
			log.WithError(err).Warnf("Could not close TCP connection listener")
		}
	}()

	for {
		c, err := l.Accept()
		if err != nil {
			log.WithError(err).Fatal()
		}
		go handleConnection(c, v.conn)
	}
}

func handleConnection(client net.Conn, tunnel net.Conn) {
	// net.Conn implements io.Writer and io.Reader :)
	go func() {
		if _, err := io.Copy(tunnel, client); err != nil {
			log.WithError(err).Fatal("Error sending request data from client to tunnel")
		}
	}()
	go func() {
		if _, err := io.Copy(client, tunnel); err != nil {
			log.WithError(err).Fatal("Error sending response data from tunnel to client")
		}
	}()
	if err := client.Close(); err != nil {
		log.WithError(err).Warnf("Could not close TCP connection handler")
	}
}
