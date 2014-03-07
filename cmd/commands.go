package cmd

import (
	"bytes"
	"fmt"
	"github.com/smarterclayton/cobra"
	"github.com/smarterclayton/geard/containers"
	"github.com/smarterclayton/geard/dispatcher"
	"github.com/smarterclayton/geard/git"
	"github.com/smarterclayton/geard/http"
	"github.com/smarterclayton/geard/jobs"
	"github.com/smarterclayton/geard/systemd"
	"log"
	nethttp "net/http"
	"os"
	"os/user"
	"strconv"
)

var (
	pre         bool
	post        bool
	follow      bool
	start       bool
	resetEnv    bool
	listenAddr  string
	environment EnvironmentDescription
	portPairs   PortPairs
)

var conf = http.HttpConfiguration{
	Dispatcher: &dispatcher.Dispatcher{
		QueueFast:         10,
		QueueSlow:         1,
		Concurrent:        2,
		TrackDuplicateIds: 1000,
	},
	Extensions: []http.HttpExtension{
		git.Routes,
	},
}

// Parse the command line arguments and invoke one of the support subcommands.
func Execute() {
	gearCmd := &cobra.Command{
		Use:   "gear",
		Short: "Gear(d) is a tool for installing Docker containers to systemd",
		Long:  "A commandline client and server that allows Docker containers to be installed to Systemd in an opinionated and distributed fashion.\n\nComplete documentation is available at http://github.com/smarterclayton/geard",
		Run:   gear,
	}
	gearCmd.PersistentFlags().StringVarP(&(conf.Docker.Socket), "docker-socket", "S", "unix:///var/run/docker.sock", "Set the docker socket to use")

	installImageCmd := &cobra.Command{
		Use:   "install <image> <name>... <key>=<value>",
		Short: "Install a docker image as a systemd service",
		Long:  "Install a docker image as one or more systemd services on one or more servers.\n\nSpecify a location on a remote server with <host>[:<port>]/<name> instead of <name>.  The default port is 2223.",
		Run:   installImage,
	}
	installImageCmd.Flags().VarP(&portPairs, "ports", "p", "List of comma separated port pairs to bind '<internal>=<external>,...'. Use zero to request a port be assigned.")
	installImageCmd.Flags().BoolVar(&start, "start", false, "Start the container immediately")
	installImageCmd.Flags().StringVar(&environment.Path, "env-file", "", "Path to an environment file to load")
	installImageCmd.Flags().StringVar(&environment.Description.Source, "env-url", "", "A url to download environment files from")
	installImageCmd.Flags().StringVar((*string)(&environment.Description.Id), "env-id", "", "An optional identifier for the environment being set")
	gearCmd.AddCommand(installImageCmd)

	setEnvCmd := &cobra.Command{
		Use:   "set-env <name>... <key>=<value>...",
		Short: "Set environment variable values on servers",
		Long:  "Adds the listed environment values to the specified locations. The name is the environment id that multiple containers may reference.",
		Run:   setEnvironment,
	}
	setEnvCmd.Flags().BoolVar(&resetEnv, "reset", false, "Remove any existing values")
	gearCmd.AddCommand(setEnvCmd)

	envCmd := &cobra.Command{
		Use:   "env <name>... <key>=<value>...",
		Short: "Retrieve environment variable values from servers",
		Long:  "Return all environment variables for each server as output",
		Run:   showEnvironment,
	}
	gearCmd.AddCommand(envCmd)

	startCmd := &cobra.Command{
		Use:   "start <name>...",
		Short: "Invoke systemd to start a container",
		Long:  "Queues the start and immediately returns.", //  Use -f to attach to the logs.",
		Run:   startContainer,
	}
	//startCmd.Flags().BoolVarP(&follow, "follow", "f", false, "Attach to the logs after startup")
	gearCmd.AddCommand(startCmd)

	stopCmd := &cobra.Command{
		Use:   "stop <name>...",
		Short: "Invoke systemd to stop a container",
		Long:  ``,
		Run:   stopContainer,
	}
	gearCmd.AddCommand(stopCmd)

	statusCmd := &cobra.Command{
		Use:   "status <name>...",
		Short: "Retrieve the systemd status of one or more containers",
		Long:  "Shows the equivalent of 'systemctl status container-<name>' for each listed unit",
		Run:   containerStatus,
	}
	gearCmd.AddCommand(statusCmd)

	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "(Local) Start the gear server",
		Long:  "Launch the gear HTTP API server as a daemon. Will not send itself to the background.",
		Run:   daemon,
	}
	daemonCmd.Flags().StringVarP(&listenAddr, "listen-address", "A", ":8080", "Set the address for the http endpoint to listen on")
	gearCmd.AddCommand(daemonCmd)

	cleanCmd := &cobra.Command{
		Use:   "clean",
		Short: "(Local) Disable all containers, slices, and targets in systemd",
		Long:  "Disable all registered resources from systemd to allow them to be removed from the system.  Will reload the systemd daemon config.",
		Run:   clean,
	}
	gearCmd.AddCommand(cleanCmd)

	initGearCmd := &cobra.Command{
		Use:   "init <name> <image>",
		Short: "(Local) Setup the environment for a container",
		Long:  "",
		Run:   initGear,
	}
	initGearCmd.Flags().BoolVarP(&pre, "pre", "", false, "Perform pre-start initialization")
	initGearCmd.Flags().BoolVarP(&post, "post", "", false, "Perform post-start initialization")
	gearCmd.AddCommand(initGearCmd)

	genAuthKeysCmd := &cobra.Command{
		Use:   "gen-auth-keys [<name>]",
		Short: "(Local) Create the authorized_keys file for a container",
		Long:  "Generate .ssh/authorized_keys file for the specified container id or (if container id is ommitted) for the current user",
		Run:   genAuthKeys,
	}
	gearCmd.AddCommand(genAuthKeysCmd)

	if err := gearCmd.Execute(); err != nil {
		fail(1, err.Error())
	}
}

