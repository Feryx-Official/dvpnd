// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package cmd

import (
	"bufio"
	gocontext "context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	sdkmath "cosmossdk.io/math"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/gin-gonic/gin"
	v1base "github.com/sentinel-official/sentinelhub/v12/types/v1"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/Feryx-Official/dvpnd/v9/api"
	"github.com/Feryx-Official/dvpnd/v9/context"
	"github.com/Feryx-Official/dvpnd/v9/libs/bandwidth"
	"github.com/Feryx-Official/dvpnd/v9/libs/geoip"
	"github.com/Feryx-Official/dvpnd/v9/lite"
	"github.com/Feryx-Official/dvpnd/v9/node"
	"github.com/Feryx-Official/dvpnd/v9/services"
	"github.com/Feryx-Official/dvpnd/v9/services/common"
	"github.com/Feryx-Official/dvpnd/v9/types"
	"github.com/Feryx-Official/dvpnd/v9/utils"
)

func init() {
	gin.SetMode(gin.ReleaseMode)
}

// bandwidthTimeout bounds the whole bandwidth measurement at start: a few
// transfers of ten to fifteen seconds each plus the server pings, with room to
// spare, but never an open-ended wait on a remote service.
const bandwidthTimeout = 5 * time.Minute

// checkGrants refuses to start a hot-key node whose grants are missing or
// expired, since its every transaction would fail, and logs what will run
// out within lite.GrantWarningWindow.
func checkGrants(log cmtlog.Logger, client *lite.Client, config *types.Config) error {
	problems, warnings, err := client.CheckGrants(time.Now(), expectedFees(config, lite.GrantWarningWindow))
	if err != nil {
		return err
	}
	for _, w := range warnings {
		log.Error("Renew the node's grants soon (dvpnd keys authz-commands)", "warning", w)
	}
	if len(problems) > 0 {
		return fmt.Errorf("the hot key cannot act for the node account: %s; "+
			"run \"dvpnd keys authz-commands\" and make the grants it prints from the node account's wallet",
			strings.Join(problems, "; "))
	}

	return nil
}

// expectedFees is roughly what the node spends on fees over d: a status
// update and a session report per interval, at the configured gas and price.
func expectedFees(config *types.Config, d time.Duration) sdk.Coins {
	prices, err := sdk.ParseDecCoins(config.Chain.GasPrices)
	if err != nil {
		return nil
	}

	var txs int64
	for _, interval := range []time.Duration{config.Node.IntervalUpdateStatus, config.Node.IntervalUpdateSessions} {
		if interval > 0 {
			txs += int64(d / interval)
		}
	}
	gas := sdkmath.LegacyNewDec(int64(config.Chain.Gas)).Mul(sdkmath.LegacyMustNewDecFromStr(
		strconv.FormatFloat(config.Chain.GasAdjustment, 'f', -1, 64)))

	var fees sdk.Coins
	for _, price := range prices {
		amount := price.Amount.Mul(gas).MulInt64(txs).Ceil().TruncateInt()
		fees = fees.Add(sdk.NewCoin(price.Denom, amount))
	}

	return fees
}

// fitInterval is the interval to use for a job the chain times out after
// statusTimeout: the configured one, unless it exceeds 80% of the timeout,
// which leaves a missed tick room before the chain deactivates the node or
// ends the session. lower says whether it was lowered.
func fitInterval(configured, statusTimeout time.Duration) (limit time.Duration, lower bool) {
	if limit = statusTimeout * 4 / 5; limit > 0 && configured > limit {
		return limit, true
	}

	return configured, false
}

// granterOf parses [keyring] granter: nil when it is empty, an error when it
// is not an address or is the signing key's own. A node that signs with the
// node account's own key has no hot key, and leaves the field empty.
func granterOf(value string, signer sdk.AccAddress) (sdk.AccAddress, error) {
	if value == "" {
		return nil, nil
	}
	granter, err := sdk.AccAddressFromBech32(value)
	if err != nil {
		return nil, err
	}
	if granter.Equals(signer) {
		return nil, errors.New("[keyring] granter is the signing key's own address; leave it empty")
	}

	return granter, nil
}

