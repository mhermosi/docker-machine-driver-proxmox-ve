package proxmoxve

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"gopkg.in/resty.v1"

	sshrw "github.com/mosolovsa/go_cat_sshfilerw"

	"github.com/docker/machine/libmachine/drivers"
	"github.com/docker/machine/libmachine/mcnflag"
	"github.com/docker/machine/libmachine/state"
	"github.com/labstack/gommon/log"
	valid "github.com/asaskevich/govalidator"
)



// PVE Default values for connection and authentication
const pveDriverName                  string = "proxmoxve"
const pveDefaultHostname             string = "192.168.1.253"
const pveDefaultNodename             string = "pve"
const pveDefaultPort                 int    = 8006
const pveDefaultUsername             string = "root"
const pveDefaultRealm                string = "pam"

// PVE Default values for PVE resource constants
const pveDefaultStorageLocation      string = "local-lvm"
const pveDefaultStorageType          string = "raw"
const pveDefaultStorageImageLocation string = "local:iso/xxxx.iso"

// PVE VM Default values constants
const pveDefaultVmAgent              string = "1"
const pveDefaultVmAutoStart          string = "1"
const pveDefaultVmOsType             string = "l26"
const pveDefaultVmKvm                string = "1"

const pveDefaultVmGuestUserName      string = "docker"
const pveDefaultVmGuestUserPassword  string = "tcuser"

const pveDefaultVmRootDiskSizeGb     string = "16"
const pveDefaultVmMemorySizeGb       int    = 8


const pveDefaultVmNetBridge          string  = "vmbr0"
const pveDefaultVmNetModel           string  = "virtio"
const pveDefaultVmNetVlan            string  = "No VLAN"

const pveDefaultVmCpuSocketCount     string  = "1"
const pveDefaultVmCpuCoreCount       string  = "4"


// Driver for Proxmox VE
type Driver struct {
	*drivers.BaseDriver
	driver                 *ProxmoxVE

	// Basic Authentication for Proxmox VE
	Host                   string // Proxmox VE Server Host name
	Port                   int    // Proxmox VE Server listening port
	Node                   string // optional, node to create VM on, host used if omitted but must match internal node name
	User                   string // username
	Password               string // password
	Realm                  string // realm, e.g. pam, pve, etc.

	// File to load as boot image RancherOS/Boot2Docker
	ImageFile              string // in the format <storagename>:iso/<filename>.iso

	Pool                   string // pool to add the VM to (necessary for users with only pool permission)
	Storage                string // internal PVE storage name
	StorageType            string // Type of the storage (currently QCOW2 and RAW)
	DiskSize               string // disk size in GB
	Memory                 int    // memory in GB
	StorageFilename        string

	VMID                   string // VM ID only filled by create()
	GuestUsername          string // username to log into the guest OS
	GuestPassword          string // password to log into the guest OS to copy the public key

	driverDebug            bool   // driver debugging
	restyDebug             bool   // enable resty debugging

	NetBridge              string // Net was defaulted to vmbr0, but should accept any other config i.e vmbr1
	NetModel               string // Net Interface Model, [e1000, virtio, realtek, etc...]
	NetVlanTag             string // VLAN
	Cores                  string // # of cores on each cpu socket
	Sockets                string // # of cpu sockets

	GuestSSHPrivateKey     string
	GuestSSHPublicKey      string
	GuestSSHAuthorizedKeys string

}

func (d *Driver) debugf(format string, v ...interface{}) {
	if d.driverDebug {
		log.Infof(fmt.Sprintf(format, v...))
	}
}

func (d *Driver) debug(v ...interface{}) {
	if d.driverDebug {
		log.Info(v...)
	}
}

func (d *Driver) connectAPI() error {
	if d.driver == nil {
		d.debugf("Create called")

		d.debugf("Connecting to %s as %s@%s with password '%s'", d.Host, d.User, d.Realm, d.Password)
		c, err := GetProxmoxVEConnectionByValues(d.User, d.Password, d.Realm, d.Host)
		d.driver = c
		if err != nil {
			return fmt.Errorf("Could not connect to host '%s' with '%s@%s'", d.Host, d.User, d.Realm)
		}
		if d.restyDebug {
			c.EnableDebugging()
		}
		d.debugf("Connected to PVE version '" + d.driver.Version + "'")
	}
	return nil
}

