package main

import (
	"os"

	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"

	"github.com/libopenstorage/operator/pkg/dryrun"
	"github.com/libopenstorage/operator/pkg/version"
)

const (
	flagVerbose        = "verbose"
	flagStorageCluster = "storagecluster"
	flagKubeConfig     = "kubeconfig"
	flagOutputFolder   = "output"
)

func main() {
	app := cli.NewApp()
	app.Name = "portworx-operator-dryrun"
	app.Usage = "Portworx operator dry run tool"
	app.Version = version.Version
	app.Action = execute

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			// vb is alias for the arg.
			// v is already used by default arg --version.
			Name:  flagVerbose + ",vb",
			Usage: "Enable verbose logging",
		},
		cli.StringFlag{
			Name:  flagStorageCluster + ",stc",
			Usage: "File for storage cluster spec, retrieve from k8s if it's not configured",
		},
		cli.StringFlag{
			Name:  flagKubeConfig + ",kc",
			Usage: "kubeconfig file, identical to KUBECONFIG env variable",
		},
		cli.StringFlag{
			Name:  flagOutputFolder + ",o",
			Usage: "output folder to save k8s objects in yaml files",
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("Error starting: %v", err)
	}
}

func execute(c *cli.Context) {
	verbose := c.Bool(flagVerbose)
	if verbose {
		log.Info("verbose log enabled")
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

	d := &dryrun.DryRun{}
	var err error
	err = d.Init(
		c.String(flagKubeConfig),
		c.String(flagOutputFolder),
		c.String(flagStorageCluster),
	)
	if err != nil {
		log.WithError(err).Fatal("failed to initialize")
	}

	if err = d.Execute(); err != nil {
		log.WithError(err).Errorf("dryrun failed")
	}
}
