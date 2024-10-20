package keys

import (
	"errors"
	"fmt"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/foundriesio/fioctl/subcommands"
	"github.com/foundriesio/fioctl/x509"
)

var (
	createOnlineCA bool
	createLocalCA  bool
	hsmModule      string
	hsmPin         string
	hsmTokenLabel  string
)

func init() {
	cmd := &cobra.Command{
		Use:   "create <PKI Directory>",
		Short: "Create PKI infrastructure to manage mutual TLS for the device gateway",
		Run:   doCreateCA,
		Args:  cobra.ExactArgs(1),
		Long: `Perform a one-time operation to set up PKI infrastructure for managing
the device gateway. This command creates a few things:

### Root of trust for your factory: factory_ca.key / factory_ca.pem
The factory_ca keypair is generated by this command to define the PKI root of
trust for this factory.

 * factory_ca.key - An EC prime256v1 private key that should be STORED OFFLINE.
 * factory_ca.pem - The public x509 certificate that is shared with
   Foundries.io. Once set, all future PKI related changes will require proof
   you own this certificate.

### online-ca - A Foundries.io owned keypair to support lmp-device-register
In order for lmp-device-register to work, Foundries.io needs the ability to
sign client certificates for devices. If enabled, the factory_ca keypair will
sign the certificate signing request returned from the API.

This is optional.

### local-ca - A keypair you own
This keypair can be used for things like your manufacturing process where you
may set up devices without having to communicate with Foundries.io web
services. This keypair is capable of signing client certificates for devices.
If enabled, the local-ca.pem will be shared with the Foundries.io device gateway
so that it will trust the client certificate of devices signed with this
keypair.

This is optional.`,
	}
	caCmd.AddCommand(cmd)
	cmd.Flags().BoolVarP(&createOnlineCA, "online-ca", "", true, "Create an online CA owned by Foundries that works with lmp-device-register")
	cmd.Flags().BoolVarP(&createLocalCA, "local-ca", "", true, "Create a local CA that you can use for signing your own device certificates")
	cmd.Flags().StringVarP(&hsmModule, "hsm-module", "", "", "Create key on an PKCS#11 compatible HSM using this module")
	cmd.Flags().StringVarP(&hsmPin, "hsm-pin", "", "", "The PKCS#11 PIN to set up on the HSM, if using one")
	cmd.Flags().StringVarP(&hsmTokenLabel, "hsm-token-label", "", "", "The label of the HSM token created for this")
}

func getDeviceCaCommonName(factory string) string {
	user, err := api.UserAccessDetails(factory, "self")
	subcommands.DieNotNil(err)
	return "fio-" + user.PolisId
}

func doCreateCA(cmd *cobra.Command, args []string) {
	factory := viper.GetString("factory")
	certsDir := args[0]
	logrus.Debugf("Create CA for %s under %s", factory, certsDir)
	subcommands.DieNotNil(os.Chdir(certsDir))

	if len(hsmModule) > 0 {
		if len(hsmPin) == 0 {
			subcommands.DieNotNil(errors.New("--hsm-pin is required with --hsm-module"))
		}
		if len(hsmTokenLabel) == 0 {
			subcommands.DieNotNil(errors.New("--hsm-token-label is required with --hsm-module"))
		}
		x509.InitHsm(x509.HsmInfo{
			Module:     hsmModule,
			Pin:        hsmPin,
			TokenLabel: hsmTokenLabel,
		})
	}

	resp, err := api.FactoryCreateCA(factory)
	subcommands.DieNotNil(err)

	fmt.Println("Creating offline root CA for Factory")
	resp.RootCrt = x509.CreateFactoryCa(factory)

	fmt.Println("Signing Foundries TLS CSR")
	resp.TlsCrt = x509.SignTlsCsr(resp.TlsCsr)

	if createOnlineCA {
		fmt.Println("Signing Foundries CSR for online use")
		resp.CaCrt = x509.SignCaCsr(resp.CaCsr)
	}

	if createLocalCA {
		fmt.Println("Creating local device CA")
		if len(resp.CaCrt) > 0 {
			resp.CaCrt += "\n"
		}
		commonName := getDeviceCaCommonName(factory)
		resp.CaCrt += x509.CreateDeviceCa(commonName, factory)
	}

	fmt.Println("Uploading signed certs to Foundries")
	subcommands.DieNotNil(api.FactoryPatchCA(factory, resp))
}