func (d *Driver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_HOST",
			Name:   "proxmoxve-host",
			Usage:  "Server Hostname or IP Address",
			Value:  pveDefaultHostname,
		},
		mcnflag.IntFlag{
			EnvVar: "PROXMOXVE_PORT",
			Name:   "proxmoxve-port",
			Usage:  "Server port",
			Value:  pveDefaultPort,
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_NODE",
			Name:   "proxmoxve-node",
			Usage:  "Node name",
			Value:  pveDefaultNodename,
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_USER",
			Name:   "proxmoxve-user",
			Usage:  "Username",
			Value:  pveDefaultUsername,
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_REALM",
			Name:   "proxmoxve-realm",
			Usage:  "authentication Realm (default: pam)",
			Value:  pveDefaultRealm,
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_PASSWORD",
			Name:   "proxmoxve-password",
			Usage:  "user Password",
			Value:  "",
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_STORAGE",
			Name:   "proxmoxve-storage",
			Usage:  "Storage location for volume creation",
			Value:  pveDefaultStorageLocation,
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_STORAGE_TYPE",
			Name:   "proxmoxve-storage-type",
			Usage:  "Storage type (QCOW2 or RAW)",
			Value:  pveDefaultStorageType,
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_IMAGE_FILE",
			Name:   "proxmoxve-image-file",
			Usage:  "Storage location of the image file (e.g. local:iso/boot2docker.iso)",
			Value:  pveDefaultStorageImageLocation,
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_POOL",
			Name:   "proxmoxve-pool",
			Usage:  "Pool to attach VM",
			Value:  "",
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_DISKSIZE_GB",
			Name:   "proxmoxve-disksize-gb",
			Usage:  "VM Root Disk size in GB",
			Value:  pveDefaultVmRootDiskSizeGb,
		},
		mcnflag.IntFlag{
			EnvVar: "PROXMOXVE_MEMORY_GB",
			Name:   "proxmoxve-memory-gb",
			Usage:  "VM RAM Memory in GB",
			Value:  pveDefaultVmMemorySizeGb,
		},
		mcnflag.StringFlag{
			Name:   "proxmoxve-guest-username",
			Usage:  "Guest OS account Username (default docker for boot2docker)",
			Value:  pveDefaultVmGuestUserName,
		},
		mcnflag.StringFlag{
			Name:   "proxmoxve-guest-password",
			Usage:  "Guest OS account Password (default tcuser for boot2docker)",
			Value:  pveDefaultVmGuestUserPassword,
		},
		mcnflag.BoolFlag{
			Name:  "proxmoxve-resty-debug",
			Usage: "Enables the resty debugging",
		},
		mcnflag.BoolFlag{
			Name:  "proxmoxve-driver-debug",
			Usage: "Enables debugging in the driver",
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_NET_BRIDGE",
			Name:   "proxmoxve-net-bridge",
			Usage:  "Network Bridge (default vmbr0)",
			Value:  pveDefaultVmNetBridge,
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_NET_MODEL",
			Name:   "proxmoxve-net-model",
			Usage:  "Network Interface model (default virtio)",
			Value:  pveDefaultVmNetModel,
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_NET_VLANTAG",
			Name:   "proxmoxve-net-vlantag",
			Usage:  "Network VLan Tag (1 - 4094)",
			Value:  pveDefaultVmNetVlan,
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_CPU_SOCKETS",
			Name:   "proxmoxve-cpu-sockets",
			Usage:  "VM number of CPU Sockets (1 - 4)",
			Value:  pveDefaultVmCpuSocketCount,
		},

		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_CPU_CORES",
			Name:   "proxmoxve-cpu-cores",
			Usage:  "VM number of Cores per Socket (1 - 128)",
			Value:  pveDefaultVmCpuCoreCount,
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_GUEST_SSH_PRIVATE_KEY",
			Name:   "proxmoxve-guest-ssh-private-key",
			Usage:  "SSH Private Key on Guest OS",
			Value:  "",
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_GUEST_SSH_PUBLIC_KEY",
			Name:   "proxmoxve-guest-ssh-public-key",
			Usage:  "SSH Public Key on Guest OS",
			Value:  "",
		},
		mcnflag.StringFlag{
			EnvVar: "PROXMOXVE_GUEST_SSH_AUTHORIZED_KEYS",
			Name:   "proxmoxve-guest-ssh-authorized-keys",
			Usage:  "SSH Authorized Keys on Guest OS",
			Value:  "",
		},
	}
}

