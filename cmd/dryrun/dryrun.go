package main

import (
	"os"

	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"

	"github.com/libopenstorage/operator/pkg/dryrun"
	"github.com/libopenstorage/operator/pkg/version"
)

const (
	flagVerbose            = "verbose"
	flagStorageCluser      = "storagecluster"
	flagKubeConfig         = "kubeconfig"
	flagOutputFile         = "output"
	flagDaemonSetMigration = "migration"
)

func main() {
	app := cli.NewApp()
	app.Name = "portworx-operator-dryrun"
	app.Usage = "Portworx operator dry run tool"
	app.Version = version.Version
	app.Action = execute

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  flagVerbose,
			Usage: "Enable verbose logging",
		},
		cli.StringFlag{
			Name:  flagStorageCluser,
			Usage: "File for storage cluster spec, retrieve from k8s if it's not configured",
		},
		cli.StringFlag{
			Name:  flagKubeConfig,
			Usage: "kubeconfig file",
		},
		cli.StringFlag{
			Name:  flagOutputFile,
			Usage: "output file to save k8s objects in yaml format",
		},
		cli.BoolFlag{
			Name:  flagDaemonSetMigration,
			Usage: "dry run daemonSet to operator migration",
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("Error starting: %v", err)
	}
}

func execute(c *cli.Context) {
	verbose := c.Bool(flagVerbose)
	if verbose {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

	d := dryrun.DryRun{}
	var err error
	err = d.Init(c.String(flagKubeConfig), c.String(flagOutputFile), c.String(flagStorageCluser))
	if err != nil {
		log.WithError(err).Fatal("failed to initialize")
	}

	if err = d.Execute(); err != nil {
		log.WithError(err).Errorf("dryrun failed")
	}
}
