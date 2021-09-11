package utils

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/template"

	"github.com/fosshostorg/aarch64/daemons/internal/message"
	"go.uber.org/zap"
)

//go:embed templates/cloud-config.yml
var cloud_cfg_file string

//go:embed templates/netplan.yml
var netplan_cfg_file string

func CreateDomain(l *zap.Logger, data *message.VMData) error {
	// Check if Domain Already Exists. If not, Set it Up
	cmd := exec.Command("virsh", "list", "--all")
	output, err := cmd.Output()
	if err != nil {
		l.Error(
			"unable to access existing domains",
			zap.String("command", "virst list --all"),
			zap.Error(err),
		)
		return err
	}
	if !strings.Contains(string(output), data.ID) {
		// Cloud Config Template Parsing
		tmpl, err := template.New("cloud-config.yml").Parse(cloud_cfg_file)
		if err != nil {
			l.Error("failed to parse cloud-config.yml", zap.Error(err))
			return err
		}
		cloudConfigFile, err := os.Create(fmt.Sprintf("/tmp/%s-cloud-config.yml", data.ID))
		if err != nil {
			l.Error(
				"failed to create cloud config",
				zap.String("file", data.ID+"-cloud-config.yml"),
				zap.Error(err),
			)
			return err
		}
		defer cloudConfigFile.Close()
		if err = tmpl.Execute(cloudConfigFile, data); err != nil {
			l.Error(
				"failed to execute cloud config template",
				zap.String("file", data.ID+"-cloud-config.yml"),
				zap.Error(err),
			)
			return err
		}
		// Netplan Config Template Parsing
		tmpl, err = template.New("netplan.yml").Parse(netplan_cfg_file)
		if err != nil {
			l.Error("failed to parse netplan.yml", zap.Error(err))
			return err
		}
		netplanConfigFile, err := os.Create(fmt.Sprintf("/tmp/%s-network-config.yml", data.ID))
		if err != nil {
			l.Error(
				"failed to create netplan config",
				zap.String("file", data.ID+"-network-config.yml"),
				zap.Error(err),
			)
			return err
		}
		defer netplanConfigFile.Close()
		if err = tmpl.Execute(netplanConfigFile, data); err != nil {
			l.Error(
				"failed to execute netplan config",
				zap.String("file", data.ID+"-network-config.yml"),
				zap.Error(err),
			)
			return err
		}
		// Create Cloud Init Image
		if output, err = exec.Command(
			"cloud-localds",
			"-v",
			fmt.Sprintf("--network-config=/tmp/%s-network-config.yml", data.ID),
			fmt.Sprintf("/opt/aarch64/vms/%s-cloudinit.iso", data.ID),
			fmt.Sprintf("/tmp/%s-cloud-config.yml", data.ID),
		).Output(); err != nil {
			l.Error(
				"unable to create cloud-init image",
				zap.String(
					"command",
					fmt.Sprintf("cloud-localds -v --network-config=/tmp/%s-network-config.yml /opt/aarch64/vms/%s-cloudinit.iso /tmp/%s-cloud-config.yml", data.ID, data.ID, data.ID),
				),
				zap.ByteString("output", output),
				zap.Error(err),
			)
			return err
		}
		// Create VM Disk
		if output, err = exec.Command(
			"qemu-img", "create",
			"-f", "qcow2",
			"-F", "qcow2",
			"-o", fmt.Sprintf("backing_file=/opt/aarch64/images/%s.qcow2", data.Os),
			fmt.Sprintf("/opt/aarch64/vms/%s-disk.qcow2", data.ID),
		).Output(); err != nil {
			l.Error(
				"unable to create vm disk",
				zap.String(
					"command",
					fmt.Sprintf("qemu-img create -f qcow2 -F qcow2 -o backing_file=/opt/aarch64/images/%s.qcow2 /opt/aarch64/vms/%s-disk.qcow2", data.Os, data.ID),
				),
				zap.ByteString("output", output),
				zap.Error(err),
			)
			return err
		}
		// Resize VM Disk
		if output, err = exec.Command(
			"qemu-img", "resize",
			fmt.Sprintf("/opt/aarch64/vms/%s-disk.qcow2", data.ID),
			fmt.Sprintf("+%dG", data.Ssd-2),
		).Output(); err != nil {
			l.Error(
				"unable to resize vm disk",
				zap.String(
					"command",
					fmt.Sprintf("qemu-img resize /opt/aarch64/vms/%s-disk.qcow2 +%dG", data.ID, data.Ssd-2),
				),
				zap.ByteString("output", output),
				zap.Error(err),
			)
			return err
		}
		// Virt Install
		if output, err = exec.Command(
			"virt-install",
			"--boot", "uefi",
			"--arch", "aarch64",
			"--name", data.ID,
			"--description", fmt.Sprintf("%d", data.Password),
			"--memory", fmt.Sprintf("%d", data.Memory*1024),
			"--vcpus", fmt.Sprintf("%d", data.Vcpus),
			"--network", fmt.Sprintf("bridge=vbr%d,model=virtio", data.Index),
			"--import",
			"--disk", fmt.Sprintf("path=/opt/aarch64/vms/%s-disk.qcow2,bus=virtio", data.ID),
			"--disk", fmt.Sprintf("path=/opt/aarch64/vms/%s-cloudinit.iso,device=cdrom", data.ID),
			"--nographics", "--noautoconsole", "--autostart",
		).Output(); err != nil {
			l.Error(
				"unable to run virt-install",
				zap.ByteString("output", output),
				zap.Error(err),
			)
			return err
		}
	}
	return nil
}