func (d *Driver) ping() bool {
	if d.driver == nil {
		return false
	}

	command := NodesNodeQemuVMIDAgentPostParameter{Command: "ping"}
	err := d.driver.NodesNodeQemuVMIDAgentPost(d.Node, d.VMID, &command)

	if err != nil {
		d.debug(err)
		return false
	}

	return true
}

// DriverName returns the name of the driver
func (d *Driver) DriverName() string {
	return pveDriverName
}

func (d *Driver) SetConfigFromFlags(flags drivers.DriverOptions) error {
	d.debug("SetConfigFromFlags called")
	d.ImageFile              = flags.String("proxmoxve-image-file")
	d.Host                   = flags.String("proxmoxve-host")
	d.Port                   = flags.Int("proxmoxve-port")
	d.Node                   = flags.String("proxmoxve-node")

	d.User                   = flags.String("proxmoxve-user")
	d.Realm                  = flags.String("proxmoxve-realm")
	d.Pool                   = flags.String("proxmoxve-pool")
	d.Password               = flags.String("proxmoxve-password")
	d.DiskSize               = flags.String("proxmoxve-disksize-gb")
	d.Storage                = flags.String("proxmoxve-storage")
	d.StorageType            = strings.ToLower(flags.String("proxmoxve-storage-type"))
	d.Memory                 = flags.Int("proxmoxve-memory-gb")
	d.Memory                *= 1024

	d.SwarmMaster            = flags.Bool("swarm-master")
	d.SwarmHost              = flags.String("swarm-host")

	d.SSHUser                = flags.String("proxmoxve-guest-username")
	d.GuestUsername          = flags.String("proxmoxve-guest-username")
	d.GuestPassword          = flags.String("proxmoxve-guest-password")

	d.driverDebug            = flags.Bool("proxmoxve-driver-debug")
	d.restyDebug             = flags.Bool("proxmoxve-resty-debug")

	if d.restyDebug {
		d.debug("enabling Resty debugging")
		resty.SetDebug(true)
	}

	d.NetBridge              = flags.String("proxmoxve-net-bridge")
	d.NetModel               = flags.String("proxmoxve-net-model")
	d.NetVlanTag             = flags.String("proxmoxve-net-vlantag")
	d.Sockets                = flags.String("proxmoxve-cpu-sockets")
	d.Cores                  = flags.String("proxmoxve-cpu-cores")

	d.GuestSSHPrivateKey     = flags.String("proxmoxve-guest-ssh-private-key")
	d.GuestSSHPublicKey      = flags.String("proxmoxve-guest-ssh-public-key")
	d.GuestSSHAuthorizedKeys = flags.String("proxmoxve-guest-ssh-authorized-keys")


	return nil
}

func (d *Driver) GetURL() (string, error) {
	ip, err := d.GetIP()
	if err != nil {
		return "", err
	}
	if ip == "" {
		return "", nil
	}
	return fmt.Sprintf("tcp://%s:2376", ip), nil
}

func (d *Driver) GetMachineName() string {
	return d.MachineName
}

func (d *Driver) GetIP() (string, error) {
	d.connectAPI()
	return d.driver.GetEth0IPv4(d.Node, d.VMID)
}

func (d *Driver) GetSSHHostname() (string, error) {
	return d.GetIP()
}

//func (d *Driver) GetSSHKeyPath() string {
//	return d.GetSSHKeyPath() + ".pub"
//}

func (d *Driver) GetSSHPort() (int, error) {
	if d.SSHPort == 0 {
		d.SSHPort = 22
	}

	return d.SSHPort, nil
}

