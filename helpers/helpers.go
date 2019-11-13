package helpers

import (
	log "github.com/sirupsen/logrus"

	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/hashicorp/vault/api"
	"io/ioutil"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
)

func HandlerExitCommand(cmd *exec.Cmd) {
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		log.WithError(err).Warnf("Could not gracefully interrupt the tunnel...")
	}
	if err := cmd.Process.Release(); err != nil {
		log.WithError(err).Warnf("Could not release tunnel OS resources...")
	}
}

func HandlerCloseConn(conn net.Conn, wrappedError error) {
	if err := conn.Close(); err != nil {
		log.WithError(err).WithField("wrappedError", wrappedError).Warnf("Could not close the test socket connection...")
	}
}

func IsVaultPrimaryInstance(vaultUrl string) (b bool, err error) {
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
	resp, err := client.Get(vaultUrl + "/v1/sys/health")
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
	var health api.HealthResponse
	err = json.Unmarshal(bodyBytes, &health)
	if err != nil {
		return false, err
	}
	return !health.Standby, err
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
			// When Conn is nil, it means the port is free
			break
		}
		HandlerCloseConn(conn, fmt.Errorf("could not close test TCP port"))
		maxTries = maxTries - 1
		if maxTries == 0 {
			return port, fmt.Errorf("could not find an available port after 10 retries :(")
		}
	}
	return port, err
}