// apiPort is the TCP port of the node API's listen address; zero when it
// names none.
func apiPort(listenOn string) uint16 {
	_, port, err := net.SplitHostPort(listenOn)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return 0
	}

	return uint16(v)
}

func StartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the VPN node",
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			var (
				home         = viper.GetString(flags.FlagHome)
				configPath   = filepath.Join(home, types.ConfigFileName)
				databasePath = filepath.Join(home, types.DatabaseFileName)
			)

			log, err := utils.PrepareLogger()
			if err != nil {
				return err
			}
			common.SetVerbose(utils.DebugLogging())

			if hint := types.LegacyHomeHint(home); hint != "" {
				log.Info(hint)
			}

			v := viper.New()
			v.SetConfigFile(configPath)

			log.Info("Reading the configuration file", "path", configPath)
			config, err := types.ReadInConfig(v)
			if err != nil {
				return err
			}

			skipConfigValidation, err := cmd.Flags().GetBool(flagSkipConfigValidation)
			if err != nil {
				return err
			}

			if !skipConfigValidation {
				log.Info("Validating the configuration", "data", config.Redacted())
				if err = config.Validate(); err != nil {
					return err
				}
			}

			protocol, err := services.Lookup(config.Node.Type)
			if err != nil {
				return err
			}

			service, err := protocol.New(config)
			if err != nil {
				return err
			}

			var (
				input   = bufio.NewReader(cmd.InOrStdin())
				remotes = strings.Split(config.Chain.RPCAddresses, ",")
			)

			log.Info("Initializing the keyring", "name", types.KeyringName, "backend", config.Keyring.Backend)
			kr, err := keyring.New(types.KeyringName, config.Keyring.Backend, home, input, lite.DefaultEncodingConfig().Codec)
			if err != nil {
				return err
			}

			info, err := kr.Key(config.Keyring.From)
			if err != nil {
				return err
			}

			fromAddr, err := info.GetAddress()
			if err != nil {
				return err
			}

			client := lite.NewDefaultClient().
				WithChainID(config.Chain.ID).
				WithFromAddress(fromAddr).
				WithFromName(config.Keyring.From).
				WithGas(config.Chain.Gas).
				WithGasAdjustment(config.Chain.GasAdjustment).
				WithGasPrices(config.Chain.GasPrices).
				WithKeyring(kr).
				WithLogger(log).
				WithQueryTimeout(config.Chain.RPCQueryTimeout).
				WithRemotes(remotes).
				WithSignModeStr("").
				WithSimulateAndExecute(config.Chain.SimulateAndExecute).
				WithTxTimeout(config.Chain.RPCTxTimeout)

			granter, err := granterOf(config.Keyring.Granter, client.FromAddress())
			if err != nil {
				return err
			}
			if granter != nil {
				client = client.WithGranter(granter)
				log.Info("Signing with a hot key for the node account", "key", client.FromAddress(), "node_account", granter)
			}

			account, err := client.QueryAccount(client.FromAddress())
			if err != nil {
				return err
			}
			if account == nil {
				if client.Granter() != nil {
					return fmt.Errorf("account %s does not exist yet; the granter's fee grant creates it "+
						"(dvpnd keys authz-commands)", client.FromAddress())
				}
				return fmt.Errorf("account does not exist with address %s", client.FromAddress())
			}

			if client.Granter() != nil {
				if err = checkGrants(log, client, config); err != nil {
					return err
				}
			}

			// The chain deactivates a node, and cancels a session, whose last update
			// is older than the respective status_timeout parameter. Whatever the
			// operator configured, never update less often than 80% of that.
			if params, err := client.QueryNodeParams(); err != nil {
				return err
			} else if limit, lower := fitInterval(config.Node.IntervalUpdateStatus, params.StatusTimeout); lower {
				log.Info("Lowering interval_update_status to fit the chain's node status_timeout",
					"configured", config.Node.IntervalUpdateStatus, "status_timeout", params.StatusTimeout, "effective", limit)
				config.Node.IntervalUpdateStatus = limit
			}
			if params, err := client.QuerySessionParams(); err != nil {
				return err
			} else if limit, lower := fitInterval(config.Node.IntervalUpdateSessions, params.StatusTimeout); lower {
				log.Info("Lowering interval_update_sessions to fit the chain's session status_timeout",
					"configured", config.Node.IntervalUpdateSessions, "status_timeout", params.StatusTimeout, "effective", limit)
				config.Node.IntervalUpdateSessions = limit
			}

			log.Info("Discovering the public IP and location", "provider", config.GeoIP.Provider)
			location, err := geoip.Location(geoip.Options{
				Provider:  config.GeoIP.Provider,
				URL:       config.GeoIP.URL,
				Token:     config.GeoIP.Token,
				IP:        config.Node.IPv4Address,
				Logger:    log,
				City:      config.GeoIP.City,
				Country:   config.GeoIP.Country,
				Latitude:  config.GeoIP.Latitude,
				Longitude: config.GeoIP.Longitude,
			})
			if err != nil {
				return err
			}
			log.Info("Public IP and location", "ip", location.IP, "city", location.City, "country", location.Country,
				"country_code", location.CountryCode, "source", location.Source)

			// The bandwidth the node advertises: declared in the config, else
			// measured (or read back from the last measurement). Nothing on the
			// chain needs it, so a failure is logged, not fatal: a node that
			// cannot reach a speed-test service still serves clients.
			measureCtx, cancelMeasure := gocontext.WithTimeout(gocontext.Background(), bandwidthTimeout)
			measured := bandwidth.Measure(measureCtx, bandwidth.Options{
				UploadMbps:   config.Bandwidth.UploadMbps,
				DownloadMbps: config.Bandwidth.DownloadMbps,
				Latitude:     location.Latitude,
				Longitude:    location.Longitude,
				IP:           location.IP,
				Home:         home,
				Logger:       log,
			})
			cancelMeasure()
			bw := v1base.NewBandwidthFromInt64(measured.Upload, measured.Download)
			log.Info("Bandwidth to advertise", "upload", bw.Upload, "download", bw.Download, "source", measured.Source)

			// The proxies render the egress policy into their configuration
			// in Init; the tunnel services install it as firewall rules in
			// Start.
			egress := common.Egress{AllowSMTP: config.Egress.AllowSMTP, APIPort: apiPort(config.Node.ListenOn)}
			common.SetEgress(egress)
			if egress.AllowSMTP {
				log.Info("Clients may send mail: [egress] allow_smtp is on")
			}

			// The protocol daemons read their files from the runtime
			// directory and, when the node runs as root and the proxy account
			// exists, run as that account.
			rt, err := common.PrepareRuntime(home, os.Geteuid(), protocol.Daemon)
			if err != nil {
				return err
			}
			common.SetRuntime(rt)
			switch {
			case rt.Proxy != nil:
				log.Info("Protocol daemons run unprivileged", "account", common.ProxyUserName, "runtime", rt.Dir)
			case os.Geteuid() == 0 && protocol.Daemon:
				log.Error("Protocol daemons run as root: create the " + common.ProxyUserName +
					" system account (the installer and the Docker image do) to run them unprivileged")
			}

			log.Info("Initializing the VPN service", "type", service.Type())
			if err = service.Init(home); err != nil {
				return err
			}

			// The resolver binds the tunnel address, which Init has settled;
			// it starts once the tunnel is up, and the tunnel's firewall lets
			// clients reach it. Without hnsd installed it stays off, and the
			// root document says so, rather than clients being sent to a
			// resolver that is not there.
			if config.Handshake.Enable {
				host, ok := service.(types.TunnelHost)
				if _, err := exec.LookPath(hnsdBinary); err != nil || !ok {
					log.Error("The Handshake resolver stays off: hnsd is not installed or the node type has no tunnel")
					config.Handshake.Enable = false
				} else {
					egress.Resolver = host.TunnelIPv4()
					common.SetEgress(egress)
				}
			}

			// The stop signals are caught from here on: left to their default
			// action they end the process at once, with no deferred call run,
			// so the service started below would outlive the node.
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			defer signal.Stop(sigCh)

			log.Info("Starting the VPN service", "type", service.Type())
			if err = service.Start(); err != nil {
				return err
			}
			// From here on, however the command ends (a stop signal, a job
			// that failed, or a step below that fails), the VPN service
			// stops first, so the tunnel interface, the firewall rules or the
			// proxy child process do not outlive the node.
			defer func() {
				log.Info("Stopping the VPN service", "type", service.Type())
				if stopErr := service.Stop(); stopErr != nil {
					log.Error("failed to stop the VPN service", "error", stopErr)
					if err == nil {
						err = stopErr
					}
				}
				// The files the node wrote for the daemon go with it: its
				// configuration can hold a REALITY private key or the stats
				// secret, and one is a copy of the TLS key.
				if rmErr := rt.RemoveFiles(); rmErr != nil {
					log.Error("failed to remove the daemons' runtime files", "error", rmErr)
				}
			}()

			// From here the node also stops when the protocol daemon exits, and
			// it looks for a reason to stop between the steps that take time. A
			// daemon that cannot run exits within the grace, before the node goes
			// active on the chain.
			stop := newStopper(log, sigCh, service)
			grace := time.Duration(0)
			if stop.daemon != nil {
				grace = daemonStartGrace
			}
			if halt, stopErr := stop.within(grace); halt {
				return stopErr
			}

			// The resolver binds the tunnel address, so it stops before the
			// service takes the tunnel down (deferred calls run in reverse).
			if egress.Resolver != nil {
				resolver := startHandshake(log, config.Handshake.Peers, egress.Resolver)
				defer resolver.Stop()
			}

			log.Info("Opening the database", "path", databasePath)
			database, err := node.OpenDatabase(databasePath)
			if err != nil {
				return err
			}

			var (
				ctx    = context.NewContext()
				router = gin.New()
			)

			ctx = ctx.WithBandwidth(&bw).
				WithBandwidthSource(measured.Source).
				WithClient(client).
				WithConfig(config).
				WithDatabase(database).
				WithHandler(router).
				WithLocation(location).
				WithLogger(log).
				WithService(service)

			// The routes read the configuration, so they are registered once
			// the context carries it.
			api.RegisterRoutes(ctx, router)
			if config.Node.LegacyHandshake {
				log.Info("The legacy handshake endpoint is on: [node] legacy_handshake")
			}

			// The API port is bound and the certificate loaded before the node
			// goes active on the chain: a node that cannot serve stops first.
			port, err := utils.ListenTLS(config.Node.ListenOn, filepath.Join(home, "tls.crt"), filepath.Join(home, "tls.key"))
			if err != nil {
				return err
			}
			defer func() { _ = port.Close() }()

			n := node.NewNode(ctx)
			if halt, stopErr := stop.within(0); halt {
				return stopErr
			}
			if err = n.Initialize(); err != nil {
				return err
			}

			if halt, stopErr := stop.within(0); halt {
				return stopErr
			}
			if err = n.ReconcileSessions(); err != nil {
				return err
			}

			// Run until a stop signal arrives, the protocol daemon exits, the
			// API server fails or a job panics; the deferred stop above then
			// takes the service down.
			errCh := make(chan error, 1)
			go func() { errCh <- n.Start(port) }()
			stop.failed = errCh
			_, err = stop.within(-1)

			return err
		},
	}

	cmd.Flags().Bool(flagSkipConfigValidation, false, "skip the validation of configuration")

	return cmd
}