// Initializers for local command execution.
func needsSystemd() error {
	systemd.Require()
	return nil
}
func needsSystemdAndData() error {
	systemd.Require()
	return containers.InitializeData()
}

func gear(cmd *cobra.Command, args []string) {
	cmd.Help()
}

func daemon(cmd *cobra.Command, args []string) {
	systemd.Start()
	containers.InitializeData()
	containers.StartPortAllocator(4000, 60000)
	conf.Dispatcher.Start()

	nethttp.Handle("/", conf.Handler())
	log.Printf("Listening for HTTP on %s ...", listenAddr)
	log.Fatal(nethttp.ListenAndServe(listenAddr, nil))
}

func clean(cmd *cobra.Command, args []string) {
	needsSystemd()
	containers.Clean()
}

func installImage(cmd *cobra.Command, args []string) {
	if err := environment.ExtractVariablesFrom(&args, true); err != nil {
		fail(1, err.Error())
	}

	if len(args) < 2 {
		fail(1, "Valid arguments: <image_name> <id> ...\n")
	}

	imageId := args[0]
	if imageId == "" {
		fail(1, "Argument 1 must be a Docker image to base the service on\n")
	}
	ids, err := NewRemoteIdentifiers(args[1:])
	if err != nil {
		fail(1, "You must pass one or more valid service names: %s\n", err.Error())
	}

	Executor{
		On: ids,
		Serial: func(on Locator) jobs.Job {
			return &http.HttpInstallContainerRequest{
				InstallContainerRequest: jobs.InstallContainerRequest{
					RequestIdentifier: jobs.NewRequestIdentifier(),

					Id:      on.(*RemoteIdentifier).Id,
					Image:   imageId,
					Started: start,

					Ports:       *portPairs.Get().(*containers.PortPairs),
					Environment: &environment.Description,
				},
			}
		},
		Output:    os.Stdout,
		LocalInit: needsSystemdAndData,
	}.StreamAndExit()
}

func setEnvironment(cmd *cobra.Command, args []string) {
	if err := environment.ExtractVariablesFrom(&args, false); err != nil {
		fail(1, err.Error())
	}

	if len(args) < 1 {
		fail(1, "Valid arguments: <name>... <key>=<value>...\n")
	}

	ids, err := NewRemoteIdentifiers(args[0:])
	if err != nil {
		fail(1, "You must pass one or more valid service names: %s\n", err.Error())
	}

	Executor{
		On: ids,
		Serial: func(on Locator) jobs.Job {
			environment.Description.Id = on.(*RemoteIdentifier).Id
			if resetEnv {
				return &http.HttpPutEnvironmentRequest{
					PutEnvironmentRequest: jobs.PutEnvironmentRequest{&environment.Description},
				}
			}
			return &http.HttpPatchEnvironmentRequest{
				PatchEnvironmentRequest: jobs.PatchEnvironmentRequest{&environment.Description},
			}
		},
		Output:    os.Stdout,
		LocalInit: needsSystemdAndData,
	}.StreamAndExit()
}

func showEnvironment(cmd *cobra.Command, args []string) {
	if len(args) < 1 {
		fail(1, "Valid arguments: <id> ...\n")
	}
	ids, err := NewRemoteIdentifiers(args)
	if err != nil {
		fail(1, "You must pass one or more valid environment ids: %s\n", err.Error())
	}

	data, errors := Executor{
		On: ids,
		Serial: func(on Locator) jobs.Job {
			return &http.HttpContentRequest{
				ContentRequest: jobs.ContentRequest{
					Locator: string(on.(*RemoteIdentifier).Id),
					Type:    jobs.ContentTypeEnvironment,
				},
			}
		},
		Output: os.Stdout,
	}.Gather()

	for i := range data {
		if buf, ok := data[i].(*bytes.Buffer); ok {
			buf.WriteTo(os.Stdout)
		}
	}
	if len(errors) > 0 {
		for i := range errors {
			fmt.Fprintf(os.Stderr, "Error: %s\n", errors[i])
		}
		os.Exit(1)
	}
	os.Exit(0)
}

