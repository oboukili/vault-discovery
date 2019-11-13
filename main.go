package main

import (
	log "github.com/sirupsen/logrus"

	"fmt"
	"github.com/google/tcpproxy"
	"gitlab.com/maltcommunity/public/vault-discovery/gce"
	"gitlab.com/maltcommunity/public/vault-discovery/helpers"
	"gitlab.com/maltcommunity/public/vault-discovery/types"
	"golang.org/x/net/context"
	"os"
	"os/signal"
)

const VaultRemoteHTTPScheme = "https"
const VaultRemotePort = "8200"
const VaultTunnelLocalAddr = "127.0.0.1"

func main() {
	var err error
	var p tcpproxy.Proxy
	var provider string
	var v types.VaultTunnelConn

	listenerAddr := VaultTunnelLocalAddr + ":" + VaultRemotePort
	ctx := context.Background()
	customFormatter := new(log.TextFormatter)
	customFormatter.TimestampFormat = "2006-01-02 15:04:05"
	customFormatter.FullTimestamp = true
	log.SetFormatter(customFormatter)

	log.Infof("Starting vault-discovery ;)")

	project, ok := os.LookupEnv("GOOGLE_PROJECT")
	if !ok {
		log.Fatalf("GOOGLE_PROJECT environment variable must be set!")
	}
	provider, ok = os.LookupEnv("DISCOVERY_PROVIDER")
	if !ok {
		provider = "GCE"
	}

	switch provider {
	case "GCE":
		v, err = gce.GetVaultPrimaryTunnelConn(ctx, project, types.VaultTunnelConnAttrs{
			LocalAddr:   VaultTunnelLocalAddr,
			LocalPort:   VaultRemotePort,
			VaultScheme: VaultRemoteHTTPScheme,
		})
		defer func() {
			helpers.HandlerCloseConn(v.Conn, fmt.Errorf("could not close local tunnel connection: %s", v.Conn.RemoteAddr()))
			helpers.HandlerExitCommand(v.Cmd)
		}()
		if err != nil {
			log.Fatal(err)
		}
	default:
		log.WithField("provider", provider).Fatalf("Unsupported provider!")
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

	p.AddRoute(listenerAddr, tcpproxy.To(v.Conn.RemoteAddr().String()))
	err = p.Start()
	if err != nil {
		log.WithError(err).Fatal("Could not start listener")
	}
	log.WithField("addr", listenerAddr).Info("Started listener...")
	<-stop
}