func (d *Driver) GetSSHUsername() string {
	if d.SSHUser == "" {
		d.SSHUser = pveDefaultVmGuestUserName
	}

	return d.SSHUser
}

func (d *Driver) GetState() (state.State, error) {
	err := d.connectAPI()
	if err != nil {
		return state.Paused, err
	}

	if d.ping() {
		return state.Running, nil
	}
	return state.Paused, nil
}

func (d *Driver) PreCreateCheck() error {

	switch d.StorageType {
	case "raw":
		fallthrough
	case "qcow2":
		break
	default:
		return fmt.Errorf("storage type '%s' is not supported", d.StorageType)
	}

	err := d.connectAPI()
	if err != nil {
		return err
	}

	d.debug("Retrieving next ID")
	id, err := d.driver.ClusterNextIDGet(0)
	if err != nil {
		return err
	}
	d.debugf("Next ID was '%s'", id)
	d.VMID = id

	storageType, err := d.driver.GetStorageType(d.Node, d.Storage)
	if err != nil {
		return err
	}

	filename := "vm-" + d.VMID + "-disk-0"
	switch storageType {
	case "lvmthin":
		fallthrough
	case "zfs":
		fallthrough
	case "ceph":
		if d.StorageType != "raw" {
			return fmt.Errorf("type '%s' on storage '%s' does only support raw", storageType, d.Storage)
		}
	case "dir":
		filename += "." + d.StorageType
	}
	d.StorageFilename = filename

	// create and save a new SSH key pair
	keyfile := d.GetSSHKeyPath()
	keypath := path.Dir(keyfile)
	d.debugf("Generating new key pair at path '%s'", keypath)
	err = os.MkdirAll(keypath, 0755)
	if err != nil {
		return err
	}
	_, _, err = GetKeyPair(keyfile)

	return err
}

