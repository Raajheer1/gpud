package command

import (
	"errors"
	"fmt"
	"time"

	"github.com/urfave/cli"

	"github.com/leptonai/gpud/pkg/config"
	"github.com/leptonai/gpud/version"
)

const usage = `
# to quick scan for your machine health status
gpud scan

# to start gpud as a systemd unit
sudo gpud up
`

var (
	logLevel string
	logFile  string

	statusWatch bool

	annotations   string
	listenAddress string

	pprof bool

	retentionPeriod time.Duration

	enableAutoUpdate   bool
	autoUpdateExitCode int

	kernelModulesToCheck         cli.StringSlice
	dockerIgnoreConnectionErrors bool
	ibstatCommand                string
)

const (
	inProgress  = "\033[33m⌛\033[0m"
	checkMark   = "\033[32m✔\033[0m"
	warningSign = "\033[31m✘\033[0m"
)

var (
	ErrEmptyToken = errors.New("token is empty")
)

func App() *cli.App {
	app := cli.NewApp()

	app.Name = "gpud"
	app.Version = version.Version
	app.Usage = usage
	app.Description = "monitor your GPU/CPU machines and run workloads"

	app.Commands = []cli.Command{
		{
			Name:  "login",
			Usage: "login gpud to lepton.ai (called automatically in gpud up with non-empty --token)",
			UsageText: `# to login gpud to lepton.ai with an existing, running gpud
sudo gpud login --token <LEPTON_AI_TOKEN>
`,
			Action: cmdLogin,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "token",
					Usage: "lepton.ai workspace token for checking in",
				},
				cli.StringFlag{
					Name:  "endpoint",
					Usage: "endpoint for control plane",
					Value: "mothership-machine.app.lepton.ai",
				},
			},
		},
		{
			Name:  "up",
			Usage: "initialize and start gpud in a daemon mode (systemd)",
			UsageText: `# to start gpud as a systemd unit (recommended)
sudo gpud up

# to enable machine monitoring powered by lepton.ai platform
# sign up here: https://lepton.ai
sudo gpud up --token <LEPTON_AI_TOKEN>

# to start gpud without a systemd unit (e.g., mac)
gpud run

# or
nohup sudo gpud run &>> <your log file path> &
`,
			Action: cmdUp,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "token",
					Usage: "lepton.ai workspace token for checking in",
				},
				cli.StringFlag{
					Name:  "endpoint",
					Usage: "endpoint for checking in",
					Value: "mothership-machine.app.lepton.ai",
				},
			},
		},
		{
			Name:   "kubeconfig",
			Usage:  "Writes the kubeconfig with gpud.",
			Action: cmdKubeConfig,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "file",
					Usage: "file path to output the kubelet config",
				},
				cli.StringFlag{
					Name:  "region",
					Usage: "region of target cluster",
				},
				cli.StringFlag{
					Name:  "cluster",
					Usage: "name of target cluster",
				},
				cli.StringFlag{
					Name:  "role",
					Usage: "role",
				},
				cli.StringFlag{
					Name:  "session",
					Usage: "cluster session name",
				},
				cli.StringFlag{
					Name:  "cluster-ca",
					Usage: "cluster ca file path",
				},
			},
		},
		{
			Name:  "down",
			Usage: "stop gpud systemd unit",
			UsageText: `# to stop the existing gpud systemd unit
sudo gpud down

# to uninstall gpud
sudo rm /usr/sbin/gpud
sudo rm /etc/systemd/system/gpud.service
`,
			Action: cmdDown,
		},
		{
			Name:   "run",
			Usage:  "starts gpud without any login/checkin ('gpud up' is recommended for linux)",
			Action: cmdRun,
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:        "log-level,l",
					Usage:       "set the logging level [debug, info, warn, error, fatal, panic, dpanic]",
					Destination: &logLevel,
				},
				&cli.StringFlag{
					Name:        "log-file",
					Usage:       "set the log file path (set empty to stdout/stderr)",
					Destination: &logFile,
					Value:       "",
				},
				&cli.StringFlag{
					Name:        "listen-address",
					Usage:       "set the listen address",
					Destination: &listenAddress,
					Value:       fmt.Sprintf("0.0.0.0:%d", config.DefaultGPUdPort),
				},
				&cli.StringFlag{
					Name:        "annotations",
					Usage:       "set the annotations",
					Destination: &annotations,
				},
				&cli.BoolFlag{
					Name:        "pprof",
					Usage:       "enable pprof (default: false)",
					Destination: &pprof,
				},
				&cli.DurationFlag{
					Name:        "retention-period",
					Usage:       "set the time period to retain metrics for (once elapsed, old records are compacted/purged)",
					Destination: &retentionPeriod,
					Value:       config.DefaultRetentionPeriod.Duration,
				},
				cli.StringFlag{
					Name:  "endpoint",
					Usage: "endpoint for control plane",
					Value: "mothership-machine.app.lepton.ai",
				},
				&cli.BoolTFlag{
					Name:        "enable-auto-update",
					Usage:       "enable auto update of gpud (default: true)",
					Destination: &enableAutoUpdate,
				},
				&cli.IntFlag{
					Name:        "auto-update-exit-code",
					Usage:       "specifies the exit code to exit with when auto updating (default: -1 to disable exit code)",
					Destination: &autoUpdateExitCode,
					Value:       -1,
				},
				&cli.StringSliceFlag{
					Name:  "kernel-modules-to-check",
					Usage: "enable 'kernel-module' component that returns healthy if and only if all the kernel modules are loaded (default: [], use '--kernel-modules-to-check=a --kernel-modules-to-check=b' for multiple modules)",
					Value: &kernelModulesToCheck,
				},
				&cli.BoolFlag{
					Name:        "docker-ignore-connection-errors",
					Usage:       "ignore connection errors to docker daemon, useful when docker daemon is not running (default: false)",
					Destination: &dockerIgnoreConnectionErrors,
				},

				// only for testing
				cli.StringFlag{
					Name:        "ibstat-command",
					Usage:       "sets the ibstat command (leave empty for default, useful for testing)",
					Destination: &ibstatCommand,
					Hidden:      true,
				},
			},
		},

		// operations
		{
			Name:      "update",
			Usage:     "update gpud",
			UsageText: "",
			Action:    cmdUpdate,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "url",
					Usage: "url for getting a package",
				},
				cli.StringFlag{
					Name:  "next-version",
					Usage: "set the next version",
				},
			},
			Subcommands: []cli.Command{
				{
					Name:   "check",
					Usage:  "check availability of new version gpud",
					Action: cmdUpdateCheck,
				},
			},
		},
		{
			Name:  "release",
			Usage: "release gpud",
			Subcommands: []cli.Command{
				{
					Name:   "gen-key",
					Usage:  "generate root or signing key pair",
					Action: cmdReleaseGenKey,
					Flags: []cli.Flag{
						cli.BoolFlag{
							Name:  "root (default: false)",
							Usage: "generate root key",
						},
						cli.BoolFlag{
							Name:  "signing (default: false)",
							Usage: "generate signing key",
						},
						cli.StringFlag{
							Name:  "priv-path",
							Usage: "path of private key",
						},
						cli.StringFlag{
							Name:  "pub-path",
							Usage: "path of public key",
						},
					},
				},
				{
					Name:   "sign-key",
					Usage:  "Sign signing keys with a root key",
					Action: cmdReleaseSignKey,
					Flags: []cli.Flag{
						cli.StringFlag{
							Name:  "root-priv-path",
							Usage: "path of root private key",
						},
						cli.StringFlag{
							Name:  "sign-pub-path",
							Usage: "path of signing public key",
						},
						cli.StringFlag{
							Name:  "sig-path",
							Usage: "output path of signature path",
						},
					},
				},
				{
					Name:   "verify-key-signature",
					Usage:  "Verify a root signture of the signing keys' bundle",
					Action: cmdReleaseVerifyKeySignature,
					Flags: []cli.Flag{
						cli.StringFlag{
							Name:  "root-pub-path",
							Usage: "path of root public key",
						},
						cli.StringFlag{
							Name:  "sign-pub-path",
							Usage: "path of signing public key",
						},
						cli.StringFlag{
							Name:  "sig-path",
							Usage: "path of signature path",
						},
					},
				},
				{
					Name:   "sign-package",
					Usage:  "Sign a package with a signing key",
					Action: cmdReleaseSignPackage,
					Flags: []cli.Flag{
						cli.StringFlag{
							Name:  "package-path",
							Usage: "path of package",
						},
						cli.StringFlag{
							Name:  "sign-priv-path",
							Usage: "path of signing private key",
						},
						cli.StringFlag{
							Name:  "sig-path",
							Usage: "output path of signature path",
						},
					},
				},
				{
					Name:   "verify-package-signature",
					Usage:  "Verify a package signture using a signing key",
					Action: cmdReleaseVerifyPackageSignature,
					Flags: []cli.Flag{
						cli.StringFlag{
							Name:  "package-path",
							Usage: "path of package",
						},
						cli.StringFlag{
							Name:  "sign-pub-path",
							Usage: "path of signing public key",
						},
						cli.StringFlag{
							Name:  "sig-path",
							Usage: "path of signature path",
						},
					},
				},
			},
		},

		// for notifying control plane state change
		{
			Name:    "notify",
			Aliases: []string{"nt"},

			Usage: "notify control plane of state change",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:        "log-level,l",
					Usage:       "set the logging level [debug, info, warn, error, fatal, panic, dpanic]",
					Destination: &logLevel,
				},
				cli.StringFlag{
					Name:  "endpoint",
					Usage: "endpoint for control plane",
					Value: "mothership-machine.app.lepton.ai",
				},
			},
			Subcommands: []cli.Command{
				{
					Name:   "startup",
					Usage:  "notify machine startup",
					Action: cmdNotifyStartup,
				},
				{
					Name:   "shutdown",
					Usage:  "notify machine shutdown",
					Action: cmdNotifyShutdown,
				},
			},
		},

		// for checking gpud status
		{
			Name:    "status",
			Aliases: []string{"st"},

			Usage:  "checks the status of gpud",
			Action: cmdStatus,
			Flags: []cli.Flag{
				&cli.BoolFlag{
					Name:        "watch, w",
					Usage:       "watch for package install status",
					Destination: &statusWatch,
				},
			},
		},

		{
			Name: "is-nvidia",

			Usage:  "quick check if the host has NVIDIA GPUs installed",
			Action: cmdIsNvidia,
		},
		{
			Name:    "accelerator",
			Aliases: []string{"a"},

			Usage:  "quick scans the currently installed accelerator",
			Action: cmdAccelerator,
		},

		// for diagnose + quick scanning
		{
			Name:    "scan",
			Aliases: []string{"check", "s"},

			Usage:  "quick scans the host for any major issues",
			Action: cmdScan,
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:        "log-level,l",
					Usage:       "set the logging level [debug, info, warn, error, fatal, panic, dpanic]",
					Destination: &logLevel,
				},

				// only for testing
				cli.StringFlag{
					Name:        "ibstat-command",
					Usage:       "sets the ibstat command (leave empty for default, useful for testing)",
					Destination: &ibstatCommand,
					Hidden:      true,
				},
			},
		},
		{
			Name:  "join",
			Usage: "join gpud machine into a lepton cluster",
			UsageText: `# to join gpud into a lepton cluster
sudo gpud join
`,
			Action: cmdJoin,
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "endpoint",
					Usage: "endpoint for control plane",
					Value: "mothership-machine.app.lepton.ai",
				},
				cli.StringFlag{
					Name:  "cluster-name",
					Usage: "cluster name for control plane (e.g.: lepton-prod-0)",
					Value: "lepton-prod-0",
				},
				cli.StringFlag{
					Name:  "provider",
					Usage: "provider of the machine",
				},
				cli.StringFlag{
					Name:  "node-group",
					Usage: "node group to join",
				},
				cli.StringFlag{
					Name:  "private-ip",
					Usage: "can specify private ip for internal network",
				},
				cli.BoolFlag{
					Name:  "skip-interactive",
					Usage: "use detected value instead of prompting for user input",
				},
				cli.StringFlag{
					Name:  "extra-info",
					Usage: "base64 encoded extra info to pass to control plane",
				},
			},
		},
	}

	return app
}
