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
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/vim25/types"
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
	Target 		string								`json:"target,omitempty"`
	User 		string								`json:"user,omitempty"`
	Password 	string 								`json:"target,omitempty"`
	Insecure 	bool								`json:"insecure,omitempty"`
	Reporter 	map[string]map[string]interface{} 	`json:"reporter,omitempty"`
	LogLevel 	string								`json:"log-level,omitempty"`
}

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

	// set up InSpec reporter
	var reporter = map[string]map[string]interface{}{}
	reporter["cli"] = map[string]interface{}{}
	reporter["json"] = map[string]interface{}{}
	reporter["cli"]["stdout"] = true
	reporter["json"]["file"] = "output.json"
	reporter["json"]["stdout"] = false

	// get esxi hosts
	fmt.Println("\nGetting hosts...\n")
	f := find.NewFinder(c.Client, true)
	//pc := property.DefaultCollector(c.Client)

	dc, err := f.DatacenterOrDefault(ctx, "*")
	if err != nil {
		log.Fatal(err)
	}
	
	f.SetDatacenter(dc)

	hosts, err := f.HostSystemList(ctx, "*")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("there are %d hosts\n", len(hosts))

	var targets []TargetConfig

	for _, h := range hosts {
		fmt.Printf("host inventory path -> %v\n", h.InventoryPath)
		// don't mess with jj's management server!
		if !strings.Contains(h.InventoryPath, "172.16.20.44") {
			hvms, err := f.VirtualMachineList(ctx, h.InventoryPath + "/*")
			if err != nil {
				log.Fatal(err)
			}

			fmt.Printf("there are %d vms for host %s", len(hvms), h.Name())

			/*vmProps, err := vsphere.GetVirtualMachinesProperties(ctx, pc, vms)
			if err != nil {
				log.Errorf("Virtual machines properties errors: %s", err)
				return
			}
			for _, prop := range vmProps {
				s := prop.Summary
				log.Infof("======= VM ==========")
				log.Infof("Name: %s", s.Config.Name)
				// log.Infof("Path: %s", o.InventoryPath)
				log.Infof("UUID: %s", s.Config.Uuid)
				log.Infof("Guest name: %s", s.Config.GuestFullName)
				log.Infof("Memory: %dMB", s.Config.MemorySizeMB)
				log.Infof("CPU: %d vCPU(s)", s.Config.NumCpu)
				log.Infof("Power state: %s", s.Runtime.PowerState)
				log.Infof("Boot time: %s", s.Runtime.BootTime)
				log.Infof("IP address: %s", s.Guest.IpAddress)
			}*/


			for _, hvm := range hvms {
				var ips []string
				err := hvm.Properties(ctx, hvm.Reference(), []string{"guest.ipAddress"}, &ips)
				if err != nil {
					log.Fatal(err)
				}

				fmt.Printf("ips -> %+v\n", ips)

				// if vm is powered on
				ps, err := hvm.PowerState(ctx)
				if err != nil {
					log.Fatal(err)
				}

				// we only want to run against vms that are powered on (which takes
				// care of templates as well bc they can't be powered on)
				if ps == types.VirtualMachinePowerStatePoweredOn {
					fmt.Println("vm is powered on...")
					fmt.Printf("ip -> %s \n", ips[0])

					t := TargetConfig{
						Target:   ips[0],
						User:     "root",
						Password: "password",
						Insecure: true,
						Reporter: reporter,
						LogLevel: "debug",
					}

					targets = append(targets, t)
				}
			}
		}
	}
    // run inspec on host vms
    fmt.Println("\nRunning InSpec on host's vms...")
	for _, t := range targets {
		conf, err := json.Marshal(t)
		if err != nil {
			log.Fatal(err)
		}
		var cmd *exec.Cmd
		args := []string{}
		args = append(args, "exec", "inspec/vsphere-6.5-U1-security-configuration-guide", "--json-config=-")

		cmd = exec.CommandContext(ctx, "inspec", args...)
		fmt.Printf("config -> %s", bytes.NewBuffer(conf).String())
		cmd.Stdin = bytes.NewBuffer(conf)

		fmt.Printf("Running: echo '%+v' | inspec %s", t, strings.Join(args, " "))
		var out bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr

		err = cmd.Run()
		if err != nil {
			log.Fatal(stderr.String())
		}
	}

	// run inspec
	fmt.Printf("\nRunning InSpec on host...\n\n")

	var cmd *exec.Cmd

	// need to discover and hit the esxi hosts; inspec doesn't run vs. vcenter
	// Retrieve summary property for all hosts
	// Reference: http://pubs.vmware.com/vsphere-60/topic/com.vmware.wssdk.apiref.doc/vim.HostSystem.html
	jsonConf := &TargetConfig {
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

	args := []string{}
	args = append(args, "exec", "inspec/vsphere-6.5-U1-security-configuration-guide", "--json-config=-")

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
		log.Fatal(stderr.String())
	}

	//fmt.Println(out.String())
}