func (d *Driver) Create() error {

	volume := NodesNodeStorageStorageContentPostParameter{
		Filename: d.StorageFilename,
		Size:     d.DiskSize + "G",
		VMID:     d.VMID,
	}

	d.debugf("Creating disk volume '%s' with size '%s'", volume.Filename, volume.Size)
	err := d.driver.NodesNodeStorageStorageContentPost(d.Node, d.Storage, &volume)
	if err != nil {
		return err
	}

	net := fmt.Sprintf("model=%s,bridge=%s", d.NetModel, d.NetBridge)
	if valid.IsInt(d.NetVlanTag) {
		net = fmt.Sprintf("%s,tag=%d", net, d.NetVlanTag)
	}

	npp := NodesNodeQemuPostParameter{
		VMID:      d.VMID,
		Agent:     pveDefaultVmAgent,
		Autostart: pveDefaultVmAutoStart,
		Memory:    d.Memory,
		Cores:     d.Cores,
		Sockets:   d.Sockets,
		Net0:      net, // Added to support bridge differnet from vmbr0 (vlan tag should be supported as well)
		SCSI0:     d.Storage + ":" + volume.Filename,
		Ostype:    pveDefaultVmOsType,
		Name:      d.BaseDriver.MachineName,
		KVM:       pveDefaultVmKvm, // if you test in a nested environment, you may have to change this to 0 if you do not have nested virtualization
		Cdrom:     d.ImageFile,
		Pool:      d.Pool,
		SshKeys:   d.GuestSSHAuthorizedKeys,
	}

	if d.StorageType == "qcow2" {
		npp.SCSI0 = d.Storage + ":" + d.VMID + "/" + volume.Filename
	}
	d.debugf("Creating VM '%s' with '%d' of memory", npp.VMID, npp.Memory)
	err = d.driver.NodesNodeQemuPost(d.Node, &npp)
	if err != nil {
		return err
	}

	d.Start()
	return d.waitAndPrepareSSH()
}
func (d *Driver) waitAndPrepareSSH() error {
	d.debugf("waiting for VM to become active, first wait 10 seconds")
	time.Sleep(10 * time.Second)

	for !d.ping() {
		d.debugf("waiting for VM to become active")
		time.Sleep(2 * time.Second)
	}
	d.debugf("VM is active waiting more")
	time.Sleep(2 * time.Second)

	sshConfig := &ssh.ClientConfig{
		User: d.GetSSHUsername(),
		Auth: []ssh.AuthMethod{
			ssh.Password(d.GuestPassword)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	sshbasedir := "/home/" + d.GetSSHUsername() + "/.ssh"
	hostname, _ := d.GetSSHHostname()
	port, _ := d.GetSSHPort()
	clientstr := fmt.Sprintf("%s:%d", hostname, port)

	d.debugf("Creating directory '%s'", sshbasedir)
	conn, err := ssh.Dial("tcp", clientstr, sshConfig)
	if err != nil {
		return err
	}
	session, err := conn.NewSession()
	if err != nil {
		return err
	}

	var stdoutBuf bytes.Buffer
	session.Stdout = &stdoutBuf
	session.Run("mkdir -p " + sshbasedir)
	d.debugf(fmt.Sprintf("%s -> %s", hostname, stdoutBuf.String()))
	session.Close()

	d.debugf("Trying to copy to %s:%s", clientstr, sshbasedir)
	c, err := sshrw.NewSSHclt(clientstr, sshConfig)
	if err != nil {
		return err
	}

	// Open a file
	f, err := os.Open(d.GetSSHKeyPath() + ".pub")
	if err != nil {
		return err
	}

	// TODO: always fails with return status 127, but file was copied correclty
	c.WriteFile(f, sshbasedir+"/authorized_keys")
	// if err = c.WriteFile(f, sshbasedir+"/authorized_keys"); err != nil {
	// 	d.debugf("Error on file write: ", err)
	// }

	// Close the file after it has been copied
	defer f.Close()

	return err
}

func (d *Driver) Start() error {
	err := d.connectAPI()
	if err != nil {
		return err
	}
	return d.driver.NodesNodeQemuVMIDStatusStartPost(d.Node, d.VMID)
}

func (d *Driver) Stop() error {
	//d.MockState = state.Stopped
	return nil
}

func (d *Driver) Restart() error {
	d.Stop()
	d.Start()
	//d.MockState = state.Running
	return nil
}

func (d *Driver) Kill() error {
	//d.MockState = state.Stopped
	return nil
}

func (d *Driver) Remove() error {
	err := d.connectAPI()
	if err != nil {
		return err
	}
	return d.driver.NodesNodeQemuVMIDDelete(d.Node, d.VMID)
}

func (d *Driver) Upgrade() error {
	return nil
}

func NewDriver(hostName, storePath string) drivers.Driver {
	return &Driver{
		BaseDriver: &drivers.BaseDriver{
			SSHUser:     pveDefaultVmGuestUserName,
			MachineName: hostName,
			StorePath:   storePath,
		},
	}
}

func GetKeyPair(file string) (string, string, error) {
	// read keys from file
	_, err := os.Stat(file)
	if err == nil {
		priv, err := ioutil.ReadFile(file)
		if err != nil {
			fmt.Printf("Failed to read file - %s", err)
			goto genKeys
		}
		pub, err := ioutil.ReadFile(file + ".pub")
		if err != nil {
			fmt.Printf("Failed to read pub file - %s", err)
			goto genKeys
		}
		return string(pub), string(priv), nil
	}

	// generate keys and save to file
genKeys:
	pub, priv, err := GenKeyPair()
	err = ioutil.WriteFile(file, []byte(priv), 0600)
	if err != nil {
		return "", "", fmt.Errorf("Failed to write file - %s", err)
	}
	err = ioutil.WriteFile(file+".pub", []byte(pub), 0644)
	if err != nil {
		return "", "", fmt.Errorf("Failed to write pub file - %s", err)
	}

	return pub, priv, nil
}

func GenKeyPair() (string, string, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}

	privateKeyPEM := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}
	var private bytes.Buffer
	if err := pem.Encode(&private, privateKeyPEM); err != nil {
		return "", "", err
	}

	// generate public key
	pub, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return "", "", err
	}

	public := ssh.MarshalAuthorizedKey(pub)
	return string(public), private.String(), nil
}
