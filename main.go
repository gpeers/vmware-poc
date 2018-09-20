package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
	"log"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/mo"
	"text/tabwriter"
	"os/exec"
	"encoding/json"
	"bytes"
)

// getEnvString returns string from environment variable.
func getEnvString(v string, def string) string {
	r := os.Getenv(v)
	if r == "" {
		return def
	}

	return r
}

// getEnvBool returns boolean from environment variable.
func getEnvBool(v string, def bool) bool {
	r := os.Getenv(v)
	if r == "" {
		return def
	}

	switch strings.ToLower(r[0:1]) {
	case "t", "y", "1":
		return true
	}

	return false
}

const (
	envURL      = "GOVMOMI_URL"
	envUserName = "GOVMOMI_USERNAME"
	envPassword = "GOVMOMI_PASSWORD"
	envInsecure = "GOVMOMI_INSECURE"
	envProfilesPath = "INSPEC_PROFILES_PATH"
)

type TargetConfig struct {
	Profiles	[]string						`json:"profiles,omitempty"`
	Target 		string							`json:"target,omitempty"`
	User 		string							`json:"user,omitempty"`
	Password 	string 							`json:"target,omitempty"`
	Insecure 	bool							`json:"insecure,omitempty"`
	Reporter 	map[string]map[string]string 	`json:"reporter,omitempty"`
	LogLevel 	string							`json:"log-level,omitempty"`
}

/*
map[string][map[string][map[string][bool]]]
{"reporter": { "cli" : {"stdout" : true}, "json" : { "file" : "/tmp/output.json", "stdout" : false } }}
 */

var urlDescription = fmt.Sprintf("ESX or vCenter URL [%s]", envURL)
var urlFlag = flag.String("url", getEnvString(envURL, "https://username:password@host"+vim25.Path), urlDescription)

var insecureDescription = fmt.Sprintf("Don't verify the server's certificate chain [%s]", envInsecure)
var insecureFlag = flag.Bool("insecure", getEnvBool(envInsecure, false), insecureDescription)

func processOverride(u *url.URL) {
	envUsername := os.Getenv(envUserName)
	envPassword := os.Getenv(envPassword)

	// Override username if provided
	if envUsername != "" {
		var password string
		var ok bool

		if u.User != nil {
			password, ok = u.User.Password()
		}

		if ok {
			u.User = url.UserPassword(envUsername, password)
		} else {
			u.User = url.User(envUsername)
		}
	}

	// Override password if provided
	if envPassword != "" {
		var username string

		if u.User != nil {
			username = u.User.Username()
		}

		u.User = url.UserPassword(username, envPassword)
	}
}

// NewClient creates a govmomi.Client for use in the examples
func NewClient(ctx context.Context) (*govmomi.Client, error) {
	flag.Parse()

	// Parse URL from string
	u, err := soap.ParseURL(*urlFlag)
	if err != nil {
		return nil, err
	}

	// Override username and/or password as required
	processOverride(u)

	// Connect and log in to ESX or vCenter
	return govmomi.NewClient(ctx, u, *insecureFlag)
}

func main() {
	ctx := context.Background()

	c, err := NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}

	defer c.Logout(ctx)

	info := c.ServiceContent.About
	fmt.Printf("\nConnected to %s, version %s - %s\n\n", info.Name, info.Version, info.InstanceUuid)

	// Create view of VirtualMachine objects
	m := view.NewManager(c.Client)

	v, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
	if err != nil {
		log.Fatal(err)
	}

	defer v.Destroy(ctx)

	// Retrieve summary property for all machines
	// Reference: http://pubs.vmware.com/vsphere-60/topic/com.vmware.wssdk.apiref.doc/vim.VirtualMachine.html
	var vms []mo.VirtualMachine
	err = v.Retrieve(ctx, []string{"VirtualMachine"}, []string{ "summary" }, &vms)
	if err != nil {
		log.Fatal(err)
	}

	// Print summary per vm (see also: govc/vm/info.go)
	fmt.Printf("Datacenter VMs\n\n")
	w := new(tabwriter.Writer)

	// Format in tab-separated columns with a tab stop of 5.
	w.Init(os.Stdout, 0, 8, 0, '\t', 0)

	for _, vm := range vms {
		fmt.Fprintf(w, "%s\t%s\t%s\n", vm.Summary.Config.Name, vm.Summary.Config.GuestFullName, vm.Summary.Config.InstanceUuid)
	}

	w.Flush()

	// run inspec
	fmt.Printf("\nRunning InSpec...\n\n")

	var cmd *exec.Cmd

	// need to discover and hit the esxi hosts; inspec doesn't run vs. vcenter
	// Retrieve summary property for all hosts
	// Reference: http://pubs.vmware.com/vsphere-60/topic/com.vmware.wssdk.apiref.doc/vim.HostSystem.html
	var reporter = map[string]map[string]string{}
	reporter["cli"] = map[string]string{}
	reporter["json"] = map[string]string{}
	reporter["cli"]["stdout"] = "true"
	reporter["json"]["file"] = "~/go/src/github.com/gpeers/vmware-poc/output.json"
	reporter["json"]["stdout"] = "false"

	jsonConf := &TargetConfig {
		Profiles: 		[]string{os.Getenv(envProfilesPath) + "/vsphere-6.5-U1-security-configuration-guide"},
		Target: 		"vmware://172.16.20.43",
		User:			"root",
		Password: 		"password",
		Insecure: 		true,
		LogLevel: 		"debug",
		Reporter: 		reporter,
	}

	conf, err := json.Marshal(jsonConf)
	if err != nil {
		log.Fatal(err)
	}

	args := []string {}
	args = append(args, "exec", "--json-config=-")
	cmd = exec.CommandContext(ctx, "inspec", args...)
	fmt.Printf("config -> %s", bytes.NewBuffer(conf).String())
	cmd.Stdin = bytes.NewBuffer(conf)

	fmt.Printf("Running: echo '%+v' | inspec %s", jsonConf, strings.Join(args, " "))
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		fmt.Println("error!")
		log.Fatal(stderr.String())
	}

	fmt.Println(cmd.Stdout)
}