func startContainer(cmd *cobra.Command, args []string) {
	if len(args) < 1 {
		fail(1, "Valid arguments: <id> ...\n")
	}
	ids, err := NewRemoteIdentifiers(args)
	if err != nil {
		fail(1, "You must pass one or more valid service names: %s\n", err.Error())
	}

	fmt.Fprintf(os.Stderr, "You can also control this container via 'systemctl start %s'\n", ids[0].(*RemoteIdentifier).Id.UnitNameFor())
	Executor{
		On: ids,
		Serial: func(on Locator) jobs.Job {
			return &http.HttpStartContainerRequest{
				StartedContainerStateRequest: jobs.StartedContainerStateRequest{
					Id: on.(*RemoteIdentifier).Id,
				},
			}
		},
		Output:    os.Stdout,
		LocalInit: needsSystemd,
	}.StreamAndExit()
}

func stopContainer(cmd *cobra.Command, args []string) {
	if len(args) < 1 {
		fail(1, "Valid arguments: <id> ...\n")
	}
	ids, err := NewRemoteIdentifiers(args)
	if err != nil {
		fail(1, "You must pass one or more valid service names: %s\n", err.Error())
	}

	fmt.Fprintf(os.Stderr, "You can also control this container via 'systemctl stop %s'\n", ids[0].(*RemoteIdentifier).Id.UnitNameFor())
	Executor{
		On: ids,
		Serial: func(on Locator) jobs.Job {
			return &http.HttpStopContainerRequest{
				StoppedContainerStateRequest: jobs.StoppedContainerStateRequest{
					Id: on.(*RemoteIdentifier).Id,
				},
			}
		},
		Output:    os.Stdout,
		LocalInit: needsSystemd,
	}.StreamAndExit()
}

func containerStatus(cmd *cobra.Command, args []string) {
	if len(args) < 1 {
		fail(1, "Valid arguments: <id> ...\n")
	}
	ids, err := NewRemoteIdentifiers(args)
	if err != nil {
		fail(1, "You must pass one or more valid service names: %s\n", err.Error())
	}

	data, errors := Executor{
		On: ids,
		Serial: func(on Locator) jobs.Job {
			return &http.HttpContainerStatusRequest{
				ContainerStatusRequest: jobs.ContainerStatusRequest{
					Id: on.(*RemoteIdentifier).Id,
				},
			}
		},
		Output:    os.Stdout,
		LocalInit: needsSystemd,
	}.Gather()

	for i := range data {
		if buf, ok := data[i].(*bytes.Buffer); ok {
			if i > 0 {
				fmt.Fprintf(os.Stdout, "\n-------------\n")
			}
			buf.WriteTo(os.Stdout)
		}
	}
	if len(errors) > 0 {
		for i := range errors {
			fmt.Fprintf(os.Stderr, "Error: %s\n", errors[i])
		}
		os.Exit(1)
	}
	os.Exit(0)
}

func initGear(cmd *cobra.Command, args []string) {
	if len(args) != 2 || !(pre || post) || (pre && post) {
		fail(1, "Valid arguments: <id> <image_name> (--pre|--post)\n")
	}
	gearId, err := containers.NewIdentifier(args[0])
	if err != nil {
		fail(1, "Argument 1 must be a valid gear identifier: %s\n", err.Error())
	}

	switch {
	case pre:
		if err := containers.InitPreStart(conf.Docker.Socket, gearId, args[1]); err != nil {
			fail(2, "Unable to initialize container %s\n", err.Error())
		}
	case post:
		if err := containers.InitPostStart(conf.Docker.Socket, gearId); err != nil {
			fail(2, "Unable to initialize container %s\n", err.Error())
		}
	}
}

func genAuthKeys(cmd *cobra.Command, args []string) {
	if len(args) > 1 {
		fail(1, "Valid arguments: [<id>]\n")
	}

	var u *user.User
	var err error

	if len(args) == 1 {
		gearId, err := containers.NewIdentifier(args[0])
		if err != nil {
			fail(1, "Argument 1 must be a valid gear identifier: %s\n", err.Error())
		}
		if u, err = user.Lookup(gearId.LoginFor()); err != nil {
			fail(2, "Unable to lookup user: %s", err.Error())
		}
	} else {
		if u, err = user.LookupId(strconv.Itoa(os.Getuid())); err != nil {
			fail(2, "Unable to lookup user")
		}
	}

	if err := containers.GenerateAuthorizedKeys(conf.Docker.Socket, u); err != nil {
		fail(2, "Unable to generate authorized_keys file: %s\n", err.Error())
	}
}
